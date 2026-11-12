// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodev2"
)

const testAuthURL = "https://auth.openai.com/api/accounts/authorize?client_id=dynamic_agent_client&redirect_uri=http%3A%2F%2F127.0.0.1%3A33157%2Fauth%2Fcallback&response_type=code&state=st4te"

func resetChatGPTAttempt() {
	chatGPTLogin.Lock()
	chatGPTLogin.info = ChatGPTLoginInfo{}
	chatGPTLogin.notified = false
	chatGPTLogin.Unlock()
}

func withChatGPTHooks(t *testing.T) {
	t.Helper()
	oldEnabled := chatGPTOpenCodeEnabled
	oldHasConnection := chatGPTHasOAuthConnection
	oldIntegration := chatGPTIntegration
	oldStart := chatGPTStartOAuth
	oldStatus := chatGPTOAuthStatus
	oldCancel := chatGPTCancelOAuth
	oldRemove := chatGPTRemoveCredentials
	oldHook := onLoginSuccess
	chatGPTLoginSuccess.RLock()
	oldChatGPTHook := chatGPTLoginSuccess.fn
	chatGPTLoginSuccess.RUnlock()
	chatGPTOpenCodeEnabled = func() bool { return true }
	chatGPTHasOAuthConnection = func(_ context.Context, _, _ string) (bool, error) { return true, nil }
	SetOnChatGPTLoginSuccess(nil)
	chatGPTIntegration = func(_ context.Context, _, _ string) ([]opencodev2.OAuthMethod, []opencodev2.IntegrationConnection, error) {
		return []opencodev2.OAuthMethod{{ID: chatGPTMethod, Type: "oauth"}}, nil, nil
	}
	chatGPTStartOAuth = func(_ context.Context, _, _, _ string) (opencodev2.OAuthAttempt, error) {
		return opencodev2.OAuthAttempt{
			AttemptID:    "con_test123",
			URL:          testAuthURL,
			Instructions: "Complete authorization in your browser. This window will close automatically.",
			Mode:         "auto",
		}, nil
	}
	chatGPTOAuthStatus = func(_ context.Context, _, _, _ string) (opencodev2.OAuthAttemptStatus, error) {
		return opencodev2.OAuthAttemptStatus{Status: "pending"}, nil
	}
	chatGPTCancelOAuth = func(_ context.Context, _, _, _ string) error { return nil }
	chatGPTRemoveCredentials = func(_ context.Context, _ string) error { return nil }
	loginSuccessMu.Lock()
	onLoginSuccess = nil
	loginSuccessMu.Unlock()
	resetChatGPTAttempt()
	t.Cleanup(func() {
		chatGPTOpenCodeEnabled = oldEnabled
		chatGPTHasOAuthConnection = oldHasConnection
		chatGPTIntegration = oldIntegration
		chatGPTStartOAuth = oldStart
		chatGPTOAuthStatus = oldStatus
		chatGPTCancelOAuth = oldCancel
		chatGPTRemoveCredentials = oldRemove
		SetOnChatGPTLoginSuccess(oldChatGPTHook)
		loginSuccessMu.Lock()
		onLoginSuccess = oldHook
		loginSuccessMu.Unlock()
		resetChatGPTAttempt()
	})
}

func TestChatGPTAuthenticatedReflectsSafeOpenCodeConnectionStatus(t *testing.T) {
	withChatGPTHooks(t)
	connected := true
	chatGPTHasOAuthConnection = func(_ context.Context, integration, _ string) (bool, error) {
		if integration != ChatGPTProvider {
			t.Fatalf("checked integration %q, want %q", integration, ChatGPTProvider)
		}
		return connected, nil
	}
	if !ChatGPTAuthenticated() {
		t.Fatal("an OpenCode OAuth connection was reported disconnected")
	}
	connected = false
	if ChatGPTAuthenticated() {
		t.Fatal("a missing OpenCode OAuth connection was reported connected")
	}
}

