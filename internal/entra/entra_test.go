// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package entra

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEntra is Microsoft's token endpoint for one tenant, issuing access tokens
// "at-1", "at-2"... and refresh tokens "rt-1", "rt-2"...
type fakeEntra struct {
	*httptest.Server
	mu        sync.Mutex
	forms     []url.Values
	issued    int
	refuse    string // an error_description to answer the next request with
	lifetime  string // expires_in, as Entra sends it
	noRefresh bool
}

func newFakeEntra(t *testing.T) *fakeEntra {
	f := &fakeEntra{lifetime: "3600"}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/tenant-1/oauth2/v2.0/token" {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		f.forms = append(f.forms, r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		if f.refuse != "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant", "error_description": f.refuse})
			f.refuse = ""
			return
		}
		f.issued++
		if f.noRefresh {
			fmt.Fprint(w, `{"access_token":"a","expires_in":3600}`)
			return
		}
		claims, _ := json.Marshal(map[string]string{"preferred_username": "alex@example.com"})
		at := fmt.Sprintf("h.%s.at-%d", base64.RawURLEncoding.EncodeToString(claims), f.issued)
		fmt.Fprintf(w, `{"token_type":"Bearer","access_token":%q,"refresh_token":"rt-%d","expires_in":%q}`, at, f.issued, f.lifetime)
	}))
	t.Cleanup(f.Close)
	t.Setenv("KWA_ENTRA_AUTHORITY", f.URL)
	return f
}

func (f *fakeEntra) lastForm() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.forms) == 0 {
		return nil
	}
	return f.forms[len(f.forms)-1]
}

func (f *fakeEntra) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.forms)
}

// handoffs records what would have gone to opencode.
type handoffs struct {
	mu    sync.Mutex
	calls []map[string]any
	held  bool
}

func stubOpencode(t *testing.T) *handoffs {
	h := &handoffs{}
	oldI, oldS, oldH := integrationOf, setCredential, signedIn
	integrationOf = func(_ context.Context, name string) (string, error) { return "mcp_" + name, nil }
	setCredential = func(_ context.Context, integration, label string, value map[string]any) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		if integration != "mcp_workiq" || label != credentialLabel {
			t.Errorf("credential for %q labelled %q", integration, label)
		}
		h.calls = append(h.calls, value)
		return nil
	}
	signedIn = func(context.Context, string) (bool, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.held, nil
	}
	t.Cleanup(func() { integrationOf, setCredential, signedIn = oldI, oldS, oldH })
	return h
}

func (h *handoffs) last(t *testing.T) map[string]any {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.calls) == 0 {
		t.Fatal("nothing was handed to opencode")
	}
	return h.calls[len(h.calls)-1]
}

func (h *handoffs) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls)
}

func setup(t *testing.T) (*fakeEntra, *handoffs, App) {
	t.Setenv("KWA_HOME", t.TempDir())
	t.Setenv("KWA_ENTRA_CLIENT_ID", "client-1")
	t.Setenv("KWA_ENTRA_TENANT_ID", "tenant-1")
	app, ok := Configured()
	if !ok {
		t.Fatal("not configured")
	}
	return newFakeEntra(t), stubOpencode(t), app
}

// landing is the address Entra sends the browser back to.
func landing(p *Pending, params string) string {
	return RedirectURI + "/?" + params + "&state=" + url.QueryEscape(p.state)
}

func TestConfigured(t *testing.T) {
	for _, c := range []struct {
		client, tenant string
		ok             bool
	}{
		{"0f3c-11", "contoso.onmicrosoft.com", true},
		{"0f3c-11", "a1b2c3d4-0000-0000-0000-000000000000", true},
		{"", "contoso.onmicrosoft.com", false},
		{"0f3c-11", "", false},
		{"0f3c-11", "contoso/../x", false},
		{"id with space", "contoso", false},
	} {
		t.Setenv("KWA_ENTRA_CLIENT_ID", c.client)
		t.Setenv("KWA_ENTRA_TENANT_ID", c.tenant)
		if _, ok := Configured(); ok != c.ok {
			t.Errorf("%q, %q: configured %v, want %v", c.client, c.tenant, ok, c.ok)
		}
	}
}

