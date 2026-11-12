// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodev2"
)

// ChatGPTProvider is OpenCode's integration and model provider id for ChatGPT.
const ChatGPTProvider = "openai"

// chatGPTMethod is opencode 2.x's ChatGPT sign-in: a browser authorization
// whose callback goes to a listener opencode opens on a random loopback port.
// opencode 2.2.5 lists only this method; its device-code method is legacy and
// fails to start.
const chatGPTMethod = "chatgpt-token-sharing"

var (
	chatGPTAttemptPattern = regexp.MustCompile(`^con_[A-Za-z0-9_-]{1,96}$`)

	// Indirected so handler tests never need to start the real OpenCode binary.
	chatGPTOpenCodeEnabled    = opencodev2.Enabled
	chatGPTHasOAuthConnection = opencodev2.HasOAuthConnection
	chatGPTIntegration        = opencodev2.OAuthIntegration
	chatGPTStartOAuth         = opencodev2.StartOAuth
	chatGPTOAuthStatus        = opencodev2.GetOAuthStatus
	chatGPTCancelOAuth        = opencodev2.CancelOAuth
	chatGPTRemoveCredentials  = opencodev2.RemoveCredentials
)

var chatGPTLogin struct {
	sync.Mutex
	info     ChatGPTLoginInfo
	notified bool
}

var chatGPTLoginSuccess struct {
	sync.RWMutex
	fn func()
}

// SetOnChatGPTLoginSuccess registers a callback for the ChatGPT flow only.
// The server uses it to invalidate and recheck ChatGPT reachability after
// OpenCode stores the new credential.
func SetOnChatGPTLoginSuccess(fn func()) {
	chatGPTLoginSuccess.Lock()
	chatGPTLoginSuccess.fn = fn
	chatGPTLoginSuccess.Unlock()
}

func fireChatGPTLoginSuccess() {
	chatGPTLoginSuccess.RLock()
	fn := chatGPTLoginSuccess.fn
	chatGPTLoginSuccess.RUnlock()
	if fn != nil {
		go fn()
	}
}

// ChatGPTLoginInfo is the browser handoff for opencode's ChatGPT sign-in: the
// authorization page to open, never a token. The callback address stays on the
// server, which uses it to check the URL the user pastes back.
type ChatGPTLoginInfo struct {
	AttemptID string   `json:"attempt_id"`
	VerifyURL string   `json:"verify_url"`
	callback  *url.URL // opencode's loopback listener (redirect_uri)
	state     string   // the authorization request's state
}

type ChatGPTLoginStatus struct {
	Status string `json:"status"`
}

// ChatGPT sign-in is built on opencode 2.x's integration API, which 1.x does
// not have, so on 1.x the provider is not offered at all (main.go registers it
// only on 2.x) and these functions report it as signed out.

// ChatGPTAuthenticated reports whether opencode 2.x has a working OAuth
// connection for OpenAI. It reads the integration's connection summary, which
// carries no credential value.
func ChatGPTAuthenticated() bool {
	if !chatGPTOpenCodeEnabled() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	ok, err := chatGPTHasOAuthConnection(ctx, ChatGPTProvider, config.Workspace)
	return err == nil && ok
}

// SignOutChatGPT cancels a sign-in in progress and removes OpenAI's saved
// credential from opencode's credential store. It does not touch other
// providers.
func SignOutChatGPT() error {
	chatGPTLogin.Lock()
	attemptID := chatGPTLogin.info.AttemptID
	chatGPTLogin.info = ChatGPTLoginInfo{}
	chatGPTLogin.notified = false
	chatGPTLogin.Unlock()

	if !chatGPTOpenCodeEnabled() {
		return errors.New("ChatGPT sign-in needs opencode 2.x")
	}
	if attemptID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		_ = chatGPTCancelOAuth(ctx, ChatGPTProvider, attemptID, config.Workspace)
		cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	err := chatGPTRemoveCredentials(ctx, ChatGPTProvider)
	cancel()
	if err != nil {
		// Do not propagate OpenCode's response body through the generic sign-out
		// handler. It is not needed to fix the connection and may be sensitive.
		return errors.New("OpenCode could not remove the ChatGPT credential")
	}
	return nil
}

