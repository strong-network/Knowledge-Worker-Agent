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

// publishEvent removed — these tests focus on HTTP handler behaviour
// (response codes, JSON shape, SSE framing) without poking at Stream
// internals. Buffer mechanics are covered separately in
// internal/chat/stream_test.go.

func TestHandleActiveStreams_Empty(t *testing.T) {
	setupTestDB(t)
	clearStreams()

	req := httptest.NewRequest("GET", "/api/sessions/active", nil)
	w := httptest.NewRecorder()
	HandleActiveStreams(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Sessions []string `json:"sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Sessions) != 0 {
		t.Errorf("expected empty list, got %v", resp.Sessions)
	}
}

func TestHandleAttachStream_NoActiveStream(t *testing.T) {
	setupTestDB(t)
	clearStreams()

	sid := chat.NewUUID()
	if err := db.CreateSession(sid, config.DefaultSessionConfig()); err != nil {
		t.Fatalf("create session: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/sessions/"+sid+"/stream", nil)
	req.SetPathValue("session_id", sid)
	w := httptest.NewRecorder()
	HandleAttachStream(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("expected SSE content-type, got %q", got)
	}
	body := w.Body.String()
	// Should immediately emit session_id then done.
	if !strings.Contains(body, `"session_id"`) {
		t.Errorf("expected session_id frame in response, got:\n%s", body)
	}
	if !strings.Contains(body, `"type":"done"`) {
		t.Errorf("expected done frame in response, got:\n%s", body)
	}
}

func TestHandleAttachStream_UnknownSession(t *testing.T) {
	setupTestDB(t)

	req := httptest.NewRequest("GET", "/api/sessions/does-not-exist/stream", nil)
	req.SetPathValue("session_id", "does-not-exist")
	w := httptest.NewRecorder()
	HandleAttachStream(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleSessions_IncludesStreamingFlag(t *testing.T) {
	setupTestDB(t)
	clearStreams()

	sid := chat.NewUUID()
	if err := db.CreateSession(sid, config.DefaultSessionConfig()); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// No active stream → flag should be false.
	req := httptest.NewRequest("GET", "/api/sessions", nil)
	w := httptest.NewRecorder()
	HandleSessions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var list []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 session, got %d", len(list))
	}
	if v, ok := list[0]["streaming"]; !ok {
		t.Errorf("expected streaming key in response: %+v", list[0])
	} else if v != false {
		t.Errorf("expected streaming=false, got %v", v)
	}
	if list[0]["session_id"] != sid {
		t.Errorf("expected session_id=%q, got %v", sid, list[0]["session_id"])
	}
}

// clearStreams removes any leftover streams from prior tests in the package.
func clearStreams() {
	chat.Streams.Range(func(k, _ any) bool {
		chat.Streams.Delete(k)
		return true
	})
}

func TestHandleStopSession_KillsProcessAndKeepsSession(t *testing.T) {
	setupTestDB(t)
	clearStreams()

	sid := chat.NewUUID()
	if err := db.CreateSession(sid, config.DefaultSessionConfig()); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Register a fake in-flight process. Its Abort closure (invoked by
	// ActiveProcess.Kill) flips a flag so we can assert the turn was stopped.
	aborted := false
	chat.Procs.Store(sid, &chat.ActiveProcess{Abort: func() { aborted = true }})
	t.Cleanup(func() { chat.Procs.Delete(sid) })

	req := httptest.NewRequest("POST", "/api/sessions/"+sid+"/stop", nil)
	req.SetPathValue("session_id", sid)
	w := httptest.NewRecorder()
	HandleStopSession(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		OK      bool `json:"ok"`
		Stopped bool `json:"stopped"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK || !resp.Stopped {
		t.Errorf("expected ok=true stopped=true, got %+v", resp)
	}
	if !aborted {
		t.Error("expected the active process to be killed/aborted")
	}
	// The process must be removed from the registry after stopping.
	if _, ok := chat.Procs.Load(sid); ok {
		t.Error("expected process to be removed from registry")
	}
	// The session itself must still exist (stop != delete).
	if !db.SessionExists(sid) {
		t.Error("expected session to be preserved after stop")
	}
}

func TestHandleStopSession_NoActiveProcess(t *testing.T) {
	setupTestDB(t)
	clearStreams()

	sid := chat.NewUUID()
	if err := db.CreateSession(sid, config.DefaultSessionConfig()); err != nil {
		t.Fatalf("create session: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/sessions/"+sid+"/stop", nil)
	req.SetPathValue("session_id", sid)
	w := httptest.NewRecorder()
	HandleStopSession(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		OK      bool `json:"ok"`
		Stopped bool `json:"stopped"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK || resp.Stopped {
		t.Errorf("expected ok=true stopped=false when nothing was running, got %+v", resp)
	}
}
