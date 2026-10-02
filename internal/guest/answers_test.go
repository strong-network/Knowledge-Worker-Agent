// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package guest

import (
	"context"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// answered stands in for the opencode server and records what reached it.
func answered(t *testing.T) *[]string {
	t.Helper()
	var got []string
	prev := chat.RespondPermission
	chat.RespondPermission = func(_ context.Context, sid, id, resp string) (bool, error) {
		got = append(got, id+"="+resp)
		return true, nil
	}
	t.Cleanup(func() { chat.RespondPermission = prev })
	return &got
}

func waitForApproval(sid, id string) *chat.Stream {
	s := chat.OpenTurn(sid)
	s.Append(map[string]any{"type": "permission", "permission_id": id, "permission": "bash", "patterns": []any{"rm -rf build"}, "call_id": "c9"})
	return s
}

func TestPermissionFramesFollowTheToggle(t *testing.T) {
	e := newEnv(t)
	ev := map[string]any{"type": "permission", "permission_id": "p1", "permission": "bash", "patterns": []any{"make deploy"}, "call_id": "c3"}
	if out, _ := frameFor(e.shared)(ev); out["type"] != "waiting_on_owner" || out["permission"] != nil {
		t.Errorf("answers off: %v", out)
	}
	db.SetShareOptions(e.shared, db.ShareOptions{AllowPermissions: true})
	out, keep := frameFor(e.shared)(ev)
	if !keep || out["permission_id"] != "p1" || out["permission"] != "bash" || out["call_id"] != nil {
		t.Errorf("answers on: %v", out)
	}
	if out, _ := frameFor(e.shared)(map[string]any{"type": "usage", "cost": 1}); out != nil {
		t.Error("frameFor lets through what Frame drops")
	}
}

func TestGuestAnswersNeedTheToggleAndTheRightRequest(t *testing.T) {
	e := newEnv(t)
	got := answered(t)
	e.identify("Sarah")
	s := waitForApproval(e.shared, "per_1")
	path := "/guest/api/sessions/" + e.shared + "/permission"
	answer := func(id, resp string) int {
		code, _, _ := e.do("POST", path, `{"permission_id":"`+id+`","response":"`+resp+`"}`, sameOrigin)
		return code
	}
	if code := answer("per_1", "once"); code != 403 {
		t.Errorf("answer with the toggle off = %d, want 403", code)
	}
	if code, _, _ := e.do("GET", path, "", nil); code != 403 {
		t.Errorf("pending request with the toggle off = %d", code)
	}
	db.SetShareOptions(e.shared, db.ShareOptions{AllowPermissions: true})
	if code, body, _ := e.do("GET", path, "", nil); code != 200 || !strings.Contains(body, `"permission_id":"per_1"`) || strings.Contains(body, "c9") {
		t.Errorf("pending request = %d %s", code, body)
	}
	if code := answer("per_1", "always"); code != 400 {
		t.Errorf("always = %d; a guest must not make standing rules", code)
	}
	// The id goes into the opencode server's URL, so only the pending one passes.
	for _, id := range []string{"per_2", "../../session", ""} {
		if code := answer(id, "once"); code != 404 {
			t.Errorf("answer %q = %d, want 404", id, code)
		}
	}
	if len(*got) != 0 {
		t.Fatalf("reached the backend before a valid answer: %v", *got)
	}
	if code, _, _ := e.do("POST", path, `{"permission_id":"per_1","response":"once"}`, map[string]string{"Content-Type": "application/json"}); code != 403 {
		t.Errorf("answer without the share origin = %d", code)
	}
	if code := answer("per_1", "once"); code != 200 {
		t.Fatalf("answer = %d", code)
	}
	if len(*got) != 1 || (*got)[0] != "per_1=once" {
		t.Errorf("backend got %v", *got)
	}
	if code := answer("per_1", "reject"); code != 404 {
		t.Errorf("a second answer = %d, want 404", code)
	}
	if chat.WaitingOnOwner(e.shared) {
		t.Error("the chat still reads as waiting on the owner")
	}
	log, _ := s.Snapshot()
	last := log[len(log)-1]
	if last["type"] != "permission_answered" || last["author_name"] != "Sarah" {
		t.Errorf("the answer was not recorded on the turn: %v", last)
	}
	db.SetSessionShared(e.shared, false)
	if code := answer("per_1", "once"); code != 404 {
		t.Errorf("answer after sharing stopped = %d", code)
	}
}

// Two requests in one turn: answering the second leaves the first pending.
func TestPendingPermissionIsTheLatestUnanswered(t *testing.T) {
	e := newEnv(t)
	answered(t)
	s := waitForApproval(e.shared, "per_1")
	s.Append(map[string]any{"type": "permission", "permission_id": "per_2"})
	if ok, _ := chat.AnswerPermission(context.Background(), e.shared, "per_1", "once", chat.Author{}); ok {
		t.Error("answered a request that is not the one waiting")
	}
	if ok, _ := chat.AnswerPermission(context.Background(), e.shared, "per_2", "once", chat.Author{}); !ok {
		t.Fatal("could not answer the waiting request")
	}
	if p, ok := chat.PendingPermission(e.shared); !ok || p["permission_id"] != "per_1" {
		t.Errorf("pending after answering per_2 = %v %v", p, ok)
	}
}