// metadataServer serves a connector's protected-resource metadata at path.
func metadataServer(t *testing.T, path string, md func(base string) map[string]any) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(md(srv.URL))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDetect(t *testing.T) {
	f := newFakeEntra(t)
	entraServer := f.URL + "/organizations/v2.0"
	ctx := context.Background()

	pathAware := metadataServer(t, "/.well-known/oauth-protected-resource/mcp", func(base string) map[string]any {
		return map[string]any{"resource": base + "/mcp", "authorization_servers": []string{entraServer}, "scopes_supported": []string{"api-1/Ask"}}
	})
	got, isEntra, err := Detect(ctx, pathAware.URL+"/mcp")
	if err != nil || !isEntra || got.ServerURL != pathAware.URL+"/mcp" || strings.Join(got.Scopes, " ") != "api-1/Ask offline_access" {
		t.Errorf("path-aware: %+v, %v, %v", got, isEntra, err)
	}

	rootOnly := metadataServer(t, "/.well-known/oauth-protected-resource", func(base string) map[string]any {
		return map[string]any{"resource": base, "authorization_servers": []string{entraServer}, "scopes_supported": []string{"api-1/Ask", "offline_access"}}
	})
	if got, isEntra, err := Detect(ctx, rootOnly.URL+"/v1/mcp"); err != nil || !isEntra || len(got.Scopes) != 2 {
		t.Errorf("root metadata: %+v, %v, %v", got, isEntra, err)
	}

	other := metadataServer(t, "/.well-known/oauth-protected-resource/mcp", func(base string) map[string]any {
		return map[string]any{"resource": base + "/mcp", "authorization_servers": []string{base}, "scopes_supported": []string{"read"}}
	})
	if _, isEntra, err := Detect(ctx, other.URL+"/mcp"); isEntra || err != nil {
		t.Errorf("another sign-in server: %v, %v", isEntra, err)
	}

	elsewhere := metadataServer(t, "/.well-known/oauth-protected-resource/mcp", func(string) map[string]any {
		return map[string]any{"resource": "https://example.com/mcp", "authorization_servers": []string{entraServer}, "scopes_supported": []string{"api-1/Ask"}}
	})
	if _, isEntra, err := Detect(ctx, elsewhere.URL+"/mcp"); isEntra || err == nil {
		t.Errorf("metadata for another resource: %v, %v", isEntra, err)
	}

	noScopes := metadataServer(t, "/.well-known/oauth-protected-resource/mcp", func(base string) map[string]any {
		return map[string]any{"resource": base + "/mcp", "authorization_servers": []string{entraServer}}
	})
	if _, isEntra, err := Detect(ctx, noScopes.URL+"/mcp"); !isEntra || err == nil {
		t.Errorf("no scopes: %v, %v", isEntra, err)
	}

	if _, _, err := Detect(ctx, "http://example.com/mcp"); err == nil {
		t.Error("plain http to another host was accepted")
	}
}

