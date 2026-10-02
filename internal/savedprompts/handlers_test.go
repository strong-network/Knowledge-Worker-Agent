// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package savedprompts

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

func setupTestDB(t *testing.T) {
	t.Helper()
	f, err := os.CreateTemp("", "sp-*.db")
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

func mux() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/saved-prompts", HandleList)
	m.HandleFunc("POST /api/saved-prompts", HandleCreate)
	m.HandleFunc("PUT /api/saved-prompts/reorder", HandleReorder)
	m.HandleFunc("PATCH /api/saved-prompts/{id}", HandleUpdate)
	m.HandleFunc("DELETE /api/saved-prompts/{id}", HandleDelete)
	return m
}

func do(m *http.ServeMux, method, path string, body any) *httptest.ResponseRecorder {
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	rr := httptest.NewRecorder()
	m.ServeHTTP(rr, r)
	return rr
}

func TestList_SeedsDefaults(t *testing.T) {
	setupTestDB(t)
	rr := do(mux(), "GET", "/api/saved-prompts", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var out struct {
		SavedPrompts []db.SavedPrompt `json:"saved_prompts"`
	}
	json.Unmarshal(rr.Body.Bytes(), &out)
	if len(out.SavedPrompts) != 5 {
		t.Fatalf("expected 5 seeded prompts, got %d", len(out.SavedPrompts))
	}
}

func TestCreate_RequiresNameAndPrompt(t *testing.T) {
	setupTestDB(t)
	rr := do(mux(), "POST", "/api/saved-prompts", map[string]string{"name": "  ", "prompt": ""})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("empty fields status = %d, want 422", rr.Code)
	}
}

func TestCreate_CustomAgentKindPreserved(t *testing.T) {
	setupTestDB(t)
	rr := do(mux(), "POST", "/api/saved-prompts", map[string]string{
		"name": "Briefing", "prompt": "Research and brief", "agent_kind": "custom", "agent_id": "demo-creator",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var p db.SavedPrompt
	json.Unmarshal(rr.Body.Bytes(), &p)
	if p.AgentKind != "custom" || p.AgentID != "demo-creator" {
		t.Errorf("custom agent not preserved: %+v", p)
	}
}

func TestCreate_CustomWithoutIDFallsBackToDefault(t *testing.T) {
	setupTestDB(t)
	rr := do(mux(), "POST", "/api/saved-prompts", map[string]string{
		"name": "X", "prompt": "y", "agent_kind": "custom", "agent_id": "",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d", rr.Code)
	}
	var p db.SavedPrompt
	json.Unmarshal(rr.Body.Bytes(), &p)
	if p.AgentKind != "default" {
		t.Errorf("expected fallback to default, got %q", p.AgentKind)
	}
}

func TestCreate_CapEnforced(t *testing.T) {
	setupTestDB(t)
	m := mux()
	// Seed (5) + create up to the cap of 12 → 7 more allowed.
	do(m, "GET", "/api/saved-prompts", nil)
	for i := 0; i < 7; i++ {
		rr := do(m, "POST", "/api/saved-prompts", map[string]string{"name": "n", "prompt": "p"})
		if rr.Code != http.StatusCreated {
			t.Fatalf("create %d status = %d", i, rr.Code)
		}
	}
	// The 13th prompt is rejected.
	rr := do(m, "POST", "/api/saved-prompts", map[string]string{"name": "over", "prompt": "cap"})
	if rr.Code != http.StatusConflict {
		t.Errorf("over-cap status = %d, want 409", rr.Code)
	}
}

func TestDeleteAndReorder(t *testing.T) {
	setupTestDB(t)
	m := mux()
	do(m, "GET", "/api/saved-prompts", nil) // seed
	list := db.ListSavedPrompts()

	// Delete the first seed.
	rr := do(m, "DELETE", "/api/saved-prompts/"+list[0].ID, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", rr.Code)
	}

	// Reverse the remaining order and persist it.
	remaining := db.ListSavedPrompts()
	ids := make([]string, len(remaining))
	for i, p := range remaining {
		ids[len(remaining)-1-i] = p.ID
	}
	rr = do(m, "PUT", "/api/saved-prompts/reorder", map[string]any{"ids": ids})
	if rr.Code != http.StatusOK {
		t.Fatalf("reorder status = %d", rr.Code)
	}
	after := db.ListSavedPrompts()
	if after[0].ID != ids[0] {
		t.Errorf("reorder not applied: want first %q, got %q", ids[0], after[0].ID)
	}
}