// ChatGPT sign-in is opencode 2.x only: on 1.x nothing reaches opencode, the
// provider reads as signed out, and starting a sign-in is refused.
func TestChatGPTIsUnavailableOnOpenCodeOneX(t *testing.T) {
	withChatGPTHooks(t)
	chatGPTOpenCodeEnabled = func() bool { return false }
	chatGPTHasOAuthConnection = func(context.Context, string, string) (bool, error) {
		t.Fatal("checked the connection through opencode 1.x")
		return false, nil
	}
	chatGPTStartOAuth = func(context.Context, string, string, string) (opencodev2.OAuthAttempt, error) {
		t.Fatal("started a sign-in on opencode 1.x")
		return opencodev2.OAuthAttempt{}, nil
	}
	if ChatGPTAuthenticated() {
		t.Fatal("ChatGPT reported signed in on opencode 1.x")
	}
	rec := httptest.NewRecorder()
	HandleChatGPTLoginStart(rec, httptest.NewRequest("POST", "/api/openai/auth/login/start", nil))
	if rec.Code != 409 {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestHandleChatGPTLoginStartReturnsOnlyAuthorizationURL(t *testing.T) {
	withChatGPTHooks(t)
	rec := httptest.NewRecorder()
	HandleChatGPTLoginStart(rec, httptest.NewRequest("POST", "/api/openai/auth/login/start", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal("response was not JSON")
	}
	if len(got) != 2 || got["attempt_id"] != "con_test123" || got["verify_url"] != testAuthURL {
		t.Fatalf("unexpected handoff: %v", got)
	}
}

func TestChatGPTBrowserInfoRejectsUnexpectedURLs(t *testing.T) {
	cb := func(redirect string) string {
		return "https://auth.openai.com/api/accounts/authorize?state=s&redirect_uri=" + url.QueryEscape(redirect)
	}
	for name, u := range map[string]string{
		"external host":   "https://evil.example/api/accounts/authorize?state=s&redirect_uri=" + url.QueryEscape("http://127.0.0.1:1/auth/callback"),
		"unexpected port": "https://auth.openai.com:444/api/accounts/authorize?state=s&redirect_uri=" + url.QueryEscape("http://127.0.0.1:1/auth/callback"),
		"unexpected path": "https://auth.openai.com/codex/device?state=s&redirect_uri=" + url.QueryEscape("http://127.0.0.1:1/auth/callback"),
		"non-loopback cb": cb("http://evil.example:1/auth/callback"),
		"cb without port": cb("http://127.0.0.1/auth/callback"),
		"cb other path":   cb("http://127.0.0.1:1/admin"),
		"no state":        "https://auth.openai.com/api/accounts/authorize?redirect_uri=" + url.QueryEscape("http://127.0.0.1:1/auth/callback"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := chatGPTBrowserInfo(opencodev2.OAuthAttempt{AttemptID: "con_x", URL: u, Mode: "auto"}); err == nil {
				t.Fatal("accepted unsupported OpenCode OAuth data")
			}
		})
	}
}

// The pasted callback is delivered to opencode's listener only when it is the
// running attempt's own: same port and path, same state, with a code.
func TestHandleChatGPTLoginCallbackForwardsOnlyTheAttemptsCallback(t *testing.T) {
	withChatGPTHooks(t)
	var got string
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.String()
		w.Write([]byte("access_token=secret"))
	}))
	defer listener.Close()
	lu, _ := url.Parse(listener.URL)
	authURL := "https://auth.openai.com/api/accounts/authorize?state=st4te&redirect_uri=" + url.QueryEscape(listener.URL+"/auth/callback")
	info, err := chatGPTBrowserInfo(opencodev2.OAuthAttempt{AttemptID: "con_test123", URL: authURL, Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	chatGPTLogin.Lock()
	chatGPTLogin.info = info
	chatGPTLogin.Unlock()

	post := func(attempt, pasted string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"url": pasted})
		req := httptest.NewRequest("POST", "/api/openai/auth/login/"+attempt+"/callback", bytes.NewReader(body))
		req.SetPathValue("attemptID", attempt)
		rec := httptest.NewRecorder()
		HandleChatGPTLoginCallback(rec, req)
		return rec
	}
	base := "http://localhost:" + lu.Port() + "/auth/callback"
	for name, c := range map[string]struct {
		attempt, url string
		want         int
	}{
		"other attempt": {"con_other", base + "?code=c&state=st4te", 404},
		"other port":    {"con_test123", "http://127.0.0.1:1/auth/callback?code=c&state=st4te", 400},
		"other host":    {"con_test123", "http://evil.example:" + lu.Port() + "/auth/callback?code=c&state=st4te", 400},
		"other path":    {"con_test123", "http://127.0.0.1:" + lu.Port() + "/x?code=c&state=st4te", 400},
		"wrong state":   {"con_test123", base + "?code=c&state=nope", 400},
		"no code":       {"con_test123", base + "?state=st4te", 400},
	} {
		if rec := post(c.attempt, c.url); rec.Code != c.want {
			t.Fatalf("%s: status = %d, want %d", name, rec.Code, c.want)
		}
	}
	if got != "" {
		t.Fatal("a rejected callback reached the listener")
	}
	rec := post("con_test123", base+"?code=c0de&state=st4te")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got != "/auth/callback?code=c0de&state=st4te" {
		t.Fatalf("listener got %q", got)
	}
}