func TestSignIn(t *testing.T) {
	f, h, app := setup(t)
	target := Target{ServerURL: "https://workiq.example.com/mcp", Scopes: []string{"api-1/Ask", "offline_access"}}
	p := Begin("WorkIQ", app, target)

	link, err := url.Parse(p.URL)
	if err != nil {
		t.Fatal(err)
	}
	q := link.Query()
	if link.Path != "/tenant-1/oauth2/v2.0/authorize" || q.Get("client_id") != "client-1" || q.Get("redirect_uri") != RedirectURI ||
		q.Get("scope") != "api-1/Ask offline_access" || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" ||
		q.Get("code_challenge") != challengeOf(p.verifier) || q.Has("resource") {
		t.Fatalf("sign-in link %s", p.URL)
	}

	ctx := context.Background()
	for _, bad := range []string{
		"http://127.0.0.1:19876/oauth/callback?code=c&state=" + p.state,
		"http://localhost/elsewhere?code=c&state=" + p.state,
		"http://localhost/?code=c&state=someone-else",
		landing(p, "x=1"),
	} {
		if _, err := p.Complete(ctx, bad); !errors.As(err, new(AddressError)) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	_, err = p.Complete(ctx, landing(p, "error=consent_required&error_description="+url.QueryEscape("AADSTS65001: The user or administrator has not consented.")))
	if err == nil || !strings.Contains(err.Error(), "administrator has to approve") || !strings.Contains(err.Error(), "AADSTS65001") {
		t.Errorf("a refusal: %v", err)
	}
	if f.requests() != 0 {
		t.Fatalf("%d token requests before a code", f.requests())
	}

	rec, err := p.Complete(ctx, landing(p, "code=code-1"))
	if err != nil {
		t.Fatal(err)
	}
	form := f.lastForm()
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != "code-1" || form.Get("code_verifier") != p.verifier ||
		form.Get("redirect_uri") != RedirectURI || form.Get("client_id") != "client-1" || form.Has("client_secret") {
		t.Errorf("code exchange %v", form)
	}
	if rec.Server != "workiq" || rec.Account != "alex@example.com" || rec.RefreshToken != "rt-1" || time.Until(rec.ExpiresAt) < 59*time.Minute {
		t.Errorf("record %+v", rec)
	}
	path, _ := recordPath("workiq")
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("record file: %v, %v", st, err)
	}
	if !Has("WORKIQ") {
		t.Error("Has is false after a sign-in")
	}
	v := h.last(t)
	meta, _ := v["metadata"].(map[string]any)
	if v["type"] != "oauth" || v["access"] != rec.AccessToken || v["refresh"] != "" || v["expires"] != rec.ExpiresAt.UnixMilli() ||
		meta["serverUrl"] != target.ServerURL {
		t.Errorf("handed to opencode %v", v)
	}

	if err := Forget("workiq"); err != nil || Has("workiq") {
		t.Errorf("after Forget: %v, %v", err, Has("workiq"))
	}
}

func TestSignInNeedsARefreshToken(t *testing.T) {
	f, _, app := setup(t)
	f.mu.Lock()
	f.noRefresh = true
	f.mu.Unlock()
	p := Begin("workiq", app, Target{ServerURL: "https://x.example.com/mcp", Scopes: []string{"a", "offline_access"}})
	if _, err := p.Complete(context.Background(), landing(p, "code=c")); err == nil || !strings.Contains(err.Error(), "offline_access") {
		t.Errorf("no refresh token: %v", err)
	}
	if Has("workiq") {
		t.Error("kept a sign-in that can't be refreshed")
	}
}

func signIn(t *testing.T, app App) *Record {
	t.Helper()
	p := Begin("workiq", app, Target{ServerURL: "https://workiq.example.com/mcp", Scopes: []string{"api-1/Ask", "offline_access"}})
	rec, err := p.Complete(context.Background(), landing(p, "code=c"))
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func load(t *testing.T) *Record {
	t.Helper()
	storeMu.Lock()
	defer storeMu.Unlock()
	r, err := loadLocked("workiq")
	if err != nil || r == nil {
		t.Fatalf("load: %v, %v", r, err)
	}
	return r
}

func TestRefreshDue(t *testing.T) {
	f, h, app := setup(t)
	rec := signIn(t, app)
	ctx := context.Background()
	idle := func() bool { return false }
	working := func() bool { return true }

	refreshDue(ctx, rec.ExpiresAt.Add(-30*time.Minute), idle)
	if f.requests() != 1 {
		t.Fatalf("refreshed %d times with half an hour left", f.requests()-1)
	}
	refreshDue(ctx, rec.ExpiresAt.Add(-5*time.Minute), working)
	if f.requests() != 1 {
		t.Fatal("refreshed during a turn with five minutes left")
	}
	now := rec.ExpiresAt.Add(-90 * time.Second)
	refreshDue(ctx, now, working)
	if f.requests() != 2 {
		t.Fatal("didn't refresh during a turn with 90 seconds left")
	}
	if form := f.lastForm(); form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "rt-1" || form.Get("scope") != "api-1/Ask offline_access" {
		t.Errorf("refresh request %v", form)
	}
	got := load(t)
	if got.RefreshToken != "rt-2" || got.AccessToken == rec.AccessToken || !got.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Errorf("after the refresh %+v", got)
	}
	if v := h.last(t); v["access"] != got.AccessToken {
		t.Errorf("opencode got %v", v["access"])
	}

	f.mu.Lock()
	f.refuse = "AADSTS700082: The refresh token has expired due to inactivity."
	f.mu.Unlock()
	handed := h.count()
	refreshDue(ctx, got.ExpiresAt.Add(-time.Minute), idle)
	got = load(t)
	if !strings.Contains(got.Failed, "has expired. Sign in again") || h.count() != handed {
		t.Errorf("after a refused refresh: %q, %d handoffs", got.Failed, h.count()-handed)
	}
	requests := f.requests()
	refreshDue(ctx, got.ExpiresAt.Add(-time.Minute), idle)
	if f.requests() != requests {
		t.Error("retried a refresh token Microsoft refused")
	}
}

