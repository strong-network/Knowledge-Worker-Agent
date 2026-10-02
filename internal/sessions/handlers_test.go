// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessions

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/workdirs"
)

func setupTestDB(t *testing.T) {
	t.Helper()
	config.Workspace = "/tmp"
	f, err := os.CreateTemp("", "sessions-test-*.db")
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

func TestHandleNewSession(t *testing.T) {
	setupTestDB(t)

	body := `{"label":"My Chat","mode":"plan"}`
	req := httptest.NewRequest("POST", "/api/sessions/new", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	HandleNewSession(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["session_id"] == nil || resp["session_id"] == "" {
		t.Error("expected session_id in response")
	}
}

// New chat: a new chat with no explicit workdir gets its own auto-created
// per-chat folder named "chat-<id>", created inside the "Chats" subdirectory of
// the base workspace rather than beside the user's own files in its root.
func TestHandleNewSession_AutoWorkspaceFolder(t *testing.T) {
	setupTestDB(t)
	base := t.TempDir()
	config.Workspace = base

	req := httptest.NewRequest("POST", "/api/sessions/new", strings.NewReader(`{"label":"Ideas"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	HandleNewSession(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Config config.SessionConfig `json:"config"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)

	wd := resp.Config.Workdir
	if wd == base || wd == "" {
		t.Fatalf("expected an auto per-chat folder distinct from base %q, got %q", base, wd)
	}
	chats := filepath.Join(base, workdirs.ChatsDirName)
	if filepath.Dir(wd) != chats {
		t.Errorf("expected folder under %q, got %q", chats, wd)
	}
	if !strings.HasPrefix(filepath.Base(wd), "chat-") {
		t.Errorf("expected folder named chat-<id>, got %q", filepath.Base(wd))
	}
	if info, err := os.Stat(wd); err != nil || !info.IsDir() {
		t.Errorf("expected auto folder to exist as a directory, err=%v", err)
	}
	// The folder the rails are asked about at delete time is the one that was
	// just created, so creation and deletion cannot drift apart.
	if !workdirs.IsAutoChat(wd) {
		t.Errorf("a freshly created chat folder must be recognized as an auto chat: %q", wd)
	}
}

// New chat: when the client explicitly chooses a working directory (existing
// folder or repo clone target), the auto per-chat folder is suppressed.
func TestHandleNewSession_ExplicitWorkdirSuppressesAutoFolder(t *testing.T) {
	setupTestDB(t)
	base := t.TempDir()
	config.Workspace = base
	chosen := filepath.Join(base, "my-existing-project")
	os.MkdirAll(chosen, 0o755)

	body := `{"label":"Work","workdir":"` + chosen + `"}`
	req := httptest.NewRequest("POST", "/api/sessions/new", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	HandleNewSession(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Config config.SessionConfig `json:"config"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Config.Workdir != chosen {
		t.Fatalf("expected chosen workdir %q to be respected, got %q", chosen, resp.Config.Workdir)
	}
}

func TestHandleSessions(t *testing.T) {
	setupTestDB(t)

	db.CreateSession("test-1", config.SessionConfig{Label: "Test 1", Mode: "autopilot"})
	db.CreateSession("test-2", config.SessionConfig{Label: "Test 2", Mode: "plan"})

	req := httptest.NewRequest("GET", "/api/sessions", nil)
	w := httptest.NewRecorder()
	HandleSessions(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var items []db.SessionListItem
	json.Unmarshal(w.Body.Bytes(), &items)
	if len(items) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(items))
	}
}

func TestHandleHistory(t *testing.T) {
	setupTestDB(t)

	db.CreateSession("hist-1", config.SessionConfig{Label: "History"})
	db.AddMessage("hist-1", "user", "Hello")
	db.AddMessage("hist-1", "assistant", "Hi!")

	req := httptest.NewRequest("GET", "/api/history?session_id=hist-1", nil)
	w := httptest.NewRecorder()
	HandleHistory(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var msgs []db.ChatMessage
	json.Unmarshal(w.Body.Bytes(), &msgs)
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages, got %d", len(msgs))
	}
}

func TestWriteSSE_IncludesEventField(t *testing.T) {
	tests := []struct {
		name      string
		data      map[string]any
		wantEvent string
	}{
		{
			name:      "chunk event",
			data:      map[string]any{"type": "chunk", "text": "Hello"},
			wantEvent: "chunk",
		},
		{
			name:      "tool_call event",
			data:      map[string]any{"type": "tool_call", "tool": "bash", "call_id": "c1"},
			wantEvent: "tool_call",
		},
		{
			name:      "tool_done event",
			data:      map[string]any{"type": "tool_done", "tool": "bash", "success": true},
			wantEvent: "tool_done",
		},
		{
			name:      "question event",
			data:      map[string]any{"type": "question", "question": "Continue?"},
			wantEvent: "question",
		},
		{
			name:      "usage event",
			data:      map[string]any{"type": "usage", "tokens_out": 42},
			wantEvent: "usage",
		},
		{
			name:      "done event",
			data:      map[string]any{"type": "done"},
			wantEvent: "done",
		},
		{
			name:      "error event",
			data:      map[string]any{"type": "error", "message": "something failed"},
			wantEvent: "error",
		},
		{
			name:      "session_id event",
			data:      map[string]any{"type": "session_id", "session_id": "abc-123"},
			wantEvent: "session_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeSSE(w, w, tt.data)

			body := w.Body.String()

			// Must contain "event: <type>\n"
			expectedEventLine := "event: " + tt.wantEvent + "\n"
			if !strings.Contains(body, expectedEventLine) {
				t.Errorf("SSE output missing event field.\nGot:\n%s\nWant line: %q", body, expectedEventLine)
			}

			// Must contain "data: " with valid JSON
			if !strings.Contains(body, "data: ") {
				t.Errorf("SSE output missing data field.\nGot:\n%s", body)
			}

			// Data must be valid JSON containing the type
			dataIdx := strings.Index(body, "data: ")
			dataLine := body[dataIdx+6 : strings.Index(body[dataIdx:], "\n")+dataIdx]
			var parsed map[string]any
			if err := json.Unmarshal([]byte(dataLine), &parsed); err != nil {
				t.Errorf("SSE data is not valid JSON: %v\nData: %s", err, dataLine)
			}
			if parsed["type"] != tt.wantEvent {
				t.Errorf("data.type = %q, want %q", parsed["type"], tt.wantEvent)
			}
		})
	}
}

func TestWriteSSE_NoTypeField(t *testing.T) {
	w := httptest.NewRecorder()
	data := map[string]any{"foo": "bar"}
	writeSSE(w, w, data)

	body := w.Body.String()

	// Should NOT have an event: line when type is missing
	if strings.Contains(body, "event:") {
		t.Errorf("SSE output should not have event field when type is missing.\nGot:\n%s", body)
	}

	// Should still have data
	if !strings.Contains(body, "data: ") {
		t.Errorf("SSE output missing data field.\nGot:\n%s", body)
	}
}

func TestWriteSSE_ChunkTextPreserved(t *testing.T) {
	w := httptest.NewRecorder()
	writeSSE(w, w, map[string]any{"type": "chunk", "text": "Hello world"})

	body := w.Body.String()
	var parsed map[string]any
	dataIdx := strings.Index(body, "data: ")
	dataLine := body[dataIdx+6 : strings.Index(body[dataIdx:], "\n")+dataIdx]
	json.Unmarshal([]byte(dataLine), &parsed)

	if parsed["text"] != "Hello world" {
		t.Errorf("chunk text = %q, want %q", parsed["text"], "Hello world")
	}
}

func TestHandleFavoriteSession_Toggles(t *testing.T) {
setupTestDB(t)

cfg := config.DefaultSessionConfig()
if err := db.CreateSession("sess-fav-1", cfg); err != nil {
t.Fatal(err)
}

call := func() map[string]any {
req := httptest.NewRequest("POST", "/api/sessions/sess-fav-1/favorite", nil)
req.SetPathValue("session_id", "sess-fav-1")
w := httptest.NewRecorder()
HandleFavoriteSession(w, req)
if w.Code != http.StatusOK {
t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
}
var resp map[string]any
json.Unmarshal(w.Body.Bytes(), &resp)
return resp
}

resp := call()
if resp["favorite"] != true {
t.Errorf("first toggle: favorite = %v, want true", resp["favorite"])
}
stored, _ := db.GetSessionConfig("sess-fav-1")
if !stored.Favorite {
t.Error("favorite flag not persisted to DB")
}

resp = call()
if resp["favorite"] != false {
t.Errorf("second toggle: favorite = %v, want false", resp["favorite"])
}
}

func TestHandleFavoriteSession_NotFound(t *testing.T) {
setupTestDB(t)
req := httptest.NewRequest("POST", "/api/sessions/missing/favorite", nil)
req.SetPathValue("session_id", "missing")
w := httptest.NewRecorder()
HandleFavoriteSession(w, req)
if w.Code != http.StatusNotFound {
t.Errorf("expected 404, got %d", w.Code)
}
}

func TestListSessions_FavoriteRoundTrip(t *testing.T) {
setupTestDB(t)

cfg := config.DefaultSessionConfig()
cfg.Favorite = true
if err := db.CreateSession("fav-list-1", cfg); err != nil {
t.Fatal(err)
}
cfg2 := config.DefaultSessionConfig()
if err := db.CreateSession("fav-list-2", cfg2); err != nil {
t.Fatal(err)
}

items := db.ListSessions()
var got1, got2 *db.SessionListItem
for i := range items {
if items[i].SessionID == "fav-list-1" {
got1 = &items[i]
}
if items[i].SessionID == "fav-list-2" {
got2 = &items[i]
}
}
if got1 == nil || !got1.Favorite {
t.Errorf("expected fav-list-1 to be favorite, got %+v", got1)
}
if got2 == nil || got2.Favorite {
t.Errorf("expected fav-list-2 to NOT be favorite, got %+v", got2)
}
}

// The history endpoint is what the browser reads when reopening a chat, so
// the per-message metadata the UI renders (how long ago, and what the turn
// cost) has to survive the round trip through JSON.
func TestHandleHistory_IncludesTimestampAndUsage(t *testing.T) {
	setupTestDB(t)

	db.CreateSession("hist-2", config.SessionConfig{Label: "History"})
	db.AddMessage("hist-2", "user", "Hello")
	db.AddMessageWithUsage("hist-2", "assistant", "Hi!", `{"tokens_out":926,"cost":0.24}`)

	req := httptest.NewRequest("GET", "/api/history?session_id=hist-2", nil)
	w := httptest.NewRecorder()
	HandleHistory(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Decode loosely: this asserts the wire shape the frontend actually reads,
	// not just that it round-trips through our own struct.
	var msgs []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &msgs); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	for i, m := range msgs {
		ts, _ := m["created_at"].(string)
		if _, err := time.Parse(time.RFC3339, ts); err != nil {
			t.Errorf("message %d created_at %q is not RFC3339: %v", i, ts, err)
		}
	}
	if _, ok := msgs[0]["usage"]; ok {
		t.Errorf("user message should carry no usage: %v", msgs[0])
	}
	usage, ok := msgs[1]["usage"].(map[string]any)
	if !ok {
		t.Fatalf("assistant message lost its usage: %v", msgs[1])
	}
	if usage["tokens_out"] != float64(926) || usage["cost"] != 0.24 {
		t.Errorf("usage = %v", usage)
	}
}

// Renaming is what marks a label as the user's own. Automatic titling must
// never overwrite it, so the endpoint distinguishes a name the user typed from
// the placeholder the client derives from the first prompt.
func TestHandleRenameSession_MarksManualUnlessAuto(t *testing.T) {
	setupTestDB(t)
	cfg := config.DefaultSessionConfig()
	if err := db.CreateSession("sess-rename-1", cfg); err != nil {
		t.Fatal(err)
	}

	rename := func(body string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/api/sessions/sess-rename-1/rename",
			strings.NewReader(body))
		req.SetPathValue("session_id", "sess-rename-1")
		w := httptest.NewRecorder()
		HandleRenameSession(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
	}

	// The client's placeholder: sets the label but leaves it auto-titleable.
	rename(`{"name":"add dark mode toggle to the","auto":true}`)
	got, err := db.GetSessionConfig("sess-rename-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != "add dark mode toggle to the" {
		t.Fatalf("label = %q", got.Label)
	}
	if got.LabelManual {
		t.Error("an automatic rename must not count as the user naming the chat")
	}

	// A name the user typed: protected from then on.
	rename(`{"name":"Dark mode work"}`)
	got, err = db.GetSessionConfig("sess-rename-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != "Dark mode work" {
		t.Fatalf("label = %q", got.Label)
	}
	if !got.LabelManual {
		t.Error("a deliberate rename must protect the label from auto-titling")
	}
}

// newChatWithAutoWorkdir creates a chat through the real handler and returns its
// id and the auto-created folder, so deletion is exercised against a workspace
// that was built the way production builds them.
func newChatWithAutoWorkdir(t *testing.T) (string, string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/sessions/new", strings.NewReader(`{"label":"Scratch"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	HandleNewSession(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create failed: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		SessionID string               `json:"session_id"`
		Config    config.SessionConfig `json:"config"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Config.Workdir == "" {
		t.Fatal("expected an auto workdir")
	}
	return resp.SessionID, resp.Config.Workdir
}

func deleteSession(t *testing.T, sid string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("DELETE", "/api/sessions/"+sid, nil)
	req.SetPathValue("session_id", sid)
	w := httptest.NewRecorder()
	HandleDeleteSession(w, req)
	return w
}

// Workspace hygiene: deleting a chat must take its auto-created folder with it. Leaving the
// folder behind stranded it permanently — with no session row it was invisible
// in the UI and unreachable by tidy-up.
func TestHandleDeleteSession_RemovesAutoWorkdir(t *testing.T) {
	setupTestDB(t)
	base := t.TempDir()
	config.Workspace = base

	sid, dir := newChatWithAutoWorkdir(t)
	if err := os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if w := deleteSession(t, sid); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if db.SessionExists(sid) {
		t.Error("expected the session row to be gone")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("expected auto workspace %s to be removed, stat err = %v", dir, err)
	}
}

// The mirror image, and the more important half: a directory the user chose is
// theirs. Deleting the chat that pointed at it must not touch it.
func TestHandleDeleteSession_KeepsUserChosenWorkdir(t *testing.T) {
	setupTestDB(t)
	base := t.TempDir()
	config.Workspace = base

	chosen := filepath.Join(base, "my-existing-project")
	if err := os.MkdirAll(chosen, 0o755); err != nil {
		t.Fatal(err)
	}
	keepMe := filepath.Join(chosen, "README.md")
	if err := os.WriteFile(keepMe, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	body := `{"label":"Work","workdir":"` + chosen + `"}`
	req := httptest.NewRequest("POST", "/api/sessions/new", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	HandleNewSession(w, req)
	var resp struct {
		SessionID string `json:"session_id"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if got := deleteSession(t, resp.SessionID); got.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", got.Code)
	}

	if _, err := os.Stat(keepMe); err != nil {
		t.Fatalf("user-chosen workspace was destroyed: %v", err)
	}
}

// Deleting an unknown session is a no-op that still succeeds, and must not go
// looking for a directory to remove on the strength of an empty config.
func TestHandleDeleteSession_UnknownSessionRemovesNothing(t *testing.T) {
	setupTestDB(t)
	base := t.TempDir()
	config.Workspace = base

	other := filepath.Join(base, "chat-aaaaaaaaaaaa")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}

	if w := deleteSession(t, "no-such-session"); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("an unrelated chat folder was removed: %v", err)
	}
}

// The opencode session id must be read while the row still exists. Reading it
// after the delete silently yields "", which would leave opencode's copy of the
// transcript on disk forever with no error anywhere to show for it — opencode
// has no retention of its own, so nothing else ever reclaims it.
func TestHandleDeleteSession_DeletesOpencodeSession(t *testing.T) {
	setupTestDB(t)
	base := t.TempDir()
	config.Workspace = base

	argsPath := filepath.Join(base, "args.txt")
	bin := filepath.Join(base, "opencode")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsPath + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	prevBin := config.OpencodeBin
	config.OpencodeBin = bin
	t.Cleanup(func() { config.OpencodeBin = prevBin })

	sid, _ := newChatWithAutoWorkdir(t)
	if err := db.SetOpencodeSession(sid, "ses_from_row"); err != nil {
		t.Fatal(err)
	}

	if w := deleteSession(t, sid); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(argsPath); err == nil {
			got := strings.Fields(string(raw))
			want := []string{"session", "delete", "ses_from_row"}
			if len(got) != len(want) {
				t.Fatalf("opencode args = %v, want %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("opencode args = %v, want %v", got, want)
				}
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("opencode session was never deleted")
}
