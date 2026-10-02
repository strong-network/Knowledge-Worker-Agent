// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// resetLoginForTest returns the login state machine to a clean slate.
func resetLoginForTest(t *testing.T) {
	t.Helper()
	login.mu.Lock()
	resetLoginLocked()
	login.mu.Unlock()
}

// waitForLogin polls snapshotLogin until pred is satisfied or the deadline
// elapses, returning the last snapshot seen.
func waitForLogin(t *testing.T, d time.Duration, pred func(LoginInfo) bool) LoginInfo {
	t.Helper()
	deadline := time.Now().Add(d)
	var last LoginInfo
	for time.Now().Before(deadline) {
		last = snapshotLogin()
		if pred(last) {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	return last
}

// TestDeviceFlow_CompletesAndPersists drives the full device flow against a
// mock GitHub: device-code request, one authorization_pending poll, then a
// token. It asserts the flow reaches done+success AND writes auth.json — i.e.
// the "never finishes" symptom would fail here.
func TestDeviceFlow_CompletesAndPersists(t *testing.T) {
	resetLoginForTest(t)

	// Isolate the credential store to a temp dir.
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	prevAgent := UserAgent
	UserAgent = "KnowledgeWorkerAgent/9.9.9"
	defer func() { UserAgent = prevAgent }()
	var agents sync.Map

	var tokenPolls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/login/device/code", func(w http.ResponseWriter, r *http.Request) {
		agents.Store(r.URL.Path, r.UserAgent())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(deviceCodeResp{
			DeviceCode:      "DEVICE-XYZ",
			UserCode:        "WXYZ-1234",
			VerificationURI: "https://github.com/login/device",
			ExpiresIn:       900,
			Interval:        1, // 1s poll to keep the test quick
		})
	})
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		agents.Store(r.URL.Path, r.UserAgent())
		w.Header().Set("Content-Type", "application/json")
		n := atomic.AddInt32(&tokenPolls, 1)
		if n == 1 {
			// First poll: still pending.
			_ = json.NewEncoder(w).Encode(tokenResp{Error: "authorization_pending"})
			return
		}
		// Subsequently: issue the token.
		_ = json.NewEncoder(w).Encode(tokenResp{
			AccessToken: "gho_DEVICEFLOWTOKEN",
			TokenType:   "bearer",
			Scope:       "read:user",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Point the flow at the mock.
	oldDev, oldTok := deviceCodeURL, accessTokenURL
	deviceCodeURL = srv.URL + "/login/device/code"
	accessTokenURL = srv.URL + "/login/oauth/access_token"
	defer func() { deviceCodeURL, accessTokenURL = oldDev, oldTok }()

	// Record whether the success callback fires.
	var cbFired int32
	SetOnLoginSuccess(func() { atomic.StoreInt32(&cbFired, 1) })
	defer SetOnLoginSuccess(nil)

	if err := startLogin(); err != nil {
		t.Fatalf("startLogin: %v", err)
	}

	// The first snapshot should already carry the user code + verify URL.
	first := snapshotLogin()
	if first.UserCode != "WXYZ-1234" {
		t.Errorf("expected user code on first snapshot, got %q", first.UserCode)
	}
	if !first.Running {
		t.Errorf("expected running=true right after start, got %+v", first)
	}

	// Within a few seconds the flow must reach done+success.
	final := waitForLogin(t, 6*time.Second, func(li LoginInfo) bool { return li.Done })
	for _, path := range []string{"/login/device/code", "/login/oauth/access_token"} {
		if got, _ := agents.Load(path); got != "KnowledgeWorkerAgent/9.9.9" {
			t.Errorf("%s: User-Agent %q", path, got)
		}
	}
	if !final.Done {
		t.Fatalf("device flow never reached done: %+v", final)
	}
	if !final.Success {
		t.Fatalf("device flow finished without success: %+v", final)
	}
	if final.Running {
		t.Errorf("running must be false once done, got %+v", final)
	}
	if final.Error != "" {
		t.Errorf("unexpected error on success: %q", final.Error)
	}

	// The credential must be written to auth.json.
	authPath := filepath.Join(dir, "opencode", "auth.json")
	raw, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("auth.json not written: %v", err)
	}
	var creds map[string]map[string]any
	if err := json.Unmarshal(raw, &creds); err != nil {
		t.Fatalf("auth.json invalid: %v", err)
	}
	if creds[Provider]["access"] != "gho_DEVICEFLOWTOKEN" {
		t.Errorf("token not persisted correctly: %+v", creds[Provider])
	}

	// The post-login success callback should have fired.
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&cbFired) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if atomic.LoadInt32(&cbFired) == 0 {
		t.Errorf("onLoginSuccess callback did not fire")
	}
}