func TestRestoreGivesOpencodeBackALostToken(t *testing.T) {
	_, h, app := setup(t)
	signIn(t, app)
	handed := h.count()
	h.held = true
	restore(context.Background())
	if h.count() != handed {
		t.Error("handed a token opencode holds")
	}
	h.held = false
	restore(context.Background())
	if h.count() != handed+1 {
		t.Error("didn't hand a lost token back")
	}
}

func TestRecordPathRefusesOddNames(t *testing.T) {
	t.Setenv("KWA_HOME", t.TempDir())
	for _, bad := range []string{"", "../x", "a/b", "a..b", ".hidden", "with space"} {
		if _, err := recordPath(bad); err == nil {
			t.Errorf("%q named a file", bad)
		}
	}
	if p, err := recordPath("Work-IQ_2"); err != nil || !strings.HasSuffix(p, "/entra/work-iq_2.json") {
		t.Errorf("Work-IQ_2: %s, %v", p, err)
	}
}

func TestErrorMessages(t *testing.T) {
	for _, c := range []struct {
		err  *Error
		want string
	}{
		{newError("invalid_grant", "AADSTS70043: The refresh token has expired.", nil, 400), "Your Microsoft sign-in has expired"},
		{newError("invalid_client", "", []int{7000218}, 401), "public clients"},
		{newError("access_denied", "The user cancelled.", nil, 0), "cancelled"},
		{newError("server_error", "AADSTS99999: Something new.\r\nTrace ID: 1", nil, 500), "Microsoft sign-in refused: AADSTS99999: Something new."},
	} {
		if got := c.err.Error(); !strings.Contains(got, c.want) || strings.Contains(got, "Trace ID") {
			t.Errorf("%+v: %q", c.err, got)
		}
	}
	if !newError("invalid_grant", "", nil, 400).Expired || newError("invalid_request", "", nil, 400).Expired {
		t.Error("Expired")
	}
}

func TestExpiresInAsANumber(t *testing.T) {
	var tk tokens
	if err := json.Unmarshal([]byte(`{"access_token":"a","expires_in":3599}`), &tk); err != nil || tk.ExpiresIn != 3599 {
		t.Errorf("%+v, %v", tk, err)
	}
}

// A connector entry can name the permission instead of the published one.
func TestWithScopes(t *testing.T) {
	target := Target{ServerURL: "https://mcp.example.com/mcp", Scopes: []string{"https://mcp.example.com/mcp/.default", "offline_access"}}
	got := target.WithScopes([]string{"res-1/.default", " ", "OFFLINE_ACCESS", "res-1/.default"})
	if strings.Join(got.Scopes, " ") != "res-1/.default offline_access" || got.ServerURL != target.ServerURL {
		t.Errorf("got %+v", got)
	}
	if strings.Join(target.Scopes, " ") != "https://mcp.example.com/mcp/.default offline_access" {
		t.Errorf("the original changed: %v", target.Scopes)
	}
}
