// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package vertexauth manages "Sign in with Google Cloud" for opencode's
// google-vertex provider (Anthropic Claude served through Google Vertex AI).
//
// Unlike the GitHub Copilot sign-in (internal/opencodeauth), there is no
// opencode-native login for Vertex: authentication is Google Cloud Application
// Default Credentials (ADC). The one command a user would otherwise run in a
// terminal is:
//
//	gcloud auth application-default login
//
// The product goal is to hide that terminal step, so we drive gcloud ourselves
// over stdin/stdout pipes (no shell, no PTY): we spawn
//
//	gcloud auth application-default login --no-launch-browser
//
// which prints a long accounts.google.com URL and then blocks on
// "enter the verification code". We scrape the URL, surface it to the web UI,
// let the user authorize in their browser, and forward the verification code
// they paste back into the running gcloud's stdin. gcloud then writes ADC to
// ~/.config/gcloud/application_default_credentials.json.
//
// opencode's google-vertex provider reads ADC plus GOOGLE_VERTEX_PROJECT /
// GOOGLE_VERTEX_LOCATION. EnsureEnv() seeds those into the server process
// environment (from the image's ANTHROPIC_VERTEX_PROJECT_ID / CLOUD_ML_REGION;
// the location falls back to "global", the project has no default) so every
// child opencode process — which inherits os.Environ() — sees them.
//
// All process execution uses exec.Command with an argument vector (never a
// shell), consistent with the rest of the codebase.
package vertexauth

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
)

// Provider is the opencode provider id for Anthropic-via-Google-Vertex.
const Provider = "google-vertex"

// Default Vertex region, used when neither GOOGLE_VERTEX_LOCATION nor
// CLOUD_ML_REGION is set. There is no default project: it's the customer's own.
const defaultLocation = "global"

// GcloudBin lets tests substitute a fake gcloud binary. When empty the resolved
// $KWA_GCLOUD_BIN (or "gcloud" on PATH) is used.
var GcloudBin = ""

// commandTimeout bounds the short-lived status calls (print-access-token).
var commandTimeout = 20 * time.Second

// loginTimeout bounds the interactive login flow end-to-end.
var loginTimeout = 15 * time.Minute

func resolveBin() string {
	if GcloudBin != "" {
		return GcloudBin
	}
	if v := strings.TrimSpace(env.Get("KWA_GCLOUD_BIN")); v != "" {
		return v
	}
	return "gcloud"
}

// Project returns the Vertex project id, preferring GOOGLE_VERTEX_PROJECT, then
// the Claude-Code-style ANTHROPIC_VERTEX_PROJECT_ID. It's empty when neither is
// set, and the reachability check then reports that no project is configured.
func Project() string {
	if v := strings.TrimSpace(os.Getenv("GOOGLE_VERTEX_PROJECT")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("ANTHROPIC_VERTEX_PROJECT_ID"))
}

// Location returns the Vertex region, preferring GOOGLE_VERTEX_LOCATION, then
// the Claude-Code-style CLOUD_ML_REGION, then the default ("global").
func Location() string {
	if v := strings.TrimSpace(os.Getenv("GOOGLE_VERTEX_LOCATION")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("CLOUD_ML_REGION")); v != "" {
		return v
	}
	return defaultLocation
}

// EnsureEnv seeds GOOGLE_VERTEX_PROJECT / GOOGLE_VERTEX_LOCATION into the
// current process environment (if unset) so child opencode processes, which
// inherit os.Environ(), light up the google-vertex provider. Safe to call more
// than once. Returns the effective project and location.
func EnsureEnv() (project, location string) {
	project, location = Project(), Location()
	if project != "" && os.Getenv("GOOGLE_VERTEX_PROJECT") == "" {
		_ = os.Setenv("GOOGLE_VERTEX_PROJECT", project)
	}
	if os.Getenv("GOOGLE_VERTEX_LOCATION") == "" {
		_ = os.Setenv("GOOGLE_VERTEX_LOCATION", location)
	}
	log.Printf("[vertexauth] vertex env ready (project=%s, location=%s)", project, location)
	return project, location
}

// Status is the response payload of GET /api/vertex/auth/status.
type Status struct {
	Authenticated bool   `json:"authenticated"`
	Provider      string `json:"provider"`
	Project       string `json:"project,omitempty"`
	Location      string `json:"location,omitempty"`
}

// CheckStatus reports whether the user has completed the Google Cloud ADC login
// flow, by looking for the credentials file it writes. Without that file gcloud
// could only find a GCE metadata server, and asking it takes about 12 seconds.
func CheckStatus() Status {
	return Status{Provider: Provider, Project: Project(), Location: Location(), Authenticated: adcCredentialsOnDisk()}
}

