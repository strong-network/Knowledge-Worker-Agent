// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package opencodeauth manages GitHub Copilot authentication for the opencode
// CLI (github.com/anomalyco/opencode). This is the only sign-in flow — it
// authenticates opencode's github-copilot provider.
//
// opencode stores provider credentials in ~/.local/share/opencode/auth.json and
// exposes them through `opencode auth login`/`opencode auth list`. The login
// command is interactive: it prints a github.com/login/device URL plus a
// one-time code and then blocks until the user authorizes in their browser
// (the GitHub OAuth *device flow*). There is no headless/token login for the
// github-copilot provider, so we drive `opencode auth login --provider
// github-copilot` over a PTY, scrape the URL + code from its output, surface
// them to the web UI, and poll until the flow completes.
//
// All process execution uses exec.Command with an argument vector (never a
// shell), consistent with the rest of the codebase.
package opencodeauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
)

// Provider is the opencode provider id for GitHub Copilot. opencode keys its
// credentials and `models <provider>` lookups by this id.
const Provider = "github-copilot"

// UserAgent names Knowledge Worker Agent, and its version once main sets it, to GitHub.
var UserAgent = "KnowledgeWorkerAgent"

// OpencodeBin lets tests substitute a fake opencode binary. When empty the
// resolved config.OpencodeBin (or $KWA_OPENCODE_BIN) is used.
var OpencodeBin = ""

// commandTimeout bounds the short-lived `opencode auth logout` call.
var commandTimeout = 20 * time.Second

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

// Status is the response payload of GET /api/opencode/auth/status.
type Status struct {
	// Authenticated is true when opencode has a stored github-copilot credential.
	Authenticated bool `json:"authenticated"`
	// Provider echoes the provider id ("github-copilot").
	Provider string `json:"provider"`
}

// ansiRe strips ANSI/VT100 escape sequences from opencode's coloured output so
// the plain text can be matched.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// CheckStatus reports whether opencode holds a stored github-copilot credential.
// It reads opencode's credential store, as `opencode auth list` does, without
// starting opencode on every check.
func CheckStatus() Status {
	return Status{Provider: Provider, Authenticated: HasCredential(Provider)}
}

// HandleStatus answers GET /api/opencode/auth/status.
func HandleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, CheckStatus())
}

// ── Login flow (GitHub OAuth device flow, driven directly over HTTP) ──
//
// Older versions of `opencode auth login` printed the device code directly, so
// we used to drive that command over a PTY and scrape its output. Newer opencode
// (1.17+) puts the device flow behind interactive TUI prompts ("Add credential"
// → method → "Select GitHub deployment type") that can't be scraped reliably,
// which wedged sign-in on "Starting sign-in…".
//
// Instead we now run the GitHub OAuth **device flow ourselves** against GitHub's
// public HTTP endpoints (no PTY, no TUI), then write the resulting token into
// opencode's credential store (auth.json) in the format opencode expects. This
// is fully deterministic and version-independent.

// githubClientID is the GitHub OAuth client id used for the device flow.
//
// This MUST be opencode's own GitHub App client id — a generic GitHub Copilot
// client id yields a token opencode's github-copilot provider rejects. The
// value below is the client id opencode itself uses for its device-flow login
// (see anomalyco/opencode: packages/opencode/src/plugin/github-copilot/copilot.ts,
// const CLIENT_ID), so the token we obtain and write to auth.json is accepted
// by opencode. It is a public client id; the device flow needs no client
// secret. Overridable via KWA_GITHUB_CLIENT_ID.
func githubClientID() string {
	if v := strings.TrimSpace(env.Get("KWA_GITHUB_CLIENT_ID")); v != "" {
		return v
	}
	return "Ov23li8tweQw6odWQebz"
}

// GitHub device-flow endpoints. Package vars (not consts) so tests can point
// them at an httptest server.
var (
	deviceCodeURL  = "https://github.com/login/device/code"
	accessTokenURL = "https://github.com/login/oauth/access_token"
)

const deviceScope = "read:user"

