// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package mcpauth drives opencode's interactive MCP OAuth command
// (`opencode mcp auth <name>`) and surfaces it over HTTP so the web UI can sign
// a user in to an OAuth-enabled MCP server (e.g. Atlassian), and signs out
// through opencode's server API.
//
// opencode owns the OAuth flow and its token store; `opencode mcp auth` prints
// an authorization URL to a TTY and then blocks waiting for the browser
// callback. We spawn it over a PTY, scrape the URL, expose it to the frontend,
// and poll for completion (confirmed by the server's stored credential).
package mcpauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/entra"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/mcp"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodev2"
)

// OpencodeBin lets tests substitute a fake opencode binary. When empty the
// resolved config.OpencodeBin (or $KWA_OPENCODE_BIN) is used.
var OpencodeBin = ""

func resolveBin() string {
	if OpencodeBin != "" {
		return OpencodeBin
	}
	if config.OpencodeBin != "" {
		return config.OpencodeBin
	}
	if v := env.Get("KWA_OPENCODE_BIN"); v != "" {
		return v
	}
	return config.ResolveOpencodeBin("")
}

// Stand-ins for opencode's server API that tests replace.
var (
	connectorStatuses = opencodev2.ConnectorStatuses
	connectorSignedIn = opencodev2.ConnectorSignedIn
	signOutConnector  = opencodev2.SignOutConnector
)

var (
	ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	// A generic authorization URL — MCP OAuth uses each server's own IdP, not a
	// fixed host. Prefer an https auth URL; trim common trailing punctuation.
	authURLRe = regexp.MustCompile(`https?://[^\s"'<>()]+`)
)

const (
	commandTimeout = 20 * time.Second
	authTimeout    = 10 * time.Minute
)