// HandleChatGPTLoginStart begins opencode's ChatGPT OAuth flow. The browser
// receives only the authorization URL; tokens stay inside opencode and its
// credential store.
func HandleChatGPTLoginStart(w http.ResponseWriter, r *http.Request) {
	if !chatGPTOpenCodeEnabled() {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "ChatGPT sign-in needs opencode 2.x.",
		})
		return
	}

	chatGPTLogin.Lock()
	defer chatGPTLogin.Unlock()
	if chatGPTLogin.info.AttemptID != "" {
		ctx, cancel := context.WithTimeout(r.Context(), commandTimeout)
		status, err := chatGPTOAuthStatus(ctx, ChatGPTProvider, chatGPTLogin.info.AttemptID, config.Workspace)
		cancel()
		if err == nil && status.Status == "pending" {
			writeJSON(w, http.StatusOK, chatGPTLogin.info)
			return
		}
		chatGPTLogin.info = ChatGPTLoginInfo{}
		chatGPTLogin.notified = false
	}

	ctx, cancel := context.WithTimeout(r.Context(), commandTimeout)
	defer cancel()
	methods, _, err := chatGPTIntegration(ctx, ChatGPTProvider, config.Workspace)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "OpenCode could not start ChatGPT sign-in."})
		return
	}
	found := false
	for _, method := range methods {
		if method.ID == chatGPTMethod && method.Type == "oauth" {
			found = true
			break
		}
	}
	if !found {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "This opencode version has no ChatGPT sign-in method.",
		})
		return
	}

	attempt, err := chatGPTStartOAuth(ctx, ChatGPTProvider, chatGPTMethod, config.Workspace)
	if err != nil {
		// OpenCode's error body is intentionally not returned or logged. It may
		// contain provider-controlled authentication details.
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "OpenCode could not start ChatGPT sign-in."})
		return
	}
	info, err := chatGPTBrowserInfo(attempt)
	if err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		_ = chatGPTCancelOAuth(ctx, ChatGPTProvider, attempt.AttemptID, config.Workspace)
		cancel()
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "OpenCode returned an unsupported ChatGPT sign-in response."})
		return
	}
	chatGPTLogin.info = info
	chatGPTLogin.notified = false
	writeJSON(w, http.StatusOK, info)
}

// HandleChatGPTLoginStatus polls the OAuth attempt this server started and intentionally
// omits OpenCode's failure message, which is not needed by the UI.
func HandleChatGPTLoginStatus(w http.ResponseWriter, r *http.Request) {
	attemptID := r.PathValue("attemptID")
	if !chatGPTAttemptPattern.MatchString(attemptID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid sign-in attempt"})
		return
	}

	chatGPTLogin.Lock()
	if chatGPTLogin.info.AttemptID != attemptID {
		chatGPTLogin.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "sign-in attempt not found"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), commandTimeout)
	status, err := chatGPTOAuthStatus(ctx, ChatGPTProvider, attemptID, config.Workspace)
	cancel()
	if err != nil {
		chatGPTLogin.Unlock()
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "OpenCode sign-in status is unavailable."})
		return
	}
	if status.Status != "pending" && status.Status != "complete" && status.Status != "failed" && status.Status != "expired" {
		status.Status = "failed"
	}
	notify := status.Status == "complete" && !chatGPTLogin.notified
	if notify {
		chatGPTLogin.notified = true
	}
	chatGPTLogin.Unlock()
	if notify {
		fireLoginSuccess()
		fireChatGPTLoginSuccess()
	}
	writeJSON(w, http.StatusOK, ChatGPTLoginStatus{Status: status.Status})
}

