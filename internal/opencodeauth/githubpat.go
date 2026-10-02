// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeauth

// GitHub MCP token resolution.
//
// The hosted GitHub MCP server (https://api.githubcopilot.com/mcp) is
// authenticated with a bearer token. Historically we reused the github-copilot
// *login* token for this, but that token only carries the `read:user` scope
// (it exists to authenticate opencode's LLM provider, not to act on repos), so
// every PR/issue/repo MCP call fails with "insufficient scopes".
//
// This file decouples the MCP bearer from the login token: it prefers an
// explicit, properly-scoped GitHub token — from the environment or a
// user-supplied Personal Access Token stored on disk — and only falls back to
// the login token when nothing better exists. The login/LLM flow is unchanged.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// TokenSource identifies where the GitHub MCP bearer token came from.
type TokenSource string

const (
	TokenSourceEnv    TokenSource = "env"    // GITHUB_TOKEN / GH_TOKEN / COPILOT_GITHUB_TOKEN
	TokenSourceStored TokenSource = "stored" // user-supplied PAT persisted on disk
	TokenSourceNone   TokenSource = "none"   // no token available
)

// There is deliberately no "login" source. The github-copilot device-flow token
// carries only read:user; it connects to the MCP endpoint and lists every tool,
// then fails with "insufficient scopes" on any repository call — a source that
// looks healthy everywhere except where it matters. See GitHubMCPToken.

// githubTokenEnvVars are consulted, in order, for an explicit GitHub token.
var githubTokenEnvVars = []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"}

// onGitHubTokenChange, when set, is invoked after the stored PAT changes so the
// caller can re-run mcp.EnsureGitHub with the freshly resolved token. Using a
// callback avoids an opencodeauth→mcp import dependency.
//
// Guarded: it is registered from the startup goroutine and read from HTTP
// handler goroutines, so an unsynchronized package var is a genuine race.
var (
	tokenChangeMu       sync.RWMutex
	onGitHubTokenChange func()
)

// SetOnGitHubTokenChange registers a callback fired after the stored GitHub PAT
// is set or cleared.
func SetOnGitHubTokenChange(fn func()) {
	tokenChangeMu.Lock()
	defer tokenChangeMu.Unlock()
	onGitHubTokenChange = fn
}

// fireGitHubTokenChange invokes the registered hook, if any.
func fireGitHubTokenChange() {
	tokenChangeMu.RLock()
	fn := onGitHubTokenChange
	tokenChangeMu.RUnlock()
	if fn != nil {
		fn()
	}
}

// githubPATPath is where the user-supplied PAT is persisted (mode 0600).
func githubPATPath() string { return layout.GitHubToken() }

type storedPAT struct {
	Token string `json:"token"`
}

// LoadGitHubPAT returns the persisted PAT, or "" if none is stored.
func LoadGitHubPAT() string {
	raw, err := os.ReadFile(githubPATPath())
	if err != nil || len(raw) == 0 {
		return ""
	}
	var p storedPAT
	if err := json.Unmarshal(raw, &p); err != nil {
		return ""
	}
	return strings.TrimSpace(p.Token)
}

// StoreGitHubPAT persists the PAT with 0600 perms and fires the change hook.
func StoreGitHubPAT(token string) error {
	token = strings.TrimSpace(token)
	path := githubPATPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(storedPAT{Token: token})
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	fireGitHubTokenChange()
	return nil
}

// ClearGitHubPAT removes any persisted PAT and fires the change hook.
func ClearGitHubPAT() error {
	err := os.Remove(githubPATPath())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	fireGitHubTokenChange()
	return nil
}

