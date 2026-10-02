// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClient_HealthCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/global/health" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(Health{Healthy: true, Version: "1.17.13"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")
	h, err := c.HealthCheck(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !h.Healthy || h.Version != "1.17.13" {
		t.Fatalf("got %+v", h)
	}
}

func TestClient_CreateSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/session" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["title"] != "My chat" {
			t.Errorf("title not forwarded: %+v", body)
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "ses_created123"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")
	id, err := c.CreateSession(context.Background(), "My chat")
	if err != nil {
		t.Fatal(err)
	}
	if id != "ses_created123" {
		t.Fatalf("id = %q", id)
	}
}

func TestClient_ListSkills(t *testing.T) {
	var gotDirectory string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/skill" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		gotDirectory = r.URL.Query().Get("directory")
		json.NewEncoder(w).Encode([]Skill{
			{Name: "ux-writing", Description: "UX guidelines", Location: "/skills/ux-writing/SKILL.md", Content: "body"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")
	skills, err := c.ListSkills(context.Background(), "/home/developer/proj one")
	if err != nil {
		t.Fatal(err)
	}
	// /skill is workspace-routed, so the directory has to be passed rather
	// than assumed from the instance.
	if gotDirectory != "/home/developer/proj one" {
		t.Errorf("directory not forwarded, got %q", gotDirectory)
	}
	if len(skills) != 1 {
		t.Fatalf("got %d skills, want 1", len(skills))
	}
	if skills[0].Content != "body" || skills[0].Location != "/skills/ux-writing/SKILL.md" {
		t.Errorf("body and location must survive the decode: %+v", skills[0])
	}
}

func TestClient_ListSkillsEmptyIsNotNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.RawQuery; q != "" {
			t.Errorf("expected no query when no directory is given, got %q", q)
		}
		w.Write([]byte("[]"))
	}))
	defer srv.Close()

	skills, err := NewClient(srv.URL, "", "").ListSkills(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if skills == nil {
		t.Fatal("expected an empty slice, not nil")
	}
}

func TestClient_PromptAsync(t *testing.T) {
	var gotBody PromptRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session/ses_1/prompt_async" {
			t.Errorf("path = %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")
	pr := PromptRequest{
		Model: &PromptModel{ProviderID: "github-copilot", ModelID: "claude-sonnet-4.6"},
		Parts: []map[string]any{TextPart("hi there")},
		Agent: "plan",
	}
	if err := c.PromptAsync(context.Background(), "ses_1", pr); err != nil {
		t.Fatal(err)
	}
	if gotBody.Model == nil || gotBody.Model.ProviderID != "github-copilot" {
		t.Errorf("model not sent: %+v", gotBody.Model)
	}
	if gotBody.Agent != "plan" {
		t.Errorf("agent = %q", gotBody.Agent)
	}
	if len(gotBody.Parts) != 1 || gotBody.Parts[0]["text"] != "hi there" {
		t.Errorf("parts = %+v", gotBody.Parts)
	}
}

func TestClient_RespondPermission(t *testing.T) {
	var gotResponse string
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		gotResponse, _ = body["response"].(string)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")
	if err := c.RespondPermission(context.Background(), "ses_1", "per_9", "once"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/session/ses_1/permissions/per_9" {
		t.Errorf("path = %s", gotPath)
	}
	if gotResponse != "once" {
		t.Errorf("response = %q", gotResponse)
	}
}

func TestClient_Abort(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/session/ses_1/abort" && r.Method == http.MethodPost {
			hit = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")
	if err := c.Abort(context.Background(), "ses_1"); err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("abort endpoint not hit")
	}
}

func TestClient_BasicAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "opencode" || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(Health{Healthy: true})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "opencode", "secret")
	if _, err := c.HealthCheck(context.Background()); err != nil {
		t.Fatalf("basic auth not applied: %v", err)
	}
}

func TestClient_ErrorStatusIncludesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, "bad model")
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")
	_, err := c.CreateSession(context.Background(), "x")
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if !strings.Contains(err.Error(), "bad model") {
		t.Errorf("error should include body snippet: %v", err)
	}
}

func TestClient_Subscribe(t *testing.T) {
	// Emit a few SSE frames then close.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/event" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fl, _ := w.(http.Flusher)
		frames := []string{
			`{"type":"server.connected","properties":{}}`,
			`{"type":"message.part.updated","properties":{"sessionID":"ses_1","part":{"type":"text","text":"hi"}}}`,
			`{"type":"session.idle","properties":{"sessionID":"ses_1"}}`,
		}
		for _, f := range frames {
			fmt.Fprintf(w, "data: %s\n\n", f)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")
	var mu sync.Mutex
	var types []string
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := c.Subscribe(ctx, func(e ServerEvent) bool {
		mu.Lock()
		types = append(types, e.Type)
		mu.Unlock()
		return e.Type != "session.idle" // stop after idle
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"server.connected", "message.part.updated", "session.idle"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("types = %v, want %v", types, want)
	}
}

func TestSupervisor_FreePort(t *testing.T) {
	p, err := freePort("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if p <= 0 || p > 65535 {
		t.Fatalf("invalid port %d", p)
	}
}

func TestPermissionRegistry_RespondNoEntry(t *testing.T) {
	reg := &PermissionRegistry{entries: map[string]permEntry{}}
	ok, err := reg.Respond(context.Background(), "missing", "per_1", "once")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected ok=false when no entry is registered")
	}
}

func TestPermissionRegistry_RespondRoutes(t *testing.T) {
	var gotSession, gotPerm, gotResp string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /session/{sid}/permissions/{pid}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) == 4 {
			gotSession = parts[1]
			gotPerm = parts[3]
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		gotResp, _ = body["response"].(string)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	reg := &PermissionRegistry{entries: map[string]permEntry{}}
	reg.set("app_sess", NewClient(srv.URL, "", ""), "ses_remote")
	ok, err := reg.Respond(context.Background(), "app_sess", "per_7", "always")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if gotSession != "ses_remote" || gotPerm != "per_7" || gotResp != "always" {
		t.Fatalf("routed wrong: sess=%q perm=%q resp=%q", gotSession, gotPerm, gotResp)
	}
}