// adcCredentialsPath returns the path to the Application Default Credentials
// file gcloud writes after `gcloud auth application-default login`, honouring
// Google's standard overrides in precedence order:
//
//  1. $GOOGLE_APPLICATION_CREDENTIALS (explicit credentials file)
//  2. $CLOUDSDK_CONFIG/application_default_credentials.json (custom gcloud config dir)
//  3. ~/.config/gcloud/application_default_credentials.json (default)
//
// It returns "" only when the home directory cannot be resolved and no override
// is set.
func adcCredentialsPath() string {
	if p := strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")); p != "" {
		return p
	}
	const adcFile = "application_default_credentials.json"
	if cfg := strings.TrimSpace(os.Getenv("CLOUDSDK_CONFIG")); cfg != "" {
		return filepath.Join(cfg, adcFile)
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".config", "gcloud", adcFile)
}

// adcCredentialsOnDisk reports whether a non-empty ADC credentials file exists
// at the resolved path — i.e. the user has already completed the gcloud login
// flow on this machine.
func adcCredentialsOnDisk() bool {
	p := adcCredentialsPath()
	if p == "" {
		return false
	}
	info, err := os.Stat(p)
	return err == nil && !info.IsDir() && info.Size() > 0
}

// HandleStatus answers GET /api/vertex/auth/status.
func HandleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, CheckStatus())
}

// ── Login flow (gcloud ADC, driven over stdin/stdout pipes) ──

// authURLRe matches the Google OAuth consent URL gcloud prints.
var authURLRe = regexp.MustCompile(`https://accounts\.google\.com/o/oauth2/auth\?\S+`)

// promptRe matches gcloud's "enter the verification code" prompt so we know it
// is now blocked waiting on stdin.
var promptRe = regexp.MustCompile(`(?i)enter the verification code`)

