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

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

func temporaryRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions/new", HandleNewSession)
	mux.HandleFunc("GET /api/sessions", HandleSessions)
	mux.HandleFunc("PUT /api/sessions/{session_id}/temporary", HandleSetTemporary)
	mux.HandleFunc("POST /api/sessions/{session_id}/favorite", HandleFavoriteSession)
	mux.HandleFunc("POST /api/sessions/{session_id}/pin", HandlePinSession)
	mux.HandleFunc("POST /api/sessions/{session_id}/queue", HandleQueueAdd)
	mux.HandleFunc("POST /api/chat", HandleChat)
	return mux
}

func call(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	temporaryRoutes().ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	return w.Code, w.Body.String()
}

// newFolderChat makes a chat with its own folder, as New chat does, holding one file.
func newFolderChat(t *testing.T) (sid, folder string) {
	t.Helper()
	code, body := call(t, "POST", "/api/sessions/new", `{"label":"Draft reply"}`)
	if code != http.StatusOK {
		t.Fatalf("new chat = %d %s", code, body)
	}
	var resp struct {
		SessionID string               `json:"session_id"`
		Config    config.SessionConfig `json:"config"`
	}
	json.Unmarshal([]byte(body), &resp)
	if err := os.WriteFile(filepath.Join(resp.Config.Workdir, "notes.md"), []byte("draft"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { chat.Streams.Delete(resp.SessionID); chat.ClearQueue(resp.SessionID) })
	return resp.SessionID, resp.Config.Workdir
}

func setTemporary(t *testing.T, sid string, on bool) (int, temporaryView, string) {
	t.Helper()
	body := `{"temporary":false}`
	if on {
		body = `{"temporary":true}`
	}
	code, out := call(t, "PUT", "/api/sessions/"+sid+"/temporary", body)
	var v temporaryView
	json.Unmarshal([]byte(out), &v)
	return code, v, out
}

// idleFor puts the chat's last activity that long ago.
func idleFor(t *testing.T, sid string, d time.Duration) {
	t.Helper()
	at := time.Now().Add(-d).UTC().Format("2006-01-02 15:04:05")
	if _, err := db.DB.Exec(`UPDATE sessions SET activity_at = ? WHERE id = ?`, at, sid); err != nil {
		t.Fatal(err)
	}
}

func temporaryChatTest(t *testing.T) {
	t.Helper()
	setupTestDB(t)
	config.Workspace = t.TempDir()
}

// Making a chat temporary gives it a full day and takes it out of Starred and
// Pinned; keeping it saves it again. A temporary chat can't be starred.
func TestMakeTemporaryThenKeep(t *testing.T) {
	temporaryChatTest(t)
	sid, _ := newFolderChat(t)
	call(t, "POST", "/api/sessions/"+sid+"/favorite", "")
	call(t, "POST", "/api/sessions/"+sid+"/pin", "")

	code, v, body := setTemporary(t, sid, true)
	if code != http.StatusOK || !v.Temporary || v.ExpiresIn < 86390 || v.Favorite || v.Pinned {
		t.Fatalf("make temporary = %d %s", code, body)
	}
	_, list := call(t, "GET", "/api/sessions", "")
	if !strings.Contains(list, `"temporary":true`) {
		t.Errorf("the list doesn't say the chat is temporary: %s", list)
	}
	if code, body := call(t, "POST", "/api/sessions/"+sid+"/favorite", ""); code != http.StatusConflict {
		t.Errorf("starring a temporary chat = %d %s", code, body)
	}
	if code, body := call(t, "POST", "/api/sessions/"+sid+"/pin", ""); code != http.StatusConflict {
		t.Errorf("pinning a temporary chat = %d %s", code, body)
	}

	code, v, body = setTemporary(t, sid, false)
	if code != http.StatusOK || v.Temporary || v.ExpiresIn != 0 {
		t.Fatalf("keep = %d %s", code, body)
	}
	if code, _ := call(t, "POST", "/api/sessions/"+sid+"/favorite", ""); code != http.StatusOK {
		t.Errorf("starring a kept chat = %d", code)
	}
}

// A chat other people rely on can't be made temporary: not a shared chat, not
// a chat whose files are shared, and not a read-only one.
func TestMakeTemporaryIsRefusedForWhatOthersUse(t *testing.T) {
	temporaryChatTest(t)
	shared, _ := newFolderChat(t)
	db.DB.Exec(`UPDATE sessions SET shared_at = CURRENT_TIMESTAMP WHERE id = ?`, shared)
	withFiles, folder := newFolderChat(t)
	db.DB.Exec(`INSERT INTO shared_artifacts (id, folder, rel_path, session_id, port) VALUES ('a1', ?, 'notes.md', ?, 8770)`, folder, withFiles)
	readOnly, _ := newFolderChat(t)
	db.DB.Exec(`UPDATE sessions SET config = json_set(config, '$.backend', 'copilot') WHERE id = ?`, readOnly)

	for _, c := range []struct{ sid, want string }{
		{shared, "This chat is shared, so it can't be made temporary. Stop sharing first."},
		{withFiles, "Files from this chat are shared, so it can't be made temporary. Stop sharing them first."},
		{readOnly, "Read-only chats can't be made temporary."},
	} {
		code, _, body := setTemporary(t, c.sid, true)
		if code != http.StatusConflict || !strings.Contains(body, c.want) {
			t.Errorf("make temporary = %d %s, want 409 %q", code, body, c.want)
		}
		if cfg, _ := db.GetSessionConfig(c.sid); cfg.Temporary {
			t.Errorf("chat %s was made temporary anyway", c.sid)
		}
	}
	if code, _, _ := setTemporary(t, "no-such-chat", true); code != http.StatusNotFound {
		t.Errorf("unknown chat = %d", code)
	}
	if code, _ := call(t, "PUT", "/api/sessions/"+shared+"/temporary", `{}`); code != http.StatusBadRequest {
		t.Errorf("no value = %d", code)
	}
}

// After 24 hours without a message the chat, its folder and its messages are
// gone, and a message sent to it later is answered, not taken as a new chat.
// Saved chats and temporary chats with time left stay.
func TestSweepDeletesATemporaryChatAfterADay(t *testing.T) {
	temporaryChatTest(t)
	expired, folder := newFolderChat(t)
	db.AddUserMessage(expired, "draft a reply", "", "", "")
	setTemporary(t, expired, true)
	idleFor(t, expired, 24*time.Hour+time.Minute)
	nearly, _ := newFolderChat(t)
	setTemporary(t, nearly, true)
	idleFor(t, nearly, 23*time.Hour+59*time.Minute)
	saved, savedFolder := newFolderChat(t)
	idleFor(t, saved, 30*24*time.Hour)

	if n := SweepTemporary(time.Now()); n != 1 {
		t.Fatalf("swept %d chats, want 1", n)
	}
	if db.SessionExists(expired) || len(db.GetMessages(expired)) != 0 {
		t.Error("the expired chat or its messages are still there")
	}
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Errorf("the expired chat's folder is still there: %v", err)
	}
	if !db.SessionExists(nearly) || !db.SessionExists(saved) {
		t.Error("a chat with time left, or a saved chat, was deleted")
	}
	if _, err := os.Stat(savedFolder); err != nil {
		t.Errorf("the saved chat's folder went: %v", err)
	}

	for _, req := range []struct{ path, body string }{
		{"/api/chat", `{"session_id":"` + expired + `","prompt":"still there?"}`},
		{"/api/sessions/" + expired + "/queue", `{"prompt":"still there?"}`},
	} {
		code, body := call(t, "POST", req.path, req.body)
		if code != http.StatusGone || !strings.Contains(body, expiredMessage) {
			t.Errorf("POST %s after deletion = %d %s", req.path, code, body)
		}
	}
	if db.SessionExists(expired) {
		t.Error("a late message brought the chat back")
	}
	if code, _, body := setTemporary(t, expired, false); code != http.StatusNotFound || !strings.Contains(body, expiredMessage) {
		t.Errorf("keep after deletion = %d %s", code, body)
	}
}