// envGitHubToken returns the first non-empty explicit token from the env.
func envGitHubToken() string {
	for _, k := range githubTokenEnvVars {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// GitHubMCPToken resolves the token used to authenticate the GitHub MCP server,
// returning the token and where it came from. Preference order:
//  1. explicit env var (a PAT the operator injected),
//  2. a user-supplied PAT stored on disk.
//
// The github-copilot login token is deliberately NOT a fallback. It carries
// only `read:user`, and measured against the live endpoint it is worse than no
// token at all: it completes `initialize`, returns all 47 tools, and makes
// `opencode mcp list` report "connected" — so every signal the UI reads says
// the server is working — while every repository call fails with
// `403 Forbidden: insufficient scopes`. Accepting it produced a green
// "Signed in" badge on a server the agent could not use, which is a worse
// failure than an obviously unconfigured one.
func GitHubMCPToken() (string, TokenSource) {
	if v := envGitHubToken(); v != "" {
		return v, TokenSourceEnv
	}
	if v := LoadGitHubPAT(); v != "" {
		return v, TokenSourceStored
	}
	return "", TokenSourceNone
}

// githubAPIBase is the GitHub REST base, overridable for tests.
var githubAPIBase = "https://api.github.com"

// TokenCheck is the result of validating a GitHub token against the API.
type TokenCheck struct {
	Valid      bool     `json:"valid"`
	Login      string   `json:"login,omitempty"`
	Scopes     []string `json:"scopes"`
	CanReadPRs bool     `json:"can_read_prs"`
	Error      string   `json:"error,omitempty"`
}

// prCapableScopes are the classic OAuth scopes that grant PR read access.
var prCapableScopes = []string{"repo", "public_repo"}

// ValidateGitHubToken checks a token against the GitHub API and reports the
// granted scopes and whether they include PR read capability. Best-effort:
// network/timeout errors surface in Error with Valid=false. Fine-grained PATs
// report no classic scopes; in that case CanReadPRs is left true when the token
// authenticates (their permissions are not visible via x-oauth-scopes).
func ValidateGitHubToken(ctx context.Context, token string) TokenCheck {
	token = strings.TrimSpace(token)
	if token == "" {
		return TokenCheck{Scopes: []string{}, Error: "empty token"}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIBase+"/user", nil)
	if err != nil {
		return TokenCheck{Scopes: []string{}, Error: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return TokenCheck{Scopes: []string{}, Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return TokenCheck{Scopes: []string{}, Error: "GitHub returned " + resp.Status}
	}
	var body struct {
		Login string `json:"login"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)

	scopes := []string{}
	rawScopes := strings.TrimSpace(resp.Header.Get("X-OAuth-Scopes"))
	fineGrained := rawScopes == "" // fine-grained PATs return no X-OAuth-Scopes
	if rawScopes != "" {
		for _, s := range strings.Split(rawScopes, ",") {
			if s = strings.TrimSpace(s); s != "" {
				scopes = append(scopes, s)
			}
		}
	}
	canPR := fineGrained
	for _, s := range scopes {
		for _, want := range prCapableScopes {
			if s == want {
				canPR = true
			}
		}
	}
	return TokenCheck{Valid: true, Login: body.Login, Scopes: scopes, CanReadPRs: canPR}
}

// ── HTTP handlers ─────────────────────────────────────────────────────────

// GitHubPATStatus is the payload of GET /api/github/pat/status.
type GitHubPATStatus struct {
	Source     TokenSource `json:"source"`     // where the active MCP token comes from
	HasStored  bool        `json:"has_stored"` // a PAT is persisted on disk
	Login      string      `json:"login,omitempty"`
	Scopes     []string    `json:"scopes"`
	CanReadPRs bool        `json:"can_read_prs"`
	Error      string      `json:"error,omitempty"`
}

var statusMu sync.Mutex

// HandleGitHubPATStatus reports the active token source and, if a token exists,
// its validated scopes/PR capability.
func HandleGitHubPATStatus(w http.ResponseWriter, r *http.Request) {
	statusMu.Lock()
	defer statusMu.Unlock()

	token, source := GitHubMCPToken()
	out := GitHubPATStatus{Source: source, HasStored: LoadGitHubPAT() != "", Scopes: []string{}}
	if token != "" {
		chk := ValidateGitHubToken(r.Context(), token)
		out.Login = chk.Login
		out.Scopes = chk.Scopes
		out.CanReadPRs = chk.CanReadPRs
		if chk.Error != "" {
			out.Error = chk.Error
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// HandleSetGitHubPAT validates and stores a user-supplied PAT.
func HandleSetGitHubPAT(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	token := strings.TrimSpace(body.Token)
	if token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "token is required"})
		return
	}
	chk := ValidateGitHubToken(r.Context(), token)
	if !chk.Valid {
		msg := chk.Error
		if msg == "" {
			msg = "token could not be validated"
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": msg, "check": chk})
		return
	}
	if err := StoreGitHubPAT(token); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "check": chk})
}

// HandleClearGitHubPAT removes the stored PAT (reverting to env/login token).
func HandleClearGitHubPAT(w http.ResponseWriter, _ *http.Request) {
	if err := ClearGitHubPAT(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	_, source := GitHubMCPToken()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "source": source})
}