// LoginInfo is the response payload of the login endpoints. Field names mirror
// opencodeauth.LoginInfo so the frontend can share a type. For Vertex the code
// is entered by the user (there is no user_code we hand them), so the modal
// shows verify_url and collects a verification code, which is POSTed to
// /api/vertex/auth/login/code.
type LoginInfo struct {
	Running      bool   `json:"running"`
	Done         bool   `json:"done"`
	Success      bool   `json:"success"`
	VerifyURL    string `json:"verify_url,omitempty"`
	AwaitingCode bool   `json:"awaiting_code"`
	Output       string `json:"output,omitempty"`
	Error        string `json:"error,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
}

type loginState struct {
	mu           sync.Mutex
	cancel       context.CancelFunc
	stdin        io.WriteCloser
	running      bool
	verifyURL    string
	awaitingCode bool
	codeSent     bool
	output       strings.Builder
	done         bool
	success      bool
	err          string
	startedAt    time.Time
}

var login = &loginState{}

// onLoginSuccess, if set, is invoked (in a goroutine) after a login flow
// completes successfully. main wires this to refresh the opencode model cache.
var onLoginSuccess func()

// SetOnLoginSuccess registers a callback fired after a successful login.
func SetOnLoginSuccess(fn func()) { onLoginSuccess = fn }

// HandleLoginStart answers POST /api/vertex/auth/login/start.
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

// HandleLoginInfo answers GET /api/vertex/auth/login/info — used by the UI to poll.
func HandleLoginInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, snapshotLogin())
}

// HandleLoginCode answers POST /api/vertex/auth/login/code with a JSON body
// {"code":"..."} — the verification code the user copied from their browser. It
// is written to the running gcloud's stdin to complete the flow.
func HandleLoginCode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := submitCode(body.Code); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, snapshotLogin())
}

// HandleLoginCancel answers POST /api/vertex/auth/login/cancel.
func HandleLoginCancel(w http.ResponseWriter, _ *http.Request) {
	login.mu.Lock()
	if login.cancel != nil && !login.done {
		login.cancel()
		login.err = "cancelled by user"
		login.done = true
		login.running = false
		login.awaitingCode = false
	}
	info := snapshotLoginLocked()
	login.mu.Unlock()
	writeJSON(w, http.StatusOK, info)
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

	ctx, cancel := context.WithTimeout(context.Background(), loginTimeout)
	cmd := exec.CommandContext(ctx, resolveBin(),
		"auth", "application-default", "login", "--no-launch-browser", "--quiet")

	// gcloud prints the URL and prompt to stderr and reads the code from stdin.
	// Combine stdout+stderr into one pipe we scan line by line.
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		_ = pw.Close()
		finishLogin(false, "could not open gcloud stdin: "+err.Error())
		return nil
	}

	if err := cmd.Start(); err != nil {
		cancel()
		_ = pw.Close()
		finishLogin(false, "could not start Google Cloud sign-in: "+err.Error())
		return nil
	}

	login.mu.Lock()
	login.cancel = cancel
	login.stdin = stdin
	login.output.WriteString("Starting Google Cloud sign-in…\n")
	login.mu.Unlock()

	log.Printf("[vertexauth] gcloud application-default login started; awaiting consent URL")
	go scanOutput(pr)
	go waitLogin(cmd, cancel, pw)
	return nil
}

// scanOutput reads gcloud's combined output, records it, scrapes the consent URL
// and detects the verification-code prompt.
func scanOutput(pr *io.PipeReader) {
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		login.mu.Lock()
		login.output.WriteString(line + "\n")
		if login.verifyURL == "" {
			if m := authURLRe.FindString(line); m != "" {
				login.verifyURL = m
				// gcloud prints the consent URL and then immediately blocks on
				// the "enter the verification code" prompt — which has no
				// trailing newline, so the scanner never emits it as a line.
				// Treat "URL shown" as "awaiting code".
				login.awaitingCode = true
				log.Printf("[vertexauth] consent URL surfaced to UI")
			}
		}
		if promptRe.MatchString(line) {
			login.awaitingCode = true
		}
		login.mu.Unlock()
	}
	_ = pr.Close()
}

// waitLogin waits for gcloud to exit, then records the outcome. Success is a
// zero exit code corroborated by a subsequent ADC check.
func waitLogin(cmd *exec.Cmd, cancel context.CancelFunc, pw *io.PipeWriter) {
	err := cmd.Wait()
	_ = pw.Close()
	cancel()

	login.mu.Lock()
	cancelled := login.err == "cancelled by user"
	login.mu.Unlock()
	if cancelled {
		finishLogin(false, "")
		return
	}
	if err != nil {
		finishLogin(false, gcloudError(err))
		return
	}
	// Corroborate: gcloud exited 0 — confirm ADC is actually usable.
	if !CheckStatus().Authenticated {
		finishLogin(false, "sign-in finished but no valid credentials were written. Please retry.")
		return
	}
	login.mu.Lock()
	login.output.WriteString("Credentials saved. Vertex AI is ready.\n")
	login.err = ""
	login.mu.Unlock()
	finishLogin(true, "")
}

// submitCode forwards the user's verification code to gcloud's stdin.
func submitCode(code string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return fmt.Errorf("verification code is empty")
	}
	login.mu.Lock()
	defer login.mu.Unlock()
	if login.done || !login.running {
		return fmt.Errorf("no sign-in is in progress")
	}
	if login.stdin == nil {
		return fmt.Errorf("sign-in is not ready to accept a code yet")
	}
	if login.codeSent {
		return fmt.Errorf("a verification code was already submitted")
	}
	if _, err := io.WriteString(login.stdin, code+"\n"); err != nil {
		return fmt.Errorf("could not send the code to gcloud: %w", err)
	}
	login.codeSent = true
	login.awaitingCode = false
	login.output.WriteString("Verification code submitted. Finishing…\n")
	return nil
}

func finishLogin(success bool, errMsg string) {
	login.mu.Lock()
	login.running = false
	login.done = true
	login.success = success
	login.awaitingCode = false
	if login.err == "" {
		login.err = errMsg
	}
	login.mu.Unlock()
	if success {
		log.Printf("[vertexauth] sign-in completed: credentials valid")
		if onLoginSuccess != nil {
			go onLoginSuccess()
		}
	} else {
		log.Printf("[vertexauth] sign-in ended without success: %s", errMsg)
	}
}

func gcloudError(err error) string {
	if ee, ok := err.(*exec.ExitError); ok {
		_ = ee
		return "Google Cloud sign-in failed. Check the CLI output and retry."
	}
	return "Google Cloud sign-in failed: " + err.Error()
}

func resetLoginLocked() {
	login.cancel = nil
	login.stdin = nil
	login.running = false
	login.verifyURL = ""
	login.awaitingCode = false
	login.codeSent = false
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
		Running:      login.running && !login.done,
		Done:         login.done,
		Success:      login.success,
		VerifyURL:    login.verifyURL,
		AwaitingCode: login.awaitingCode && !login.codeSent,
		Output:       login.output.String(),
		Error:        login.err,
		StartedAt:    timeString(login.startedAt),
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