// A running turn holds the deletion off, and its reply starts the day again.
func TestSweepWaitsForARunningTurn(t *testing.T) {
	temporaryChatTest(t)
	sid, _ := newFolderChat(t)
	setTemporary(t, sid, true)
	idleFor(t, sid, 25*time.Hour)

	turn := chat.OpenTurn(sid)
	if n := SweepTemporary(time.Now()); n != 0 || !db.SessionExists(sid) {
		t.Fatalf("a chat with a turn running was deleted (swept %d)", n)
	}
	db.AddTurnMessage(sid, "assistant", "Here's the reply.", "", "")
	turn.Finish()
	if n := SweepTemporary(time.Now()); n != 0 || !db.SessionExists(sid) {
		t.Fatalf("the chat was deleted right after its turn (swept %d)", n)
	}
	if left, _ := db.TemporaryExpiresIn(sid, time.Now()); left < 86000 {
		t.Errorf("%d s left after the turn, want about a day", left)
	}
}

// A message on its way counts before the sweeper looks, so the chat stays.
func TestAMessageKeepsTheChatFromTheSweeper(t *testing.T) {
	temporaryChatTest(t)
	sid, _ := newFolderChat(t)
	setTemporary(t, sid, true)
	idleFor(t, sid, 25*time.Hour)
	if !admitMessage(sid) {
		t.Fatal("a message to an existing chat was refused")
	}
	if n := SweepTemporary(time.Now()); n != 0 || !db.SessionExists(sid) {
		t.Errorf("the sweeper deleted a chat that had just got a message (swept %d)", n)
	}
}
