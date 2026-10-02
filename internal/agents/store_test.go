// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package agents

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/agentbuilder"
)

func newStore(t *testing.T, shipped ...string) *Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	set := map[string]bool{}
	for _, s := range shipped {
		set[s] = true
	}
	return &Store{Dir: dir, ShippedFn: func() map[string]bool { return set }}
}

func write(t *testing.T, s *Store, filename, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.Dir, filename), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStoreSaveAndGet(t *testing.T) {
	s := newStore(t)
	item, err := s.Save(&agentbuilder.Agent{
		Name:        "Release Notes Writer",
		Description: "Drafts release notes.",
		Prompt:      "Write notes.",
		Mode:        agentbuilder.ModeSubagent,
	}, "")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if item.ID != "release-notes-writer" || item.Filename != "release-notes-writer.md" {
		t.Errorf("unexpected item: %+v", item)
	}
	// File exists with well-formed content.
	data, err := os.ReadFile(filepath.Join(s.Dir, "release-notes-writer.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "mode: subagent") {
		t.Errorf("file missing serialized mode:\n%s", data)
	}
	got, err := s.Get("release-notes-writer")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Description != "Drafts release notes." {
		t.Errorf("Get description = %q", got.Description)
	}
}

func TestStoreListExcludesShipped(t *testing.T) {
	s := newStore(t, "product-manager.md")
	write(t, s, "product-manager.md", "---\ndescription: shipped\n---\nhi\n")
	write(t, s, "mine.md", "---\ndescription: user\n---\nhi\n")

	items, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "mine" {
		t.Fatalf("expected only user agent, got %+v", items)
	}
}

func TestStoreCollision(t *testing.T) {
	s := newStore(t)
	if _, err := s.Save(&agentbuilder.Agent{Name: "My Agent", Prompt: "p"}, ""); err != nil {
		t.Fatal(err)
	}
	// "my-agent" slugifies the same → collision on create.
	_, err := s.Save(&agentbuilder.Agent{Name: "my-agent", Prompt: "p"}, "")
	var ce ErrCollision
	if !asErr(err, &ce) {
		t.Fatalf("expected ErrCollision, got %v", err)
	}
}

func TestStoreRenameRemovesOldFile(t *testing.T) {
	s := newStore(t)
	if _, err := s.Save(&agentbuilder.Agent{Name: "Old Name", Prompt: "p"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(&agentbuilder.Agent{Name: "New Name", Prompt: "p"}, "old-name"); err != nil {
		t.Fatalf("rename save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "old-name.md")); !os.IsNotExist(err) {
		t.Error("old file should have been removed on rename")
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "new-name.md")); err != nil {
		t.Error("new file should exist after rename")
	}
}

func TestStoreEditSameNameNoCollision(t *testing.T) {
	s := newStore(t)
	if _, err := s.Save(&agentbuilder.Agent{Name: "Agent", Prompt: "p"}, ""); err != nil {
		t.Fatal(err)
	}
	// Editing in place (originalID == slug) must not be treated as a collision.
	if _, err := s.Save(&agentbuilder.Agent{Name: "Agent", Prompt: "changed"}, "agent"); err != nil {
		t.Fatalf("edit in place: %v", err)
	}
}

func TestStoreProtectShipped(t *testing.T) {
	s := newStore(t, "product-manager.md")
	write(t, s, "product-manager.md", "---\ndescription: shipped\n---\nhi\n")
	if err := s.Delete("product-manager"); err != ErrProtected {
		t.Errorf("Delete shipped = %v, want ErrProtected", err)
	}
	if _, err := s.Get("product-manager"); err != ErrProtected {
		t.Errorf("Get shipped = %v, want ErrProtected", err)
	}
	// Saving onto a shipped slug is refused.
	if _, err := s.Save(&agentbuilder.Agent{Name: "Product Manager", Prompt: "p"}, ""); err != ErrProtected {
		t.Errorf("Save onto shipped = %v, want ErrProtected", err)
	}
}

func TestStoreDeleteNotFound(t *testing.T) {
	s := newStore(t)
	if err := s.Delete("ghost"); err != ErrNotFound {
		t.Errorf("Delete missing = %v, want ErrNotFound", err)
	}
}

// --- HTTP layer ---

func newServer(t *testing.T, s *Store) *httptest.Server {
	mux := http.NewServeMux()
	(&Handlers{Store: s}).RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestHandlerCreateListDelete(t *testing.T) {
	s := newStore(t)
	srv := newServer(t, s)

	// Create.
	body := `{"name":"Helper Bot","description":"Helps.","prompt":"Be helpful.","mode":"subagent"}`
	resp, _ := http.Post(srv.URL+"/api/agent-builder/agents", "application/json", strings.NewReader(body))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	var item Item
	json.NewDecoder(resp.Body).Decode(&item)
	if item.ID != "helper-bot" {
		t.Fatalf("create id = %q", item.ID)
	}

	// List.
	resp, _ = http.Get(srv.URL + "/api/agent-builder/agents")
	var listResp struct{ Agents []Item }
	json.NewDecoder(resp.Body).Decode(&listResp)
	if len(listResp.Agents) != 1 {
		t.Fatalf("list len = %d", len(listResp.Agents))
	}

	// Delete.
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/agent-builder/agents/helper-bot", nil)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "helper-bot.md")); !os.IsNotExist(err) {
		t.Error("file not deleted")
	}
}

func TestHandlerCollisionReturns409(t *testing.T) {
	s := newStore(t)
	srv := newServer(t, s)
	body := `{"name":"Dup","prompt":"p"}`
	http.Post(srv.URL+"/api/agent-builder/agents", "application/json", strings.NewReader(body))
	resp, _ := http.Post(srv.URL+"/api/agent-builder/agents", "application/json", strings.NewReader(body))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
}

func TestHandlerValidationReturns422(t *testing.T) {
	s := newStore(t)
	srv := newServer(t, s)
	// All-symbol name → invalid slug.
	body := `{"name":"!!!","prompt":"p"}`
	resp, _ := http.Post(srv.URL+"/api/agent-builder/agents", "application/json", bytes.NewBufferString(body))
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", resp.StatusCode)
	}
	var out struct {
		Fields []agentbuilder.ValidationError
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Fields) == 0 {
		t.Error("expected field-level errors in 422 response")
	}
}

func TestHandlerUpdateRoundTrip(t *testing.T) {
	s := newStore(t)
	srv := newServer(t, s)
	http.Post(srv.URL+"/api/agent-builder/agents", "application/json",
		strings.NewReader(`{"name":"Agent","description":"v1","prompt":"p","mode":"subagent"}`))

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/agent-builder/agents/agent",
		strings.NewReader(`{"name":"Agent","description":"v2","prompt":"p","mode":"subagent"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d", resp.StatusCode)
	}
	got, _ := s.Get("agent")
	if got.Description != "v2" {
		t.Errorf("update didn't persist: %q", got.Description)
	}
}

func asErr(err error, target any) bool {
	if err == nil {
		return false
	}
	if ce, ok := target.(*ErrCollision); ok {
		var e ErrCollision
		if errorsAs(err, &e) {
			*ce = e
			return true
		}
	}
	return false
}

// small local errors.As to avoid an extra import in the test's helper.
func errorsAs(err error, target *ErrCollision) bool {
	e, ok := err.(ErrCollision)
	if ok {
		*target = e
	}
	return ok
}
