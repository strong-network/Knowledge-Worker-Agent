// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/env/envtest"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/files"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodeauth"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/providers"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/vertexauth"
)

func setupServerTestDB(t *testing.T) {
	t.Helper()
	f, err := os.CreateTemp("", "server-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	t.Cleanup(func() {
		db.Close()
		os.Remove(path)
	})
	if err := db.Init(path); err != nil {
		t.Fatal(err)
	}
}

func TestHandleModelStatsReadsDatabase(t *testing.T) {
	setupServerTestDB(t)
	if err := db.IncrementModelStats("claude-sonnet-4.6", 1, 120, 300, 3, 2, 40, 8, 0); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession("session-1", config.SessionConfig{Label: "Stats"}); err != nil {
		t.Fatal(err)
	}
	if err := db.IncrementSessionStats("session-1", 3); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/model-stats", nil)
	w := httptest.NewRecorder()
	handleModelStats(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Models  []db.ModelStat  `json:"models"`
		Summary db.UsageSummary `json:"summary"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("expected 1 model stat, got %d", len(resp.Models))
	}
	got := resp.Models[0]
	if got.Model != "claude-sonnet-4.6" {
		t.Errorf("expected model claude-sonnet-4.6, got %q", got.Model)
	}
	if got.Requests != 1 || got.Count != 1 {
		t.Errorf("expected 1 request/count, got requests=%d count=%d", got.Requests, got.Count)
	}
	if got.PremiumReqs != 1 || got.TotalInput != 120 || got.TotalOutput != 300 {
		t.Errorf("unexpected stat totals: %+v", got)
	}
	if got.ToolCalls != 3 {
		t.Errorf("unexpected tool call total: %+v", got)
	}
	if got.FilesModified != 2 || got.LinesAdded != 40 || got.LinesRemoved != 8 {
		t.Errorf("unexpected change totals: %+v", got)
	}
	if resp.Summary.TotalSessions != 1 || resp.Summary.TotalToolCalls != 3 || resp.Summary.TotalRequests != 1 {
		t.Errorf("unexpected summary: %+v", resp.Summary)
	}
}

// TestRegisterRoutes_NoDuplicates ensures registerRoutes() does not
// register the same (method, path) twice. http.ServeMux panics on
// duplicate registration, so this also catches future regressions
// where a stub and a real handler accidentally collide.
func TestRegisterRoutes_NoDuplicates(t *testing.T) {
	setupServerTestDB(t)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("registerRoutes panicked (likely duplicate pattern): %v", r)
		}
	}()

	mux := http.NewServeMux()
	registerRoutes(mux)
}

// TestRegisterRoutes_OpencodeAuthStatusUsesRealHandler asserts that the
// /api/opencode/auth/status route reaches the real internal/opencodeauth
// handler (which reflects actual auth state), not a stub.
func TestRegisterRoutes_OpencodeAuthStatusUsesRealHandler(t *testing.T) {
	setupServerTestDB(t)

	t.Setenv("HOME", t.TempDir())

	mux := http.NewServeMux()
	registerRoutes(mux)

	req := httptest.NewRequest("GET", "/api/opencode/auth/status", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v (body=%q)", err, w.Body.String())
	}
}

// TestRegisterRoutesNoDuplicates ensures the full route table can be
// registered onto a fresh ServeMux without panicking on duplicate patterns.
// Regression test for the duplicate `GET /api/auth/status` panic at startup.
func TestRegisterRoutesNoDuplicates(t *testing.T) {
	setupServerTestDB(t)
	config.Workspace = t.TempDir()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("registerRoutes panicked: %v", r)
		}
	}()

	mux := http.NewServeMux()
	registerRoutes(mux)
}

func TestChatModel(t *testing.T) {
	chat := opencodeauth.ModelDetail{ToolCall: true, OutputText: true, Status: "active"}
	if !chatModel(chat) {
		t.Error("a text model with tools was left out")
	}
	for name, d := range map[string]opencodeauth.ModelDetail{
		"no tools":   {OutputText: true},
		"image":      {ToolCall: true, OutputText: true, OutputImage: true},
		"speech":     {ToolCall: true, OutputAudio: true},
		"deprecated": {ToolCall: true, OutputText: true, Status: "deprecated"},
	} {
		if chatModel(d) {
			t.Errorf("%s model counted as a chat model", name)
		}
	}
}

// An unanswered check keeps a model's last answer, so a passing outage doesn't
// empty the picker, nor bring back a model that was refused.
func TestVertexAvailable(t *testing.T) {
	ids := map[string]string{
		"ok": "google-vertex/ok", "no": "google-vertex/no",
		"flaky-was-ok": "google-vertex/flaky-was-ok", "flaky-new": "google-vertex/flaky-new",
	}
	states := map[string]vertexauth.Availability{
		"ok": vertexauth.Available, "no": vertexauth.Unavailable,
		"flaky-was-ok": vertexauth.Unknown, "flaky-new": vertexauth.Unknown,
	}
	last := map[string]bool{"google-vertex/flaky-was-ok": true, "google-vertex/no": true}

	got := sortedKeys(vertexAvailable(ids, states, last))
	want := []string{"google-vertex/flaky-was-ok", "google-vertex/ok"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

// registerTestBuiltins installs stub built-ins with scripted auth state, in the
// same order initProviderRegistry() uses (Vertex first). The real CheckAuth
// funcs shell out to gcloud / read the copilot credential store, neither of
// which a unit test may depend on.
func registerTestBuiltins(t *testing.T, copilotOK, vertexOK bool) {
	t.Helper()
	envtest.Clear(t, config.EnvCentralConfig)
	providers.ResetBuiltins()
	t.Cleanup(providers.ResetBuiltins)
	providers.RegisterBuiltin(providers.Builtin{
		ID:        vertexauth.Provider,
		AuthType:  providers.AuthGcloudADC,
		CheckAuth: func() bool { return vertexOK },
	})
	providers.RegisterBuiltin(providers.Builtin{
		ID:        opencodeauth.Provider,
		AuthType:  providers.AuthOAuthDevice,
		CheckAuth: func() bool { return copilotOK },
	})
}

// TestAuthenticatedProvidersAreGated verifies the model list stays auth-gated
// now that the provider registry makes the decision: `opencode models
// <provider>` lists a provider's catalog once it is merely *configured* (Vertex
// env vars are seeded by EnsureEnv), not once it is authenticated — so a
// Copilot-only user must never be offered, nor auto-assigned, a google-vertex
// model.
func TestAuthenticatedProvidersAreGated(t *testing.T) {
	cases := []struct {
		name                string
		copilotOK, vertexOK bool
		want                []string
	}{
		{"copilot only", true, false, []string{opencodeauth.Provider}},
		{"vertex only", false, true, []string{vertexauth.Provider}},
		{"both", true, true, []string{vertexauth.Provider, opencodeauth.Provider}},
		{"neither", false, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registerTestBuiltins(t, tc.copilotOK, tc.vertexOK)
			got := providers.Resolve().Authenticated()
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("index %d: got %q, want %q (full: %v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

// TestPreferredDefaultProvider verifies the server-side default follows real
// auth state, with Vertex winning the both-signed-in tiebreak and no preference
// expressed when neither provider is signed in.
//
// The tiebreak is now expressed as registry order rather than a hard-coded
// branch, so this drives it through the registry to prove the ordering
// contract, not just the one-line helper.
func TestPreferredDefaultProvider(t *testing.T) {
	cases := []struct {
		name                string
		copilotOK, vertexOK bool
		want                string
	}{
		{"copilot only", true, false, opencodeauth.Provider},
		{"vertex only", false, true, vertexauth.Provider},
		{"both prefers vertex", true, true, vertexauth.Provider},
		{"neither", false, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registerTestBuiltins(t, tc.copilotOK, tc.vertexOK)
			got := preferredDefaultProvider(providers.Resolve().Authenticated())
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Skills resolve per directory, so the picker has to be told which directory
// this chat would actually run in — otherwise a session pointed at a project
// workspace would be offered the default workspace's skills.
func TestSkillsWorkdirPrefersTheSessionWorkspace(t *testing.T) {
	setupServerTestDB(t)

	prev := config.Workspace
	config.Workspace = "/home/developer"
	t.Cleanup(func() { config.Workspace = prev })

	if err := db.CreateSession("with-workdir", config.SessionConfig{Workdir: "/home/developer/proj"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession("no-workdir", config.SessionConfig{}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		sessionID string
		workdir   string
		want      string
	}{
		{"session wins", "with-workdir", "/tmp/ignored", "/home/developer/proj"},
		{"explicit workdir when the session has none", "no-workdir", "/tmp/explicit", "/tmp/explicit"},
		// A brand-new chat has no session row yet, which is exactly when
		// someone wants to arm a skill.
		{"unknown session falls back to the explicit workdir", "never-created", "/tmp/explicit", "/tmp/explicit"},
		{"nothing given falls back to the workspace", "", "", "/home/developer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := skillsWorkdir(tc.sessionID, tc.workdir); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRegistryPresetsResolvesAllThreePresets(t *testing.T) {
	reg := providers.Registry{Providers: []providers.Provider{{
		ID:            "mistral-onprem",
		Authenticated: true,
		Presets: map[string]string{
			config.PresetDefault:  "mistral-onprem/mistral-large",
			config.PresetThinking: "mistral-onprem/mistral-large",
			config.PresetSmall:    "mistral-onprem/mistral-small",
		},
	}}}
	models := []string{"mistral-onprem/mistral-large", "mistral-onprem/mistral-small"}

	got := registryPresets(reg, models)
	// "small" is not a composer choice, so it is easy to add the constant and
	// forget this loop — at which case titles silently keep running on the
	// conversation's own model and the nomination does nothing.
	want := map[string]string{
		config.PresetDefault:  "mistral-onprem/mistral-large",
		config.PresetThinking: "mistral-onprem/mistral-large",
		config.PresetSmall:    "mistral-onprem/mistral-small",
	}
	for name, model := range want {
		if got[name] != model {
			t.Errorf("preset %q = %q, want %q", name, got[name], model)
		}
	}
}

func TestRegistryPresetsSkipsUndiscoveredNominations(t *testing.T) {
	reg := providers.Registry{Providers: []providers.Provider{{
		ID:            "mistral-onprem",
		Authenticated: true,
		Presets:       map[string]string{config.PresetSmall: "mistral-onprem/retired"},
	}}}

	// Publishing a nomination the workspace cannot run would send every title
	// to a dead model id; leaving it unset falls back to the session's model.
	if got := registryPresets(reg, []string{"mistral-onprem/mistral-large"}); len(got) != 0 {
		t.Errorf("got %v, want no presets", got)
	}
}

// The owner's file routes keep shared files in step: a rename moves the
// shared file's record with it, and a delete stops sharing it first.
func TestFileRoutesKeepSharedFilesInStep(t *testing.T) {
	setupServerTestDB(t)
	t.Setenv("HOME", t.TempDir())
	prevRename, prevDelete := files.AfterRename, files.BeforeDelete
	t.Cleanup(func() { files.AfterRename, files.BeforeDelete = prevRename, prevDelete })
	mux := http.NewServeMux()
	registerRoutes(mux)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.html"), []byte("<p>Q3</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSharedArtifact(db.SharedArtifact{ID: "a1", Folder: dir, RelPath: "report.html", Port: 1}); err != nil {
		t.Fatal(err)
	}
	post := func(path string, body any) int {
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", path, bytes.NewReader(b)))
		return rec.Code
	}
	if code := post("/api/files/rename", map[string]string{"src": filepath.Join(dir, "report.html"), "dest": filepath.Join(dir, "q3.html")}); code != http.StatusOK {
		t.Fatalf("rename: %d", code)
	}
	if a, ok := db.GetSharedArtifact("a1"); !ok || a.RelPath != "q3.html" {
		t.Fatalf("after the rename: %+v", a)
	}
	if code := post("/api/files/delete", map[string]string{"path": filepath.Join(dir, "q3.html")}); code != http.StatusOK {
		t.Fatalf("delete: %d", code)
	}
	if _, ok := db.GetSharedArtifact("a1"); ok {
		t.Error("the deleted file is still shared")
	}
}