// AuthInfo is the JSON payload for the auth start/info/cancel endpoints.
type AuthInfo struct {
	Server    string `json:"server"`
	Running   bool   `json:"running"`
	Done      bool   `json:"done"`
	Success   bool   `json:"success"`
	VerifyURL string `json:"verify_url,omitempty"`
	Output    string `json:"output,omitempty"`
	Error     string `json:"error,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
	// Method is "entra" for Knowledge Worker Agent's own Microsoft sign-in, and
	// empty for opencode's.
	Method string `json:"method,omitempty"`
	// Account is who a Microsoft sign-in signed in as.
	Account string `json:"account,omitempty"`
}

// authState tracks the single in-flight MCP auth flow. Single-flight: only one
// server can be authenticating at a time (the UI drives one at a time).
type authState struct {
	mu        sync.Mutex
	server    string
	cmd       *exec.Cmd
	cancel    context.CancelFunc
	master    *os.File
	running   bool
	verifyURL string
	output    strings.Builder
	done      bool
	success   bool
	err       string
	startedAt time.Time
	// entra is Knowledge Worker Agent's own Microsoft sign-in, when it runs
	// instead of opencode's.
	entra   *entra.Pending
	account string
}

var auth = &authState{}

// oauthUnsupported lists MCP servers whose auth server does NOT support the
// browser OAuth flow (no OAuth dynamic client registration) and therefore must
// never be driven through `opencode mcp auth`. The hosted GitHub MCP server is
// authenticated with a bearer token (the user's GitHub Copilot access token,
// injected by mcp.EnsureGitHub) tied to the GitHub Copilot account login — not
// an MCP OAuth flow. Attempting OAuth against it fails with
// "unsupported path: /mcp". See internal/mcp/defaults.go (GitHubDefault).
var oauthUnsupported = map[string]string{
	"github": "GitHub MCP is authenticated automatically via your GitHub Copilot login, not an OAuth sign-in. Sign in to GitHub Copilot to connect it.",
}

// ErrOAuthUnsupported is returned by startAuth for servers that cannot use the
// browser OAuth flow (see oauthUnsupported).
type ErrOAuthUnsupported struct{ Msg string }

func (e ErrOAuthUnsupported) Error() string { return e.Msg }

// ErrNotEnabled is returned by startAuth for a server that is configured but
// switched off. Signing in would report success while the agent still could
// not use the server — the credentials are stored, but opencode never connects
// a disabled server — so we refuse and point the user at the toggle instead.
//
// With central connectors this is on the common path rather than an edge case: centrally
// provisioned servers arrive switched off by design, so "enable it, then sign
// in" is the normal first-time sequence. The wording therefore has to read as
// the next step, not as a failure.
type ErrNotEnabled struct{ Server string }

func (e ErrNotEnabled) Error() string {
	return fmt.Sprintf("Turn %q on first, then sign in. Signing in while it's off has no effect — opencode never connects a server that's switched off.", e.Server)
}

func (a *authState) resetLocked(server string) {
	a.server = server
	a.cmd = nil
	a.cancel = nil
	a.master = nil
	a.running = false
	a.verifyURL = ""
	a.output.Reset()
	a.done = false
	a.success = false
	a.err = ""
	a.startedAt = time.Time{}
	a.entra = nil
	a.account = ""
}

// startAuth spawns `opencode mcp auth <name>` over a PTY and begins scraping its
// output for the authorization URL.
func startAuth(server string) error {
	if msg, blocked := oauthUnsupported[strings.ToLower(strings.TrimSpace(server))]; blocked {
		return ErrOAuthUnsupported{Msg: msg}
	}
	// Enabling is a precondition, not a consequence, of signing in. A config
	// read error or an unknown name falls through: the auth attempt itself
	// will fail with a better message than we could invent here.
	if enabled, found, err := mcp.IsEnabled(server); err == nil && found && !enabled {
		return ErrNotEnabled{Server: server}
	}
	if handled, err := startEntra(server); handled {
		return err
	}
	auth.mu.Lock()
	defer auth.mu.Unlock()
	if (auth.running || auth.cmd != nil) && !auth.done {
		return fmt.Errorf("an authentication flow is already in progress")
	}
	auth.resetLocked(server)
	auth.running = true
	auth.startedAt = time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), authTimeout)
	cmd := opencodev2.Command(ctx, resolveBin(), "mcp", "auth", server)
	// The command already carries the server's password; keep it.
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = append(env,
		"DISPLAY=", // suppress auto browser-open on the server
		"WAYLAND_DISPLAY=",
		"TERM=xterm-256color",
		"OPENCODE_DISABLE_AUTOUPDATE=1",
	)
	// opencode sets up every folder it is run in; the shared scratch one is set up.
	if dir, err := opencodev2.ScratchDir(); err == nil {
		cmd.Dir = dir
	}

	master, slave, ptyErr := openPTY()
	var stdout io.Reader
	if ptyErr == nil {
		cmd.Stdin = slave
		cmd.Stdout = slave
		cmd.Stderr = slave
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
		stdout = master
	} else {
		pipeOut, err := cmd.StdoutPipe()
		if err != nil {
			cancel()
			auth.running = false
			auth.done = true
			auth.err = fmt.Sprintf("stdout pipe: %v", err)
			return fmt.Errorf("stdout pipe: %w", err)
		}
		cmd.Stderr = cmd.Stdout
		stdout = pipeOut
	}

	if err := cmd.Start(); err != nil {
		if master != nil {
			master.Close()
		}
		if slave != nil {
			slave.Close()
		}
		cancel()
		auth.running = false
		auth.done = true
		auth.err = fmt.Sprintf("start opencode mcp auth: %v", err)
		return fmt.Errorf("start opencode mcp auth: %w", err)
	}
	if slave != nil {
		slave.Close()
	}
	auth.cmd = cmd
	auth.cancel = cancel
	auth.master = master

	go consumeAuthOutput(server, cmd, stdout, master)
	return nil
}

func consumeAuthOutput(server string, cmd *exec.Cmd, stdout io.Reader, master *os.File) {
	buf := make([]byte, 4096)
	var partial []byte
	// opencode asks before replacing credentials it still holds. Selecting Sign
	// in is the answer, so press Enter on its default, Yes; unanswered, the
	// sign-in would hang until it times out. Prompts can arrive a byte at a time.
	var tail []byte
	answered := false
	answerReauth := func(chunk []byte) {
		if answered || master == nil {
			return
		}
		tail = append(tail, chunk...)
		if len(tail) > 2048 {
			tail = tail[len(tail)-2048:]
		}
		if strings.Contains(strings.Join(strings.Fields(ansiRe.ReplaceAllString(string(tail), "")), ""), "Re-authenticate?") {
			answered = true
			master.Write([]byte("\r"))
		}
	}

	flushLine := func(line string) {
		clean := strings.TrimSpace(ansiRe.ReplaceAllString(line, ""))
		if clean == "" {
			return
		}
		auth.mu.Lock()
		auth.output.WriteString(clean)
		auth.output.WriteByte('\n')
		if auth.verifyURL == "" {
			if m := authURLRe.FindString(clean); m != "" {
				auth.verifyURL = strings.TrimRight(m, ".,;)")
			}
		}
		auth.mu.Unlock()
	}

	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			answerReauth(buf[:n])
			partial = append(partial, buf[:n]...)
			for {
				idx := bytes.IndexByte(partial, '\n')
				if idx < 0 {
					break
				}
				flushLine(string(bytes.TrimRight(partial[:idx], "\r")))
				partial = partial[idx+1:]
			}
			// The URL is often printed without a trailing newline while opencode
			// blocks "Waiting for authorization"; inspect the partial too.
			if len(partial) > 0 {
				flushLine(string(partial))
				partial = partial[:0]
			}
		}
		if err != nil {
			if len(partial) > 0 {
				flushLine(string(bytes.TrimRight(partial, "\r")))
			}
			break
		}
	}

	werr := cmd.Wait()
	if master != nil {
		master.Close()
	}

	// Confirm success by the stored credential rather than trusting the exit
	// code — opencode stores it just before exiting, then reconnects the server.
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	authed, _ := connectorSignedIn(ctx, server)
	cancel()
	auth.mu.Lock()
	auth.master = nil
	auth.running = false
	auth.done = true
	cancelled := auth.err == "cancelled by user"
	switch {
	case cancelled:
		auth.success = false
	case authed:
		auth.success = true
		auth.err = ""
	case werr != nil:
		auth.success = false
		if auth.err == "" {
			auth.err = authErrorMessage(werr, auth.output.String())
		}
	default:
		auth.success = false
		if auth.err == "" {
			auth.err = "authentication finished but the server is still not authenticated"
		}
	}
	success := auth.success
	auth.mu.Unlock()

	// Note there is deliberately no "enable on success" here: startAuth refuses
	// to run for a disabled server, so a flow that gets this far was already
	// enabled by the user. Nothing turns a server on except the user.

	if success && onAuthSuccess != nil {
		go onAuthSuccess()
	}
}

func authErrorMessage(err error, output string) string {
	low := strings.ToLower(output)
	if strings.Contains(low, "timeout") || strings.Contains(low, "timed out") {
		return "authentication timed out waiting for approval. Retry and approve in your browser."
	}
	return err.Error()
}

var onAuthSuccess func()

// SetOnAuthSuccess registers a callback fired (in a goroutine) after a
// successful MCP auth — e.g. to refresh the server list.
func SetOnAuthSuccess(fn func()) { onAuthSuccess = fn }

func snapshot() AuthInfo {
	auth.mu.Lock()
	defer auth.mu.Unlock()
	return snapshotLocked()
}

func snapshotLocked() AuthInfo {
	started := ""
	if !auth.startedAt.IsZero() {
		started = auth.startedAt.UTC().Format(time.RFC3339)
	}
	method := ""
	if auth.entra != nil {
		method = "entra"
	}
	return AuthInfo{
		Server:    auth.server,
		Running:   (auth.running || auth.cmd != nil) && !auth.done,
		Done:      auth.done,
		Success:   auth.success,
		VerifyURL: auth.verifyURL,
		Output:    auth.output.String(),
		Error:     auth.err,
		StartedAt: started,
		Method:    method,
		Account:   auth.account,
	}
}

// ServerAuthenticated reports whether an MCP server is authenticated, as
// opencode's server reports it.
func ServerAuthenticated(server string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	statuses, err := connectorStatuses(ctx)
	if err != nil {
		return false
	}
	authed := statuses[strings.ToLower(strings.TrimSpace(server))]
	// Keep the cache warm as a side effect of a live check.
	db.SetMcpStatus(strings.ToLower(strings.TrimSpace(server)), authed)
	return authed
}

// refreshRunning guards the background refresh so concurrent callers collapse
// into one status read.
//
// This matters because the refresh is fired from request handlers. Before the
// batched status endpoint existed the connectors modal read status one server
// at a time, and every cached read kicked off its own background refresh —
// opening the modal with sixteen configured servers ran sixteen concurrent
// status reads to compute a result all sixteen shared. They also raced each
// other writing the same cache rows.
var refreshRunning atomic.Bool

// refreshInBackground refreshes the status cache unless a refresh is already
// under way, in which case it does nothing: the in-flight run is about to write
// exactly the same answer.
func refreshInBackground() {
	if !refreshRunning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer refreshRunning.Store(false)
		RefreshAllStatuses()
	}()
}

// RefreshAllStatuses reads every server's status from opencode's server ONCE
// and updates the DB cache for each. Safe to call from a background goroutine.
func RefreshAllStatuses() {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	statuses, err := connectorStatuses(ctx)
	if err != nil {
		return
	}
	for name, connected := range statuses {
		db.SetMcpStatus(name, connected)
	}
}

// Logout removes the stored OAuth credentials for a server through opencode's
// server API.
func Logout(server string) error {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	if entra.Has(server) {
		if err := entra.Forget(server); err != nil {
			log.Printf("[ERROR] mcpauth: forget the Microsoft sign-in of %q: %v", server, err)
		}
	}
	err := signOutConnector(ctx, server)
	if err == nil {
		// Reflect the sign-out in the cache immediately.
		db.SetMcpStatus(strings.ToLower(strings.TrimSpace(server)), false)
		// Signing out makes the server unusable again, so take it back out of
		// the model's tool surface rather than leaving it to fail per turn.
		// This is the one place we change the toggle for the user, and it only
		// ever narrows what the agent can reach; the sign-out confirmation says
		// so. Turning it back on is then the first step of signing in again.
		if _, derr := mcp.SetEnabled(server, false); derr != nil {
			log.Printf("[ERROR] mcpauth: could not disable %q after sign-out: %v", server, derr)
		}
	}
	return err
}

// ── HTTP handlers ───────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// HandleAuthStart — POST /api/mcp/servers/{name}/auth/start.
func HandleAuthStart(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "server name is required"})
		return
	}
	if err := startAuth(name); err != nil {
		if _, ok := err.(ErrOAuthUnsupported); ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if strings.Contains(err.Error(), "already in progress") {
			writeJSON(w, http.StatusOK, snapshot())
			return
		}
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	// Block briefly so the first response already carries the URL (opencode
	// prints it asynchronously after the OAuth flow starts).
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		info := snapshot()
		if info.VerifyURL != "" || info.Done {
			writeJSON(w, http.StatusOK, info)
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	writeJSON(w, http.StatusOK, snapshot())
}

// HandleAuthInfo — GET /api/mcp/servers/{name}/auth/info (poll endpoint).
func HandleAuthInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, snapshot())
}

// HandleAuthCancel — POST /api/mcp/servers/{name}/auth/cancel.
func HandleAuthCancel(w http.ResponseWriter, _ *http.Request) {
	auth.mu.Lock()
	if auth.cancel != nil && !auth.done {
		auth.cancel()
		auth.err = "cancelled by user"
		auth.done = true
		auth.running = false
	}
	info := snapshotLocked()
	auth.mu.Unlock()
	writeJSON(w, http.StatusOK, info)
}

// HandleLogout — POST /api/mcp/servers/{name}/logout.
func HandleLogout(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "server name is required"})
		return
	}
	if err := Logout(name); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// HandleStatus — GET /api/mcp/servers/{name}/status.
// Serves the cached authenticated state instantly. Pass ?fresh=1 to force a
// live (slow) re-check. When the cache is missing, it does a one-time live
// check so the first read is still correct.
func HandleStatus(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "server name is required"})
		return
	}
	key := strings.ToLower(name)

	if r.URL.Query().Get("fresh") == "1" {
		writeJSON(w, http.StatusOK, map[string]any{
			"server": name, "authenticated": ServerAuthenticated(name), "cached": false,
		})
		return
	}

	if authed, known := db.GetMcpStatus(key); known {
		// Serve the cached value immediately; refresh in the background so the
		// next read reflects any change.
		refreshInBackground()
		writeJSON(w, http.StatusOK, map[string]any{
			"server": name, "authenticated": authed, "cached": true,
		})
		return
	}

	// No cache yet — do a live check (also warms the cache).
	writeJSON(w, http.StatusOK, map[string]any{
		"server": name, "authenticated": ServerAuthenticated(name), "cached": false,
	})
}

// coldRefreshAttempted records whether HandleAllStatus has already paid for one
// synchronous refresh. Without it, a workspace where the status read yields
// nothing — no servers configured, or opencode's server down — would re-run
// it, and wait out its 20s timeout, on every single request.
var coldRefreshAttempted atomic.Bool

// HandleAllStatus — GET /api/mcp/status.
//
// Returns the cached authenticated state for every known MCP server in one
// response, so the connectors modal can render its status pills and filter
// counts from a single request instead of one per server. Pass ?fresh=1 to
// force a live re-check before answering.
//
// Servers absent from the map have no cached status: either they are disabled
// (opencode does not connect, and does not list, a disabled server) or nothing
// has checked them yet. The frontend treats absent as "not authenticated",
// which is the safe reading — a disabled server is off regardless, and an
// unchecked one resolves on the next refresh.
func HandleAllStatus(w http.ResponseWriter, r *http.Request) {
	fresh := r.URL.Query().Get("fresh") == "1"
	if fresh {
		RefreshAllStatuses()
		writeAllStatus(w, db.AllMcpStatus(), false)
		return
	}

	cached := db.AllMcpStatus()
	if len(cached) == 0 && coldRefreshAttempted.CompareAndSwap(false, true) {
		// First read on a cold cache: pay for one synchronous list so the modal
		// opens with correct state rather than showing everything as signed out.
		RefreshAllStatuses()
		writeAllStatus(w, db.AllMcpStatus(), false)
		return
	}

	// Serve the cache immediately and refresh behind the response.
	refreshInBackground()
	writeAllStatus(w, cached, true)
}

func writeAllStatus(w http.ResponseWriter, statuses map[string]bool, cached bool) {
	// Always an object, never null, so the frontend can index it unconditionally.
	servers := map[string]map[string]bool{}
	for name, authed := range statuses {
		servers[name] = map[string]bool{"authenticated": authed}
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": servers, "cached": cached})
}

// isLoopbackHost reports whether host resolves only to loopback addresses. Used
// to constrain callback forwarding to opencode's own local listener.
func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// callbackTarget is where the running sign-in sends the browser back to: the
// redirect_uri of its authorization URL. A connector can set its own with
// oauth.redirectUri, and Figma needs that: it accepts OpenCode only at /callback.
func callbackTarget() *url.URL {
	auth.mu.Lock()
	v := auth.verifyURL
	auth.mu.Unlock()
	a, err := url.Parse(v)
	if v == "" || err != nil {
		return nil
	}
	r, err := url.Parse(a.Query().Get("redirect_uri"))
	if err != nil || r.Host == "" {
		return nil
	}
	return r
}

// DeliverCallback forwards a pasted OAuth callback URL to opencode's local
// listener server-side. In this deployment the user's browser cannot reach the
// server's 127.0.0.1 callback port, so they paste the redirected URL and we
// deliver it from the server (which shares the host with the opencode process).
//
// Security: only loopback hosts are allowed, on the running sign-in's callback
// port and path (or an /oauth/callback path while none is known), so this
// endpoint cannot be turned into a general SSRF primitive.
func DeliverCallback(rawURL string) (int, string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return 0, "", fmt.Errorf("callback URL is required")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0, "", fmt.Errorf("invalid callback URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return 0, "", fmt.Errorf("callback URL must be http(s)")
	}
	if !isLoopbackHost(u.Hostname()) {
		return 0, "", fmt.Errorf("callback host must be localhost/127.0.0.1 (paste the URL your browser was redirected to)")
	}
	if t := callbackTarget(); t != nil {
		if u.Port() != t.Port() || u.Path != t.Path {
			return 0, "", fmt.Errorf("that isn't the address this sign-in is waiting for")
		}
	} else if !strings.Contains(u.Path, "/oauth/callback") {
		return 0, "", fmt.Errorf("that doesn't look like an OAuth callback URL")
	}
	if u.Query().Get("code") == "" {
		return 0, "", fmt.Errorf("the callback URL is missing the authorization code")
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(u.String())
	if err != nil {
		return 0, "", fmt.Errorf("could not reach the local callback listener (is the sign-in still waiting?): %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, string(body), nil
}

// HandleAuthCallback — POST /api/mcp/servers/{name}/auth/callback, body
// {"url": "<pasted callback URL>"}. Forwards it to opencode's local listener to
// complete the sign-in, then returns the current auth snapshot.
func HandleAuthCallback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	r.Body.Close()

	if p := pendingEntra(); p != nil {
		completeEntra(w, p, body.URL)
		return
	}
	code, _, err := DeliverCallback(body.URL)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// Give opencode a moment to complete the token exchange + persist, then
	// return the (likely updated) snapshot so the frontend reflects success.
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if snapshot().Done {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          code >= 200 && code < 400,
		"status_code": code,
		"info":        snapshot(),
	})
}
