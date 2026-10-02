// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package guest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// Every value a guest must never see carries this marker.
const secret = "SECRET-7f3a"

func frames() map[string]map[string]any {
	return map[string]map[string]any{
		"session_id":          {"type": "session_id", "session_id": "s1"},
		"title":               {"type": "title", "title": "Plan the launch"},
		"step":                {"type": "step"},
		"chunk":               {"type": "chunk", "text": "hello"},
		"done":                {"type": "done", "queue_len": 0},
		"question":            {"type": "question", "question": "Which one?", "choices": []any{"a", "b"}},
		"tool_call":           {"type": "tool_call", "call_id": "c1", "tool": "bash", "args": `{"command":"cat ~/.ssh/id_rsa ` + secret + `"}`, "mcp_server": secret},
		"tool_done":           {"type": "tool_done", "call_id": "c1", "tool": "bash", "success": true, "output": "-----BEGIN KEY----- " + secret, "mcp_server": secret},
		"tool_blocked":        {"type": "tool_blocked", "call_id": "c2", "tool": "bash", "args": `{"command":"rm -rf ` + secret + `"}`, "reason": secret, "mcp_server": secret},
		"permission":          {"type": "permission", "permission_id": "p1", "permission": "bash " + secret, "patterns": []any{secret}, "call_id": "c3"},
		"permission_answered": {"type": "permission_answered", "permission_id": "p1", "response": "once", "author_id": nil, "author_name": nil},
		"mcp_auth_required":   {"type": "mcp_auth_required", "server": "atlassian-" + secret},
		"error":               {"type": "error", "message": "open /home/developer/" + secret + ": denied"},
		"usage":               {"type": "usage", "cost": 1.23, "tokens_in": 9000, "note": secret},
	}
}

func TestFrameNeverLetsASecretThrough(t *testing.T) {
	for name, ev := range frames() {
		out, keep := Frame(ev)
		if !keep {
			continue
		}
		b, _ := json.Marshal(out)
		if strings.Contains(string(b), secret) {
			t.Errorf("%s: leaked to guests: %s", name, b)
		}
	}
}

func TestFrameDecisions(t *testing.T) {
	f := frames()
	for _, name := range []string{"session_id", "title", "step", "chunk", "done", "question"} {
		out, keep := Frame(f[name])
		if !keep || !reflect.DeepEqual(out, f[name]) {
			t.Errorf("%s should pass unchanged, got keep=%v %v", name, keep, out)
		}
	}
	if out, _ := Frame(f["tool_call"]); out["tool"] != "bash" || out["call_id"] != "c1" || out["args"] != nil {
		t.Errorf("tool_call should keep name and id, drop args: %v", out)
	}
	if out, _ := Frame(f["tool_done"]); out["success"] != true || out["output"] != nil {
		t.Errorf("tool_done should keep status, drop output: %v", out)
	}
	if out, _ := Frame(f["permission"]); out["type"] != "waiting_on_owner" || out["reason"] != "approval" {
		t.Errorf("permission should become waiting_on_owner/approval: %v", out)
	}
	if out, _ := Frame(f["mcp_auth_required"]); out["type"] != "waiting_on_owner" || out["reason"] != "connector" {
		t.Errorf("mcp_auth_required should become waiting_on_owner/connector: %v", out)
	}
	if out, _ := Frame(f["error"]); out["message"] != errorForGuests {
		t.Errorf("error should carry the generic text: %v", out)
	}
	if _, keep := Frame(f["usage"]); keep {
		t.Error("usage reached guests")
	}
	if _, keep := Frame(map[string]any{"type": "brand_new_frame", "x": secret}); keep {
		t.Error("an undecided frame type reached guests")
	}
}

// The plan is the one tool whose arguments guests see, because they are it.
func TestFrameKeepsThePlan(t *testing.T) {
	plan := `{"todos":[{"content":"Draft","status":"in_progress"}]}`
	for _, tool := range []string{"todowrite", "todo_write", "TodoWrite"} {
		out, keep := Frame(map[string]any{"type": "tool_call", "call_id": "opencode-todo", "tool": tool, "args": plan})
		if !keep || out["args"] != plan {
			t.Errorf("%s: plan not forwarded: %v", tool, out)
		}
	}
}

// The frame map is shared with every other viewer of the turn, the owner
// included; filtering one guest's copy must not strip the owner's.
func TestFrameDoesNotMutateTheSharedFrame(t *testing.T) {
	for name, ev := range frames() {
		before, _ := json.Marshal(ev)
		Frame(ev)
		after, _ := json.Marshal(ev)
		if string(before) != string(after) {
			t.Errorf("%s: shared frame was modified", name)
		}
	}
}

// Guards the allow-list against new frame types: every "type" the chat package
// emits must be decided here, so a new one is a deliberate choice, not a silent
// default.
func TestEveryEmittedFrameTypeIsDecided(t *testing.T) {
	files, _ := filepath.Glob("../chat/*.go")
	re := regexp.MustCompile(`"type":\s*"([a-z_]+)"`)
	emitted := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			emitted[m[1]] = true
		}
	}
	if len(emitted) < 10 {
		t.Fatalf("found only %d frame types; the scan is broken", len(emitted))
	}
	decided := frames()
	var undecided []string
	for typ := range emitted {
		if _, ok := decided[typ]; !ok {
			undecided = append(undecided, typ)
		}
	}
	sort.Strings(undecided)
	if len(undecided) > 0 {
		t.Errorf("frame types emitted by internal/chat with no guest decision: %v — add each to Frame and to frames() here", undecided)
	}
}

func TestHistoryStripsUsageAndKeepsAuthorsAndTurns(t *testing.T) {
	in := []db.ChatMessage{
		{Role: "user", Content: "hi", AuthorID: "g-1", AuthorName: "Sarah", TurnID: "t-1"},
		{Role: "assistant", Content: "hello", Usage: json.RawMessage(`{"cost":1.5,"note":"` + secret + `"}`)},
	}
	b, _ := json.Marshal(History(in))
	if strings.Contains(string(b), secret) || strings.Contains(string(b), "usage") {
		t.Errorf("usage reached guest history: %s", b)
	}
	if !strings.Contains(string(b), `"author_name":"Sarah"`) || !strings.Contains(string(b), `"turn_id":"t-1"`) {
		t.Errorf("author or turn dropped: %s", b)
	}
	if b, _ := json.Marshal(History(nil)); string(b) != "[]" {
		t.Errorf("empty history encodes as %s, want []", b)
	}
}