func TestHandleChatGPTLoginStatusNotifiesOnceAndOmitsFailureText(t *testing.T) {
	withChatGPTHooks(t)
	chatGPTLogin.info = ChatGPTLoginInfo{AttemptID: "con_test123", VerifyURL: testAuthURL}
	chatGPTOAuthStatus = func(_ context.Context, _, _, _ string) (opencodev2.OAuthAttemptStatus, error) {
		return opencodev2.OAuthAttemptStatus{Status: "complete"}, nil
	}
	called := make(chan struct{}, 2)
	chatGPTCalled := make(chan struct{}, 2)
	SetOnLoginSuccess(func() { called <- struct{}{} })
	SetOnChatGPTLoginSuccess(func() { chatGPTCalled <- struct{}{} })
	for range 2 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/openai/auth/login/con_test123", nil)
		req.SetPathValue("attemptID", "con_test123")
		HandleChatGPTLoginStatus(rec, req)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"complete"`) {
			t.Fatalf("status response = %d %s", rec.Code, rec.Body.String())
		}
		if strings.Contains(strings.ToLower(rec.Body.String()), "token") {
			t.Fatal("status response included credential data")
		}
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("successful sign-in callback was not fired")
	}
	select {
	case <-called:
		t.Fatal("successful sign-in callback fired more than once")
	case <-time.After(25 * time.Millisecond):
	}
	select {
	case <-chatGPTCalled:
	case <-time.After(time.Second):
		t.Fatal("ChatGPT-specific success callback was not fired")
	}
	select {
	case <-chatGPTCalled:
		t.Fatal("ChatGPT-specific success callback fired more than once")
	case <-time.After(25 * time.Millisecond):
	}
}

func TestSignOutChatGPTRemovesOpenCodeCredentialByMetadata(t *testing.T) {
	withChatGPTHooks(t)
	removed := ""
	chatGPTRemoveCredentials = func(_ context.Context, integration string) error {
		removed = integration
		return nil
	}
	if err := SignOutChatGPT(); err != nil {
		t.Fatal("ChatGPT sign-out failed")
	}
	if removed != ChatGPTProvider {
		t.Fatalf("removed integration = %q, want %q", removed, ChatGPTProvider)
	}
}

// Sign-out cancels a sign-in in progress through the stub, and never passes
// opencode's response text on.
func TestSignOutChatGPTCancelsAttemptAndHidesErrors(t *testing.T) {
	withChatGPTHooks(t)
	chatGPTLogin.Lock()
	chatGPTLogin.info = ChatGPTLoginInfo{AttemptID: "con_inflight"}
	chatGPTLogin.Unlock()
	cancelled := ""
	chatGPTCancelOAuth = func(_ context.Context, _, attemptID, _ string) error {
		cancelled = attemptID
		return nil
	}
	chatGPTRemoveCredentials = func(context.Context, string) error {
		return errors.New("test token should not be surfaced")
	}
	err := SignOutChatGPT()
	if err == nil || strings.Contains(err.Error(), "test token") {
		t.Fatal("opencode sign-out error was missing or exposed its underlying response")
	}
	if cancelled != "con_inflight" {
		t.Fatalf("cancelled attempt = %q, want con_inflight", cancelled)
	}
}
