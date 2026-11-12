// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodev2"
)

func resetChatGPTAttempt() {
	chatGPTLogin.Lock()
	chatGPTLogin.info = ChatGPTLoginInfo{}
	chatGPTLogin.notified = false
	chatGPTLogin.Unlock()
}

func withChatGPTHooks(t *testing.T) {
	t.Helper()
	oldVersion := chatGPTIsOpenCodeV2
	oldEnabled := chatGPTOpenCodeEnabled
	oldHasConnection := chatGPTHasOAuthConnection
	oldAuthList := chatGPTAuthList
	oldAuthLogout := chatGPTAuthLogout
	oldIntegration := chatGPTIntegration
	oldStart := chatGPTStartOAuth
	oldStatus := chatGPTOAuthStatus
	oldCancel := chatGPTCancelOAuth
	oldRemove := chatGPTRemoveCredentials
	oldHook := onLoginSuccess
	chatGPTLoginSuccess.RLock()
	oldChatGPTHook := chatGPTLoginSuccess.fn
	chatGPTLoginSuccess.RUnlock()
	chatGPTIsOpenCodeV2 = func() bool { return true }
	chatGPTOpenCodeEnabled = func() bool { return true }
	chatGPTHasOAuthConnection = func(_ context.Context, _, _ string) (bool, error) { return true, nil }
	chatGPTAuthList = func(context.Context) (string, error) { return "openai oauth", nil }
	chatGPTAuthLogout = func(context.Context, string) error { return nil }
	SetOnChatGPTLoginSuccess(nil)
	chatGPTIntegration = func(_ context.Context, _, _ string) ([]opencodev2.OAuthMethod, []opencodev2.IntegrationConnection, error) {
		return []opencodev2.OAuthMethod{{ID: chatGPTDeviceMethod, Type: "oauth"}}, nil, nil
	}
	chatGPTStartOAuth = func(_ context.Context, _, _, _ string) (opencodev2.OAuthAttempt, error) {
		return opencodev2.OAuthAttempt{
			AttemptID:    "con_test123",
			URL:          "https://auth.openai.com/codex/device",
			Instructions: "Enter code: AB12-CD34",
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
		chatGPTIsOpenCodeV2 = oldVersion
		chatGPTOpenCodeEnabled = oldEnabled
		chatGPTHasOAuthConnection = oldHasConnection
		chatGPTAuthList = oldAuthList
		chatGPTAuthLogout = oldAuthLogout
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

func TestChatGPTAuthenticatedUsesOpenCodeOneXMetadataOnlyAuthList(t *testing.T) {
	withChatGPTHooks(t)
	chatGPTIsOpenCodeV2 = func() bool { return false }
	chatGPTAuthList = func(context.Context) (string, error) {
		return "\x1b[32mOpenAI oauth\x1b[0m\nGitHub Copilot oauth", nil
	}
	if !ChatGPTAuthenticated() {
		t.Fatal("OpenCode 1.x OAuth metadata was reported disconnected")
	}
	chatGPTAuthList = func(context.Context) (string, error) { return "OpenAI api", nil }
	if ChatGPTAuthenticated() {
		t.Fatal("OpenCode 1.x API key was reported as a ChatGPT subscription")
	}
}

func TestHandleChatGPTLoginStartReturnsOnlyDeviceHandoff(t *testing.T) {
	withChatGPTHooks(t)
	rec := httptest.NewRecorder()
	HandleChatGPTLoginStart(rec, httptest.NewRequest("POST", "/api/openai/auth/login/start", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got ChatGPTLoginInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal("response was not JSON")
	}
	if got.AttemptID != "con_test123" || got.VerifyURL != "https://auth.openai.com/codex/device" || got.UserCode != "AB12-CD34" {
		t.Fatalf("unexpected device handoff: %+v", got)
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "token") || strings.Contains(rec.Body.String(), "access") {
		t.Fatal("sign-in response included credential data")
	}
}

func TestChatGPTBrowserInfoRejectsUnexpectedCallbackAndInstructions(t *testing.T) {
	for name, attempt := range map[string]opencodev2.OAuthAttempt{
		"external host":           {AttemptID: "con_badhost", URL: "https://evil.example/codex/device", Instructions: "Enter code: AB12", Mode: "auto"},
		"unexpected port":         {AttemptID: "con_badport", URL: "https://auth.openai.com:444/codex/device", Instructions: "Enter code: AB12", Mode: "auto"},
		"unexpected query":        {AttemptID: "con_badquery", URL: "https://auth.openai.com/codex/device?next=evil", Instructions: "Enter code: AB12", Mode: "auto"},
		"unexpected instructions": {AttemptID: "con_badtext", URL: "https://auth.openai.com/codex/device", Instructions: "token: secret", Mode: "auto"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := chatGPTBrowserInfo(attempt); err == nil {
				t.Fatal("accepted unsupported OpenCode OAuth data")
			}
		})
	}
}

func TestHandleChatGPTLoginStatusNotifiesOnceAndOmitsFailureText(t *testing.T) {
	withChatGPTHooks(t)
	chatGPTLogin.info = ChatGPTLoginInfo{AttemptID: "con_test123", VerifyURL: "https://auth.openai.com/codex/device", UserCode: "AB12-CD34"}
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

func TestSignOutChatGPTUsesOpenCodeOneXLogoutAndHidesErrors(t *testing.T) {
	withChatGPTHooks(t)
	chatGPTIsOpenCodeV2 = func() bool { return false }
	removed := ""
	chatGPTAuthLogout = func(_ context.Context, provider string) error {
		removed = provider
		return errors.New("test token should not be surfaced")
	}
	err := SignOutChatGPT()
	if err == nil || strings.Contains(err.Error(), "test token") {
		t.Fatal("OpenCode sign-out error was missing or exposed its underlying response")
	}
	if removed != ChatGPTProvider {
		t.Fatalf("logged out provider = %q, want %q", removed, ChatGPTProvider)
	}
}
