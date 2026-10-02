// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessions

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

func newSessionForQueue(t *testing.T) string {
	t.Helper()
	sid := chat.NewUUID()
	if err := db.CreateSession(sid, config.DefaultSessionConfig()); err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(func() {
		chat.ClearQueue(sid)
		chat.Streams.Delete(sid)
	})
	return sid
}

func TestHandleQueueListEmpty(t *testing.T) {
	setupTestDB(t)
	sid := newSessionForQueue(t)

	req := httptest.NewRequest("GET", "/api/sessions/"+sid+"/queue", nil)
	req.SetPathValue("session_id", sid)
	w := httptest.NewRecorder()
	HandleQueueList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		SessionID string              `json:"session_id"`
		Queue     []chat.QueuedPrompt `json:"queue"`
		Streaming bool                `json:"streaming"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.SessionID != sid {
		t.Errorf("session_id=%q want %q", resp.SessionID, sid)
	}
	if resp.Queue == nil {
		t.Errorf("queue should be [] not null")
	}
	if len(resp.Queue) != 0 {
		t.Errorf("expected empty queue, got %v", resp.Queue)
	}
	if resp.Streaming {
		t.Errorf("expected streaming=false")
	}
}

func TestHandleQueueListUnknownSession(t *testing.T) {
	setupTestDB(t)
	req := httptest.NewRequest("GET", "/api/sessions/missing/queue", nil)
	req.SetPathValue("session_id", "missing")
	w := httptest.NewRecorder()
	HandleQueueList(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandleQueueAddImmediateStartReturnsLive(t *testing.T) {
	// When no stream is in flight, HandleQueueAdd starts a stream. We can't
	// run a real copilot binary here, so we register a fake live stream BEFORE
	// the request to force the queued path. That keeps the test hermetic.
	setupTestDB(t)
	sid := newSessionForQueue(t)
	makeLiveStream(t, sid)

	body := `{"prompt":"hello"}`
	req := httptest.NewRequest("POST", "/api/sessions/"+sid+"/queue", strings.NewReader(body))
	req.SetPathValue("session_id", sid)
	w := httptest.NewRecorder()
	HandleQueueAdd(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["queued"] != true {
		t.Errorf("expected queued=true, got %v", resp["queued"])
	}
	if _, ok := resp["id"].(string); !ok {
		t.Errorf("expected id in response, got %v", resp["id"])
	}
	if chat.QueueLen(sid) != 1 {
		t.Errorf("expected QueueLen=1, got %d", chat.QueueLen(sid))
	}
}

func TestHandleQueueAddEmptyPrompt(t *testing.T) {
	setupTestDB(t)
	sid := newSessionForQueue(t)
	req := httptest.NewRequest("POST", "/api/sessions/"+sid+"/queue", strings.NewReader(`{"prompt":"   "}`))
	req.SetPathValue("session_id", sid)
	w := httptest.NewRecorder()
	HandleQueueAdd(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleQueueAddInvalidJSON(t *testing.T) {
	setupTestDB(t)
	sid := newSessionForQueue(t)
	req := httptest.NewRequest("POST", "/api/sessions/"+sid+"/queue", strings.NewReader("not json"))
	req.SetPathValue("session_id", sid)
	w := httptest.NewRecorder()
	HandleQueueAdd(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleQueueDelete(t *testing.T) {
	setupTestDB(t)
	sid := newSessionForQueue(t)
	makeLiveStream(t, sid)

	a := chat.EnqueuePrompt(sid, "first")
	chat.EnqueuePrompt(sid, "second")

	req := httptest.NewRequest("DELETE", "/api/sessions/"+sid+"/queue/"+a.ID, nil)
	req.SetPathValue("session_id", sid)
	req.SetPathValue("id", a.ID)
	w := httptest.NewRecorder()
	HandleQueueDelete(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	if chat.QueueLen(sid) != 1 {
		t.Errorf("expected len=1, got %d", chat.QueueLen(sid))
	}

	// Delete same id again → 404
	req2 := httptest.NewRequest("DELETE", "/api/sessions/"+sid+"/queue/"+a.ID, nil)
	req2.SetPathValue("session_id", sid)
	req2.SetPathValue("id", a.ID)
	w2 := httptest.NewRecorder()
	HandleQueueDelete(w2, req2)
	if w2.Code != http.StatusNotFound {
		t.Errorf("expected 404 on double delete, got %d", w2.Code)
	}
}

func TestHandleQueueClear(t *testing.T) {
	setupTestDB(t)
	sid := newSessionForQueue(t)
	makeLiveStream(t, sid)

	chat.EnqueuePrompt(sid, "a")
	chat.EnqueuePrompt(sid, "b")

	req := httptest.NewRequest("DELETE", "/api/sessions/"+sid+"/queue", nil)
	req.SetPathValue("session_id", sid)
	w := httptest.NewRecorder()
	HandleQueueClear(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	if chat.QueueLen(sid) != 0 {
		t.Errorf("expected empty queue, got len=%d", chat.QueueLen(sid))
	}
}

func TestHandleQueueListReturnsItems(t *testing.T) {
	setupTestDB(t)
	sid := newSessionForQueue(t)
	makeLiveStream(t, sid)

	chat.EnqueuePrompt(sid, "one")
	chat.EnqueuePrompt(sid, "two")

	req := httptest.NewRequest("GET", "/api/sessions/"+sid+"/queue", nil)
	req.SetPathValue("session_id", sid)
	w := httptest.NewRecorder()
	HandleQueueList(w, req)

	var resp struct {
		Queue     []chat.QueuedPrompt `json:"queue"`
		Streaming bool                `json:"streaming"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Queue) != 2 || resp.Queue[0].Prompt != "one" || resp.Queue[1].Prompt != "two" {
		t.Errorf("unexpected queue: %+v", resp.Queue)
	}
	if !resp.Streaming {
		t.Errorf("expected streaming=true")
	}
}

func TestHandleChatQueuesWhenStreaming(t *testing.T) {
	setupTestDB(t)
	sid := newSessionForQueue(t)
	makeLiveStream(t, sid)

	body := `{"session_id":"` + sid + `","prompt":"hello there"}`
	req := httptest.NewRequest("POST", "/api/chat", strings.NewReader(body))
	w := httptest.NewRecorder()
	HandleChat(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"type":"queued"`) {
		t.Errorf("expected queued SSE event, body=%s", w.Body.String())
	}
	if chat.QueueLen(sid) != 1 {
		t.Errorf("expected queue len=1, got %d", chat.QueueLen(sid))
	}
}

// makeLiveStream registers a stream in chat.Streams whose Snapshot reports
// done=false, so chat.IsStreaming(sid) returns true. It does NOT spawn any
// process. Tests must not call Finish on the returned value (its notify
// channel is unset). Cleanup is handled by newSessionForQueue's t.Cleanup.
func makeLiveStream(t *testing.T, sid string) *chat.Stream {
	t.Helper()
	s := &chat.Stream{SessionID: sid}
	chat.Streams.Store(sid, s)
	return s
}
