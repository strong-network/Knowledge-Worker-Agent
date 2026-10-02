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
)

func TestHandleQueueDeleteUnknownSession(t *testing.T) {
	setupTestDB(t)
	req := httptest.NewRequest("DELETE", "/api/sessions/missing/queue/abc", nil)
	req.SetPathValue("session_id", "missing")
	req.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	HandleQueueDelete(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandleQueueDeleteUnknownID(t *testing.T) {
	setupTestDB(t)
	sid := newSessionForQueue(t)
	makeLiveStream(t, sid)

	req := httptest.NewRequest("DELETE", "/api/sessions/"+sid+"/queue/never-existed", nil)
	req.SetPathValue("session_id", sid)
	req.SetPathValue("id", "never-existed")
	w := httptest.NewRecorder()
	HandleQueueDelete(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandleQueueClearUnknownSession(t *testing.T) {
	setupTestDB(t)
	req := httptest.NewRequest("DELETE", "/api/sessions/missing/queue", nil)
	req.SetPathValue("session_id", "missing")
	w := httptest.NewRecorder()
	HandleQueueClear(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandleQueueClearOnEmptyIsOK(t *testing.T) {
	setupTestDB(t)
	sid := newSessionForQueue(t)

	req := httptest.NewRequest("DELETE", "/api/sessions/"+sid+"/queue", nil)
	req.SetPathValue("session_id", sid)
	w := httptest.NewRecorder()
	HandleQueueClear(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["queue_len"] != float64(0) {
		t.Errorf("expected queue_len=0, got %v", resp["queue_len"])
	}
}

func TestHandleQueueAddUnknownSession(t *testing.T) {
	setupTestDB(t)
	req := httptest.NewRequest("POST", "/api/sessions/missing/queue", strings.NewReader(`{"prompt":"p"}`))
	req.SetPathValue("session_id", "missing")
	w := httptest.NewRecorder()
	HandleQueueAdd(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandleQueueDeletePreservesOthers(t *testing.T) {
	setupTestDB(t)
	sid := newSessionForQueue(t)
	makeLiveStream(t, sid)

	a := chat.EnqueuePrompt(sid, "first")
	b := chat.EnqueuePrompt(sid, "second")
	c := chat.EnqueuePrompt(sid, "third")
	_ = a
	_ = c

	req := httptest.NewRequest("DELETE", "/api/sessions/"+sid+"/queue/"+b.ID, nil)
	req.SetPathValue("session_id", sid)
	req.SetPathValue("id", b.ID)
	w := httptest.NewRecorder()
	HandleQueueDelete(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	got := chat.SnapshotQueue(sid)
	if len(got) != 2 || got[0].Prompt != "first" || got[1].Prompt != "third" {
		t.Errorf("expected [first,third], got %+v", got)
	}
}

func TestHandleChatNonStreamingProducesPrompt(t *testing.T) {
	// When there's no live stream, HandleChat would spawn a real copilot
	// process. We can't run the binary in tests, so we verify only that the
	// queued-path branch is selected when a live stream is registered.
	setupTestDB(t)
	sid := newSessionForQueue(t)
	makeLiveStream(t, sid)

	body := `{"session_id":"` + sid + `","prompt":"hello"}`
	req := httptest.NewRequest("POST", "/api/chat", strings.NewReader(body))
	w := httptest.NewRecorder()
	HandleChat(w, req)

	if !strings.Contains(w.Body.String(), `"queued"`) {
		t.Errorf("expected 'queued' in SSE body, got %s", w.Body.String())
	}
	if chat.QueueLen(sid) != 1 {
		t.Errorf("expected queue_len=1, got %d", chat.QueueLen(sid))
	}
}

func TestHandleChatBlankPromptRejected(t *testing.T) {
	setupTestDB(t)
	sid := newSessionForQueue(t)

	body := `{"session_id":"` + sid + `","prompt":"   "}`
	req := httptest.NewRequest("POST", "/api/chat", strings.NewReader(body))
	w := httptest.NewRecorder()
	HandleChat(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleChatInvalidJSONRejected(t *testing.T) {
	setupTestDB(t)
	req := httptest.NewRequest("POST", "/api/chat", strings.NewReader("not json"))
	w := httptest.NewRecorder()
	HandleChat(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleQueueListIncludesEnqueuedAt(t *testing.T) {
	setupTestDB(t)
	sid := newSessionForQueue(t)
	makeLiveStream(t, sid)
	chat.EnqueuePrompt(sid, "hello")

	req := httptest.NewRequest("GET", "/api/sessions/"+sid+"/queue", nil)
	req.SetPathValue("session_id", sid)
	w := httptest.NewRecorder()
	HandleQueueList(w, req)

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	queue, _ := resp["queue"].([]any)
	if len(queue) != 1 {
		t.Fatalf("expected 1 item, got %d", len(queue))
	}
	first := queue[0].(map[string]any)
	if _, ok := first["id"].(string); !ok {
		t.Errorf("missing id in queue item: %+v", first)
	}
	if first["prompt"] != "hello" {
		t.Errorf("prompt mismatch: %v", first["prompt"])
	}
	if _, ok := first["enqueued_at"].(string); !ok {
		t.Errorf("missing enqueued_at: %+v", first)
	}
}
