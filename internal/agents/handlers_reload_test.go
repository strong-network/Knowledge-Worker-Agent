// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package agents

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestReloadFnInvokedOnMutations verifies the Handlers call ReloadFn after a
// successful create and delete, so the backend can pick up the on-disk change
// immediately (the fix for "OpenCode needs a restart to see new agents").
func TestReloadFnInvokedOnMutations(t *testing.T) {
	dir := t.TempDir()
	var calls int
	h := &Handlers{
		Store:    &Store{Dir: dir},
		ReloadFn: func() { calls++ },
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Create.
	body, _ := json.Marshal(agentPayload{Name: "My Helper", Prompt: "Be helpful."})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/agent-builder/agents", bytes.NewReader(body)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	if calls != 1 {
		t.Fatalf("expected ReloadFn after create, calls=%d", calls)
	}

	// Delete.
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/api/agent-builder/agents/my-helper", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if calls != 2 {
		t.Fatalf("expected ReloadFn after delete, calls=%d", calls)
	}
}

// TestReloadFnNotInvokedOnFailedMutation ensures a rejected save does not signal
// a reload (nothing changed on disk).
func TestReloadFnNotInvokedOnFailedMutation(t *testing.T) {
	dir := t.TempDir()
	var calls int
	h := &Handlers{
		Store:    &Store{Dir: dir},
		ReloadFn: func() { calls++ },
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Empty name fails validation → no file written, no reload.
	body, _ := json.Marshal(agentPayload{Name: "", Prompt: "x"})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/agent-builder/agents", bytes.NewReader(body)))
	if rr.Code == http.StatusCreated {
		t.Fatalf("expected failure for empty name, got %d", rr.Code)
	}
	if calls != 0 {
		t.Fatalf("expected no ReloadFn on failed save, calls=%d", calls)
	}
}

// TestReloadFnNilSafe ensures mutations work when no ReloadFn is wired.
func TestReloadFnNilSafe(t *testing.T) {
	dir := t.TempDir()
	h := &Handlers{Store: &Store{Dir: dir}}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	body, _ := json.Marshal(agentPayload{Name: "Solo", Prompt: "Be helpful."})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/agent-builder/agents", bytes.NewReader(body)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201 with nil ReloadFn, got %d: %s", rr.Code, rr.Body.String())
	}
}
