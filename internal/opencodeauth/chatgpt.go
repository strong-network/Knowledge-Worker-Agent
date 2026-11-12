// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeauth

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodev2"
)

// ChatGPTProvider is OpenCode's integration and model provider id for ChatGPT.
const ChatGPTProvider = "openai"

const chatGPTDeviceMethod = "chatgpt-headless"

var (
	chatGPTAttemptPattern = regexp.MustCompile(`^con_[A-Za-z0-9_-]{1,96}$`)
	chatGPTCodePattern    = regexp.MustCompile(`^[A-Za-z0-9-]{4,32}$`)

	// Indirected so handler tests never need to start the real OpenCode binary.
	chatGPTIsOpenCodeV2       = opencodev2.Enabled
	chatGPTOpenCodeEnabled    = opencodev2.Enabled
	chatGPTHasOAuthConnection = opencodev2.HasOAuthConnection
	chatGPTAuthList           = opencodeAuthList
	chatGPTAuthLogout         = opencodeAuthLogout
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

// ChatGPTLoginInfo is the browser handoff for OpenCode's native device-code
// flow. It contains a short-lived verification code, never an access token.
type ChatGPTLoginInfo struct {
	AttemptID string `json:"attempt_id"`
	VerifyURL string `json:"verify_url"`
	UserCode  string `json:"user_code"`
}

type ChatGPTLoginStatus struct {
	Status string `json:"status"`
}

// ChatGPTAuthenticated reports whether OpenCode has an OAuth connection for
// OpenAI. On 2.x it reads the public integration connection summary; on 1.x it
// parses the metadata-only output of `opencode auth list`.
func ChatGPTAuthenticated() bool {
	if !chatGPTIsOpenCodeV2() {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		out, err := chatGPTAuthList(ctx)
		return err == nil && authListHasOAuth(out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	ok, err := chatGPTHasOAuthConnection(ctx, ChatGPTProvider, config.Workspace)
	return err == nil && ok
}

// SignOutChatGPT removes OpenAI's saved credential through the same OpenCode
// credential store used by the CLI/TUI. It does not touch other providers.
func SignOutChatGPT() error {
	chatGPTLogin.Lock()
	attemptID := chatGPTLogin.info.AttemptID
	chatGPTLogin.info = ChatGPTLoginInfo{}
	chatGPTLogin.notified = false
	chatGPTLogin.Unlock()

	if attemptID != "" && chatGPTIsOpenCodeV2() {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		_ = opencodev2.CancelOAuth(ctx, ChatGPTProvider, attemptID, config.Workspace)
		cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	var err error
	if chatGPTIsOpenCodeV2() {
		err = chatGPTRemoveCredentials(ctx, ChatGPTProvider)
	} else {
		err = chatGPTAuthLogout(ctx, ChatGPTProvider)
	}
	cancel()
	if err != nil {
		// Do not propagate OpenCode's response body through the generic sign-out
		// handler. It is not needed to fix the connection and may be sensitive.
		return errors.New("OpenCode could not remove the ChatGPT credential")
	}
	return nil
}

// opencodeAuthList invokes OpenCode 1.x's metadata-only auth list command.
// That command prints provider name and credential type, never credential
// values. Its output is parsed in memory and is never logged or returned.
func opencodeAuthList(ctx context.Context) (string, error) {
	if config.OpencodeBin == "" {
		return "", errors.New("OpenCode binary is unavailable")
	}
	output, err := exec.CommandContext(ctx, config.OpencodeBin, "auth", "list").Output()
	return string(output), err
}

// opencodeAuthLogout asks OpenCode 1.x to remove one provider's credential.
// OpenCode handles its own auth store without KWA reading or copying tokens.
func opencodeAuthLogout(ctx context.Context, provider string) error {
	if config.OpencodeBin == "" {
		return errors.New("OpenCode binary is unavailable")
	}
	return exec.CommandContext(ctx, config.OpencodeBin, "auth", "logout", provider).Run()
}

var authListANSI = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

func authListHasOAuth(output string) bool {
	output = authListANSI.ReplaceAllString(output, "")
	for _, line := range strings.Split(output, "\n") {
		line = strings.Trim(line, " \t│┌└┘┐─")
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		provider, kind := fields[0], fields[len(fields)-1]
		if strings.EqualFold(provider, ChatGPTProvider) && strings.EqualFold(kind, "oauth") {
			return true
		}
	}
	return false
}

// HandleChatGPTLoginStart begins OpenCode's headless ChatGPT OAuth flow. The
// browser receives only the verification URL and one-time device code; OAuth
// tokens remain inside OpenCode and its credential store.
func HandleChatGPTLoginStart(w http.ResponseWriter, r *http.Request) {
	if !chatGPTOpenCodeEnabled() {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "ChatGPT web sign-in needs OpenCode 2.x. Use OpenCode's terminal sign-in as a fallback.",
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
		if method.ID == chatGPTDeviceMethod && method.Type == "oauth" {
			found = true
			break
		}
	}
	if !found {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "This OpenCode version has no ChatGPT web sign-in method. Use OpenCode's terminal sign-in as a fallback.",
		})
		return
	}

	attempt, err := chatGPTStartOAuth(ctx, ChatGPTProvider, chatGPTDeviceMethod, config.Workspace)
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

// HandleChatGPTLoginStatus polls one KWA-owned OAuth attempt and intentionally
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
	verifyURL, err := url.Parse(attempt.URL)
	if err != nil || verifyURL.Scheme != "https" || verifyURL.Host != "auth.openai.com" || verifyURL.Path != "/codex/device" || verifyURL.User != nil || verifyURL.RawQuery != "" || verifyURL.Fragment != "" {
		return ChatGPTLoginInfo{}, errors.New("unsupported verification URL")
	}
	const codePrefix = "Enter code:"
	if !strings.HasPrefix(attempt.Instructions, codePrefix) {
		return ChatGPTLoginInfo{}, errors.New("unsupported verification instructions")
	}
	code := strings.TrimSpace(strings.TrimPrefix(attempt.Instructions, codePrefix))
	if !chatGPTCodePattern.MatchString(code) {
		return ChatGPTLoginInfo{}, errors.New("invalid verification code")
	}
	return ChatGPTLoginInfo{AttemptID: attempt.AttemptID, VerifyURL: verifyURL.String(), UserCode: code}, nil
}