// TestRequestToken_SlowDownHonorsGitHubInterval asserts that a "slow_down"
// response returns GitHub's requested backoff interval (not a hardcoded value).
// Polling faster than GitHub demands keeps it returning slow_down forever, which
// is the root cause of the flow never finishing.
func TestRequestToken_SlowDownHonorsGitHubInterval(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(tokenResp{Error: "slow_down", Interval: 15})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	oldTok := accessTokenURL
	accessTokenURL = srv.URL + "/login/oauth/access_token"
	defer func() { accessTokenURL = oldTok }()

	tok, retry, err := requestToken("D")
	if err != nil {
		t.Fatalf("requestToken: %v", err)
	}
	if tok != "" {
		t.Errorf("expected no token on slow_down, got %q", tok)
	}
	if retry != 15*time.Second {
		t.Errorf("expected 15s backoff from GitHub, got %s", retry)
	}
}

// TestRequestToken_SlowDownWithoutInterval falls back to a safe backoff when
// GitHub omits the interval on a slow_down.
func TestRequestToken_SlowDownWithoutInterval(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(tokenResp{Error: "slow_down"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	oldTok := accessTokenURL
	accessTokenURL = srv.URL + "/login/oauth/access_token"
	defer func() { accessTokenURL = oldTok }()

	_, retry, err := requestToken("D")
	if err != nil {
		t.Fatalf("requestToken: %v", err)
	}
	if retry < 5*time.Second {
		t.Errorf("expected a safe (>=5s) fallback backoff, got %s", retry)
	}
}

// TestDeviceFlow_RecoversAfterSlowDown drives the full flow through a slow_down
// response and asserts it still completes (rather than looping forever).
func TestDeviceFlow_RecoversAfterSlowDown(t *testing.T) {
	resetLoginForTest(t)
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	var polls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/login/device/code", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(deviceCodeResp{
			DeviceCode: "D", UserCode: "AAAA-BBBB",
			VerificationURI: "https://github.com/login/device", Interval: 1,
		})
	})
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		switch atomic.AddInt32(&polls, 1) {
		case 1:
			// Ask the client to slow down to 1s (kept small for the test).
			_ = json.NewEncoder(w).Encode(tokenResp{Error: "slow_down", Interval: 1})
		default:
			_ = json.NewEncoder(w).Encode(tokenResp{AccessToken: "gho_AFTERSLOWDOWN"})
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	oldDev, oldTok := deviceCodeURL, accessTokenURL
	deviceCodeURL = srv.URL + "/login/device/code"
	accessTokenURL = srv.URL + "/login/oauth/access_token"
	defer func() { deviceCodeURL, accessTokenURL = oldDev, oldTok }()

	if err := startLogin(); err != nil {
		t.Fatalf("startLogin: %v", err)
	}
	final := waitForLogin(t, 8*time.Second, func(li LoginInfo) bool { return li.Done })
	if !final.Done || !final.Success {
		t.Fatalf("flow should complete after slow_down, got %+v", final)
	}
}

// TestHandleLoginStartReturnsCodeImmediately verifies the HTTP handler surfaces
// the user code on the very first /start response (so the UI can render it
// without waiting for a poll).
func TestHandleLoginStartReturnsCodeImmediately(t *testing.T) {
	resetLoginForTest(t)
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	mux := http.NewServeMux()
	mux.HandleFunc("/login/device/code", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(deviceCodeResp{
			DeviceCode:      "D",
			UserCode:        "AAAA-BBBB",
			VerificationURI: "https://github.com/login/device",
			Interval:        1,
		})
	})
	// Token endpoint stays pending forever for this test.
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(tokenResp{Error: "authorization_pending"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	oldDev, oldTok := deviceCodeURL, accessTokenURL
	deviceCodeURL = srv.URL + "/login/device/code"
	accessTokenURL = srv.URL + "/login/oauth/access_token"
	defer func() {
		deviceCodeURL, accessTokenURL = oldDev, oldTok
		// Stop the background poller so it doesn't leak into other tests.
		login.mu.Lock()
		if login.cancel != nil {
			login.cancel()
		}
		login.mu.Unlock()
	}()

	rec := httptest.NewRecorder()
	HandleLoginStart(rec, httptest.NewRequest(http.MethodPost, "/api/opencode/auth/login/start", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var li LoginInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &li); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if li.UserCode != "AAAA-BBBB" {
		t.Errorf("expected user code AAAA-BBBB on start, got %q", li.UserCode)
	}
	if !li.Running {
		t.Errorf("expected running=true, got %+v", li)
	}
}