// HandleChatGPTLoginCancel cancels an in-flight OpenCode OAuth attempt without
// exposing any OpenCode response body.
func HandleChatGPTLoginCancel(w http.ResponseWriter, r *http.Request) {
	attemptID := r.PathValue("attemptID")
	if !chatGPTAttemptPattern.MatchString(attemptID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid sign-in attempt"})
		return
	}

	chatGPTLogin.Lock()
	if chatGPTLogin.info.AttemptID != attemptID {
		chatGPTLogin.Unlock()
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), commandTimeout)
	err := chatGPTCancelOAuth(ctx, ChatGPTProvider, attemptID, config.Workspace)
	cancel()
	chatGPTLogin.info = ChatGPTLoginInfo{}
	chatGPTLogin.notified = false
	chatGPTLogin.Unlock()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "OpenCode could not cancel ChatGPT sign-in."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func chatGPTBrowserInfo(attempt opencodev2.OAuthAttempt) (ChatGPTLoginInfo, error) {
	if !chatGPTAttemptPattern.MatchString(attempt.AttemptID) || attempt.Mode != "auto" {
		return ChatGPTLoginInfo{}, errors.New("unsupported OAuth attempt")
	}
	authURL, err := url.Parse(attempt.URL)
	if err != nil || authURL.Scheme != "https" || authURL.Host != "auth.openai.com" || authURL.Path != "/api/accounts/authorize" || authURL.User != nil || authURL.Fragment != "" {
		return ChatGPTLoginInfo{}, errors.New("unsupported authorization URL")
	}
	q := authURL.Query()
	callback, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || callback.Scheme != "http" || !isLoopback(callback.Hostname()) || callback.Port() == "" || callback.Path != "/auth/callback" || callback.User != nil || callback.RawQuery != "" {
		return ChatGPTLoginInfo{}, errors.New("unsupported callback address")
	}
	if q.Get("state") == "" {
		return ChatGPTLoginInfo{}, errors.New("authorization URL has no state")
	}
	return ChatGPTLoginInfo{AttemptID: attempt.AttemptID, VerifyURL: authURL.String(), callback: callback, state: q.Get("state")}, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// chatGPTCallbackClient delivers the pasted callback. Indirected for tests.
var chatGPTCallbackClient = &http.Client{
	Timeout: 15 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// HandleChatGPTLoginCallback — POST /api/openai/auth/login/{attemptID}/callback,
// body {"url": "<pasted callback URL>"}. After approving, the user's browser is
// sent to opencode's listener on the workspace's loopback address, which it
// can't reach, so the user pastes that address here and the server delivers it.
//
// Only the running attempt's own callback is accepted: same port and path, the
// state its authorization asked for, and a code. The request goes to the
// address opencode named, never one built from the pasted host, so this can't
// reach anything else.
func HandleChatGPTLoginCallback(w http.ResponseWriter, r *http.Request) {
	attemptID := r.PathValue("attemptID")
	if !chatGPTAttemptPattern.MatchString(attemptID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid sign-in attempt"})
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Paste the address your browser showed after you approved."})
		return
	}

	chatGPTLogin.Lock()
	info := chatGPTLogin.info
	chatGPTLogin.Unlock()
	if info.AttemptID != attemptID || info.callback == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "sign-in attempt not found"})
		return
	}

	pasted, err := url.Parse(strings.TrimSpace(body.URL))
	if err != nil || (pasted.Scheme != "http" && pasted.Scheme != "https") || !isLoopback(pasted.Hostname()) ||
		pasted.Port() != info.callback.Port() || pasted.Path != info.callback.Path {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "That isn't the address this sign-in is waiting for. Paste the whole address from your browser's address bar."})
		return
	}
	q := pasted.Query()
	if q.Get("state") != info.state {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "That address belongs to a different sign-in. Start again."})
		return
	}
	if q.Get("code") == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "That address has no authorization code. Approve the sign-in first, then paste the address you land on."})
		return
	}

	target := *info.callback
	target.RawQuery = pasted.RawQuery
	resp, err := chatGPTCallbackClient.Get(target.String())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "The sign-in is no longer waiting. Start again."})
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		// opencode's response is not passed on: it may describe the token exchange.
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "ChatGPT didn't accept the sign-in. Start again."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