// LoginInfo is the response payload of GET /api/opencode/auth/login/info.
type LoginInfo struct {
	Running   bool   `json:"running"`
	Done      bool   `json:"done"`
	Success   bool   `json:"success"`
	VerifyURL string `json:"verify_url,omitempty"`
	UserCode  string `json:"user_code,omitempty"`
	Output    string `json:"output,omitempty"`
	Error     string `json:"error,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
}

type loginState struct {
	mu        sync.Mutex
	cancel    context.CancelFunc
	running   bool
	verifyURL string
	userCode  string
	output    strings.Builder
	done      bool
	success   bool
	err       string
	startedAt time.Time
}

var login = &loginState{}

// onLoginSuccess, if set, is invoked (in a goroutine) after a login flow
// completes successfully. main wires this to refresh the opencode model cache.
//
// Guarded for the same reason as onGitHubTokenChange: registered from the
// startup goroutine, read from the login-flow goroutine.
var (
	loginSuccessMu sync.RWMutex
	onLoginSuccess func()
)

// SetOnLoginSuccess registers a callback fired after a successful login.
func SetOnLoginSuccess(fn func()) {
	loginSuccessMu.Lock()
	defer loginSuccessMu.Unlock()
	onLoginSuccess = fn
}

// fireLoginSuccess invokes the registered hook, if any, in a goroutine.
func fireLoginSuccess() {
	loginSuccessMu.RLock()
	fn := onLoginSuccess
	loginSuccessMu.RUnlock()
	if fn != nil {
		go fn()
	}
}

// httpClient bounds each GitHub HTTP call.
var httpClient = &http.Client{Timeout: 20 * time.Second}

// HandleLoginStart answers POST /api/opencode/auth/login/start. It kicks off the
// GitHub device flow (if none is in flight) and returns the current LoginInfo —
// which, because the device code is fetched synchronously, already carries the
// user code on the first response.
func HandleLoginStart(w http.ResponseWriter, _ *http.Request) {
	if err := startLogin(); err != nil {
		if strings.Contains(err.Error(), "already in progress") {
			writeJSON(w, http.StatusOK, snapshotLogin())
			return
		}
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, snapshotLogin())
}

// HandleLoginInfo answers GET /api/opencode/auth/login/info — used by the UI to poll.
func HandleLoginInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, snapshotLogin())
}

// HandleLoginCancel answers POST /api/opencode/auth/login/cancel.
func HandleLoginCancel(w http.ResponseWriter, _ *http.Request) {
	login.mu.Lock()
	if login.cancel != nil && !login.done {
		login.cancel()
		login.err = "cancelled by user"
		login.done = true
		login.running = false
	}
	info := snapshotLoginLocked()
	login.mu.Unlock()
	writeJSON(w, http.StatusOK, info)
}

// deviceCodeResp is GitHub's response to the device-code request.
type deviceCodeResp struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// tokenResp is GitHub's response to the access-token poll.
type tokenResp struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	// Interval is GitHub's requested minimum polling interval (seconds). It is
	// present on "slow_down" responses and MUST be honored — polling faster
	// keeps GitHub returning "slow_down" indefinitely, so the flow would never
	// complete even after the user approves.
	Interval int `json:"interval"`
}

func startLogin() error {
	login.mu.Lock()
	if login.running && !login.done {
		login.mu.Unlock()
		return fmt.Errorf("a login flow is already in progress")
	}
	resetLoginLocked()
	login.running = true
	login.startedAt = time.Now()
	login.mu.Unlock()

	// Fetch the device code synchronously so the very first /start response
	// already carries the user code + verification URL for the UI.
	dc, err := requestDeviceCode()
	if err != nil {
		login.mu.Lock()
		login.running = false
		login.done = true
		login.success = false
		login.err = "could not start GitHub sign-in: " + err.Error()
		login.mu.Unlock()
		return nil // reported via LoginInfo, not as an HTTP error
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	login.mu.Lock()
	login.cancel = cancel
	login.verifyURL = dc.VerificationURI
	login.userCode = dc.UserCode
	login.output.WriteString("Device code requested. Waiting for authorization…\n")
	login.mu.Unlock()

	log.Printf("[opencodeauth] device code issued (client_id=%s, verify=%s); awaiting user approval", githubClientID(), dc.VerificationURI)
	go pollForToken(ctx, dc)
	return nil
}

// requestDeviceCode asks GitHub for a device + user code.
func requestDeviceCode() (*deviceCodeResp, error) {
	form := url.Values{}
	form.Set("client_id", githubClientID())
	form.Set("scope", deviceScope)
	req, err := http.NewRequest(http.MethodPost, deviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device code request returned %d: %s", resp.StatusCode, firstNonEmptyLine(string(body)))
	}
	var dc deviceCodeResp
	if err := json.Unmarshal(body, &dc); err != nil {
		return nil, fmt.Errorf("unexpected device code response")
	}
	if dc.DeviceCode == "" || dc.UserCode == "" {
		return nil, fmt.Errorf("GitHub did not return a device code")
	}
	if dc.Interval <= 0 {
		dc.Interval = 5
	}
	if dc.VerificationURI == "" {
		dc.VerificationURI = "https://github.com/login/device"
	}
	return &dc, nil
}

// pollForToken polls GitHub's token endpoint until the user authorizes, then
// persists the token to opencode's auth store.
func pollForToken(ctx context.Context, dc *deviceCodeResp) {
	interval := time.Duration(dc.Interval) * time.Second
	if interval < time.Second {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	finish := func(success bool, errMsg string) {
		login.mu.Lock()
		login.running = false
		login.done = true
		login.success = success
		if login.err == "" {
			login.err = errMsg
		}
		login.mu.Unlock()
		if success {
			log.Printf("[opencodeauth] device flow completed: authorized")
		} else {
			log.Printf("[opencodeauth] device flow ended without success: %s", errMsg)
		}
		if success {
			fireLoginSuccess()
		}
	}

	log.Printf("[opencodeauth] polling for authorization (interval=%s)", interval)
	for {
		select {
		case <-ctx.Done():
			login.mu.Lock()
			cancelled := login.err == "cancelled by user"
			login.mu.Unlock()
			if cancelled {
				finish(false, "")
			} else {
				finish(false, "sign-in timed out waiting for authorization. Retry and approve the code in your browser.")
			}
			return
		case <-ticker.C:
			tok, retry, err := requestToken(dc.DeviceCode)
			if err != nil {
				finish(false, err.Error())
				return
			}
			if retry != 0 {
				// slow_down: GitHub asks us to back off. Honor its interval.
				if retry > 0 {
					log.Printf("[opencodeauth] slow_down from GitHub; backing off to %s", retry)
					ticker.Reset(retry)
				}
				continue
			}
			if tok == "" {
				continue // authorization_pending
			}
			if err := writeOpencodeToken(tok); err != nil {
				log.Printf("[opencodeauth] token received but saving credential failed: %v", err)
				finish(false, "signed in, but saving the credential failed: "+err.Error())
				return
			}
			login.mu.Lock()
			login.output.WriteString("Authorized. Credential saved.\n")
			login.err = ""
			login.mu.Unlock()
			log.Printf("[opencodeauth] token received and written to %s", opencodeAuthPath())
			finish(true, "")
			return
		}
	}
}

// requestToken polls the access-token endpoint once. Returns (token, retryAfter,
// error). retryAfter>0 means "slow down and keep polling"; token=="" with
// retryAfter==0 means "still pending".
func requestToken(deviceCode string) (string, time.Duration, error) {
	form := url.Values{}
	form.Set("client_id", githubClientID())
	form.Set("device_code", deviceCode)
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	req, err := http.NewRequest(http.MethodPost, accessTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var tr tokenResp
	_ = json.Unmarshal(body, &tr)
	if tr.AccessToken != "" {
		return tr.AccessToken, 0, nil
	}
	switch tr.Error {
	case "authorization_pending", "":
		return "", 0, nil
	case "slow_down":
		// Honor GitHub's requested interval. Per RFC 8628 §3.5 the client must
		// increase its polling interval by at least 5s; GitHub also returns the
		// new required interval explicitly. Using a fixed value below GitHub's
		// demand causes perpetual "slow_down" and a flow that never completes.
		wait := time.Duration(tr.Interval) * time.Second
		if wait <= 0 {
			wait = 10 * time.Second
		}
		return "", wait, nil
	case "expired_token":
		return "", 0, fmt.Errorf("the code expired before you approved it. Please retry.")
	case "access_denied":
		return "", 0, fmt.Errorf("authorization was denied.")
	default:
		msg := tr.ErrorDescription
		if msg == "" {
			msg = tr.Error
		}
		return "", 0, fmt.Errorf("GitHub sign-in error: %s", msg)
	}
}

// opencodeAuthPath returns the path to opencode's credential store, honoring
// XDG_DATA_HOME (default ~/.local/share/opencode/auth.json).
func opencodeAuthPath() string {
	base := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			home = "/home/developer"
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "opencode", "auth.json")
}

// GitHubToken returns the stored github-copilot access token from opencode's
// credential store (auth.json), or "" if none is present or the store is
// unreadable. This is the same token obtained via the device-flow login; it is
// used to authenticate the hosted GitHub MCP server via a bearer header (that
// server does not support the OAuth dynamic-client-registration flow).
func GitHubToken() string {
	raw, err := os.ReadFile(opencodeAuthPath())
	if err != nil || len(raw) == 0 {
		return ""
	}
	var creds map[string]any
	if err := json.Unmarshal(raw, &creds); err != nil {
		return ""
	}
	entry, _ := creds[Provider].(map[string]any)
	if entry == nil {
		return ""
	}
	access, _ := entry["access"].(string)
	return strings.TrimSpace(access)
}

// writeOpencodeToken merges a github-copilot oauth credential into opencode's
// auth.json (preserving any other providers) with 0600 perms.
func writeOpencodeToken(token string) error {
	path := opencodeAuthPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	creds := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &creds) // best-effort; overwrite on parse failure
	}
	creds[Provider] = map[string]any{
		"type":    "oauth",
		"access":  token,
		"refresh": token,
		"expires": 0,
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	// Write atomically: temp file in the same dir, then rename.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func firstNonEmptyLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			return ln
		}
	}
	return strings.TrimSpace(s)
}

func resetLoginLocked() {
	login.cancel = nil
	login.running = false
	login.verifyURL = ""
	login.userCode = ""
	login.output.Reset()
	login.done = false
	login.success = false
	login.err = ""
	login.startedAt = time.Time{}
}

func snapshotLogin() LoginInfo {
	login.mu.Lock()
	defer login.mu.Unlock()
	return snapshotLoginLocked()
}

func snapshotLoginLocked() LoginInfo {
	return LoginInfo{
		Running:   login.running && !login.done,
		Done:      login.done,
		Success:   login.success,
		VerifyURL: login.verifyURL,
		UserCode:  login.userCode,
		Output:    login.output.String(),
		Error:     login.err,
		StartedAt: timeString(login.startedAt),
	}
}

func timeString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
