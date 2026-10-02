// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package workspaceheartbeat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// resetUIActivity keeps the package-level window from leaking between tests.
func resetUIActivity(t *testing.T) {
	t.Helper()
	lastUIActivity.Store(0)
	t.Cleanup(func() { lastUIActivity.Store(0) })
}

func TestUIActivityWindow(t *testing.T) {
	resetUIActivity(t)
	if UIActive() {
		t.Fatal("a workspace that never reported UI activity must not be active")
	}
	MarkUIActivity()
	if !UIActive() {
		t.Fatal("a fresh report must keep the workspace active")
	}
	lastUIActivity.Store(time.Now().Add(-uiActivityWindow - time.Second).UnixNano())
	if UIActive() {
		t.Fatal("a report older than the window must expire")
	}
}

func TestHandleActivityRecordsUse(t *testing.T) {
	resetUIActivity(t)
	w := httptest.NewRecorder()
	HandleActivity(w, httptest.NewRequest(http.MethodPost, "/api/activity", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
	if !UIActive() {
		t.Fatal("the request itself is the signal; it must mark the workspace active")
	}
}

// A user reading or typing keeps the workspace alive even though no chat turn
// is running — the gap the task-only signal left open.
func TestHeartbeatReportsUIActivityWithoutTask(t *testing.T) {
	resetUIActivity(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	MarkUIActivity()
	reported := make(chan bool, 1)
	r := socketReporter(t, func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			WasActive bool `json:"wasActive"`
		}
		json.NewDecoder(req.Body).Decode(&body)
		select {
		case reported <- body.WasActive:
			cancel()
		default:
		}
		w.Write([]byte("{}"))
	})
	r.run(ctx, func() bool { return false })
	select {
	case wasActive := <-reported:
		if !wasActive {
			t.Fatal("recent UI activity must report wasActive=true with no task running")
		}
	default:
		t.Fatal("no heartbeat was sent")
	}
}
