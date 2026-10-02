// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeserver

import (
	"reflect"
	"testing"
)

func TestPrepareModel(t *testing.T) {
	cases := []struct {
		in           string
		wantProvider string
		wantModel    string
	}{
		{"github-copilot/claude-sonnet-4.6", "github-copilot", "claude-sonnet-4.6"},
		{"anthropic/claude-3-5-sonnet-20241022", "anthropic", "claude-3-5-sonnet-20241022"},
		{"opencode/big-pickle", "opencode", "big-pickle"},
		{"", "", ""},
		{"nomodel", "", ""},        // no slash → fall back to default
		{"/leadingslash", "", ""},  // empty provider → default
		{"trailingslash/", "", ""}, // empty model → default
		{"a/b/c", "a", "b/c"},      // only split on the first slash
	}
	for _, c := range cases {
		p, m := PrepareModel(c.in)
		if p != c.wantProvider || m != c.wantModel {
			t.Errorf("PrepareModel(%q) = (%q,%q), want (%q,%q)", c.in, p, m, c.wantProvider, c.wantModel)
		}
	}
}

func ev(typ string, props map[string]any) ServerEvent {
	return ServerEvent{Type: typ, Properties: props}
}

func partEvent(part map[string]any, sid string) ServerEvent {
	return ev("message.part.updated", map[string]any{"sessionID": sid, "part": part})
}

func TestClassifyEvent_TextChunk(t *testing.T) {
	st := newTurnState()
	out, done := classifyEvent(partEvent(map[string]any{"type": "text", "text": "hello world"}, "ses_1"), st)
	if done {
		t.Fatal("text part should not signal done")
	}
	if len(out) != 1 || out[0].Kind != KindChunk || out[0].Text != "hello world" {
		t.Fatalf("got %+v", out)
	}
	if out[0].SessionID != "ses_1" {
		t.Errorf("sessionID not propagated: %q", out[0].SessionID)
	}
}

func TestClassifyEvent_EmptyTextIgnored(t *testing.T) {
	st := newTurnState()
	out, _ := classifyEvent(partEvent(map[string]any{"type": "text", "text": "   "}, "ses_1"), st)
	if len(out) != 0 {
		t.Fatalf("blank text should produce no events, got %+v", out)
	}
}

func TestClassifyEvent_StepFinishAccounting(t *testing.T) {
	st := newTurnState()
	part := map[string]any{
		"type": "step-finish",
		"cost": 0.0509565,
		"tokens": map[string]any{
			"input":     float64(3),
			"output":    float64(52),
			"reasoning": float64(8),
			// Prompt-cache tokens carry the bulk of the re-fed context and must
			// be counted as input.
			"cache": map[string]any{
				"read":  float64(1200),
				"write": float64(300),
			},
		},
	}
	out, _ := classifyEvent(partEvent(part, "ses_1"), st)
	if len(out) != 1 {
		t.Fatalf("want 1 event, got %d", len(out))
	}
	e := out[0]
	if e.Kind != KindStepFinish {
		t.Fatalf("kind = %v", e.Kind)
	}
	if e.TokensIn != 1503 { // 3 input + 1200 cache read + 300 cache write
		t.Errorf("tokensIn = %d, want 1503 (input + cache read/write)", e.TokensIn)
	}
	if e.TokensOut != 60 { // 52 output + 8 reasoning
		t.Errorf("tokensOut = %d, want 60", e.TokensOut)
	}
	if !e.TokensInKnown {
		t.Error("tokensInKnown should be true when a tokens object is present")
	}
	if e.Cost != 0.0509565 {
		t.Errorf("cost = %v", e.Cost)
	}
}

// A step-finish with no cache field still counts plain input tokens.
func TestClassifyEvent_StepFinishNoCache(t *testing.T) {
	st := newTurnState()
	part := map[string]any{
		"type": "step-finish",
		"tokens": map[string]any{
			"input":  float64(42),
			"output": float64(10),
		},
	}
	out, _ := classifyEvent(partEvent(part, "ses_1"), st)
	if len(out) != 1 {
		t.Fatalf("want 1 event, got %d", len(out))
	}
	if out[0].TokensIn != 42 {
		t.Errorf("tokensIn = %d, want 42", out[0].TokensIn)
	}
}

func TestClassifyEvent_ToolCompleted(t *testing.T) {
	st := newTurnState()
	part := map[string]any{
		"type":   "tool",
		"tool":   "bash",
		"callID": "call_123",
		"state": map[string]any{
			"status": "completed",
			"input":  map[string]any{"command": "pwd"},
			"output": "/tmp/work",
		},
	}
	out, _ := classifyEvent(partEvent(part, "ses_1"), st)
	if len(out) != 1 {
		t.Fatalf("want 1 event, got %d", len(out))
	}
	e := out[0]
	if e.Kind != KindToolDone {
		t.Fatalf("kind = %v, want tool_done", e.Kind)
	}
	if e.Tool != "bash" || e.CallID != "call_123" || !e.Success {
		t.Errorf("unexpected: %+v", e)
	}
	if e.Output != "/tmp/work" {
		t.Errorf("output = %q", e.Output)
	}
	if !reflect.DeepEqual(e.Args, map[string]any{"command": "pwd"}) {
		t.Errorf("args = %+v", e.Args)
	}
}

func TestClassifyEvent_ToolNonTerminalIgnored(t *testing.T) {
	st := newTurnState()
	for _, status := range []string{"pending", "running"} {
		part := map[string]any{
			"type":   "tool",
			"tool":   "bash",
			"callID": "call_x",
			"state":  map[string]any{"status": status},
		}
		out, _ := classifyEvent(partEvent(part, "ses_1"), st)
		if len(out) != 0 {
			t.Fatalf("status %q should emit no events, got %+v", status, out)
		}
	}
}

func TestClassifyEvent_TaskRunningEmitsLiveRow(t *testing.T) {
	// The `task` (subagent) tool emits a live tool_call the moment it reaches
	// `running`, so the UI shows the delegation immediately instead of only at
	// completion.
	st := newTurnState()
	part := map[string]any{
		"type":   "tool",
		"tool":   "task",
		"callID": "call_task_1",
		"state": map[string]any{
			"status": "running",
			"input":  map[string]any{"description": "Research a company"},
		},
	}
	out, _ := classifyEvent(partEvent(part, "ses_1"), st)
	if len(out) != 1 {
		t.Fatalf("want 1 event, got %d: %+v", len(out), out)
	}
	e := out[0]
	if e.Kind != KindToolCall || e.Tool != "task" || e.CallID != "call_task_1" {
		t.Fatalf("unexpected running task event: %+v", e)
	}
	if e.Args["description"] != "Research a company" {
		t.Errorf("args not carried through: %+v", e.Args)
	}

	// A non-task tool at running still stays quiet.
	bash := map[string]any{
		"type":   "tool",
		"tool":   "bash",
		"callID": "call_b",
		"state":  map[string]any{"status": "running"},
	}
	if out, _ := classifyEvent(partEvent(bash, "ses_1"), st); len(out) != 0 {
		t.Fatalf("non-task running tool should stay quiet, got %+v", out)
	}
}

func TestClassifyEvent_ToolPermissionDenied(t *testing.T) {
	st := newTurnState()
	part := map[string]any{
		"type":   "tool",
		"tool":   "bash",
		"callID": "call_9",
		"state": map[string]any{
			"status": "error",
			"error":  "The user rejected permission to use this specific tool call.",
		},
	}
	out, _ := classifyEvent(partEvent(part, "ses_1"), st)
	if len(out) != 1 || out[0].Kind != KindToolBlocked {
		t.Fatalf("want tool_blocked, got %+v", out)
	}
	if out[0].Success {
		t.Error("blocked tool should not be success")
	}
}

func TestClassifyEvent_PermissionAsked(t *testing.T) {
	st := newTurnState()
	props := map[string]any{
		"sessionID":  "ses_1",
		"id":         "per_abc",
		"permission": "bash",
		"patterns":   []any{"rm *", "git *"},
		"tool":       map[string]any{"messageID": "msg_1", "callID": "call_5"},
	}
	out, done := classifyEvent(ev("permission.asked", props), st)
	if done {
		t.Fatal("permission should not signal done")
	}
	if len(out) != 1 {
		t.Fatalf("want 1 event, got %d", len(out))
	}
	e := out[0]
	if e.Kind != KindPermission {
		t.Fatalf("kind = %v", e.Kind)
	}
	if e.PermissionID != "per_abc" || e.Permission != "bash" || e.CallID != "call_5" {
		t.Errorf("unexpected: %+v", e)
	}
	if !reflect.DeepEqual(e.Patterns, []string{"rm *", "git *"}) {
		t.Errorf("patterns = %+v", e.Patterns)
	}
}

func TestClassifyEvent_SessionIdleIsDone(t *testing.T) {
	st := newTurnState()
	out, done := classifyEvent(ev("session.idle", map[string]any{"sessionID": "ses_1"}), st)
	if !done {
		t.Fatal("session.idle must signal done")
	}
	if len(out) != 0 {
		t.Fatalf("session.idle should emit no events, got %+v", out)
	}
}

func TestClassifyEvent_SessionError(t *testing.T) {
	st := newTurnState()
	props := map[string]any{
		"sessionID": "ses_1",
		"error": map[string]any{
			"name": "ProviderAuthError",
			"data": map[string]any{"message": "not authenticated"},
		},
	}
	out, _ := classifyEvent(ev("session.error", props), st)
	if len(out) != 1 || out[0].Kind != KindError {
		t.Fatalf("want error event, got %+v", out)
	}
	if out[0].Text != "not authenticated" {
		t.Errorf("error text = %q, want the structured data.message", out[0].Text)
	}
}

func TestClassifyEvent_UnknownTypeIgnored(t *testing.T) {
	st := newTurnState()
	out, done := classifyEvent(ev("session.diff", map[string]any{"sessionID": "ses_1"}), st)
	if done || len(out) != 0 {
		t.Fatalf("unknown event should be a no-op, got out=%+v done=%v", out, done)
	}
}

func TestClassifyEvent_SuppressesUserEcho(t *testing.T) {
	st := newTurnState()

	// 1) message.updated tells us msg_user is the user's message.
	if out, _ := classifyEvent(ev("message.updated", map[string]any{
		"sessionID": "ses_1",
		"info":      map[string]any{"id": "msg_user", "role": "user"},
	}), st); len(out) != 0 {
		t.Fatalf("message.updated should emit nothing, got %+v", out)
	}

	// 2) The user's text part (the echoed prompt) must be suppressed.
	userPart := map[string]any{"type": "text", "text": "my prompt", "messageID": "msg_user"}
	if out, _ := classifyEvent(partEvent(userPart, "ses_1"), st); len(out) != 0 {
		t.Fatalf("user echo should be suppressed, got %+v", out)
	}

	// 3) The assistant's text part (different messageID) must pass through.
	asstPart := map[string]any{"type": "text", "text": "the answer", "messageID": "msg_asst"}
	out, _ := classifyEvent(partEvent(asstPart, "ses_1"), st)
	if len(out) != 1 || out[0].Text != "the answer" {
		t.Fatalf("assistant text should pass through, got %+v", out)
	}
}

func TestSessionScope_AdmitsDescendantsOnly(t *testing.T) {
	scope := newSessionScope("root")

	if !scope.contains("root") || !scope.isRoot("root") {
		t.Fatal("root session should be in scope and be the root")
	}
	// Unrelated session is out of scope.
	if scope.contains("ses_other") {
		t.Fatal("unrelated session should not be in scope")
	}
	// A subagent child (parentID == root) is admitted once observed.
	scope.observe(ev("session.updated", map[string]any{
		"info": map[string]any{"id": "child_1", "parentID": "root"},
	}))
	if !scope.contains("child_1") {
		t.Fatal("child session with root parent should be admitted")
	}
	if scope.isRoot("child_1") {
		t.Fatal("child session is not the root")
	}
	// A grandchild (parentID == child_1) is admitted transitively.
	scope.observe(ev("session.created", map[string]any{
		"info": map[string]any{"id": "child_2", "parentID": "child_1"},
	}))
	if !scope.contains("child_2") {
		t.Fatal("grandchild session should be admitted transitively")
	}
	// A session whose parent is unknown stays out of scope.
	scope.observe(ev("session.updated", map[string]any{
		"info": map[string]any{"id": "stray", "parentID": "someone_else"},
	}))
	if scope.contains("stray") {
		t.Fatal("session with unknown parent should not be admitted")
	}
}

func TestDemoteChildEvents(t *testing.T) {
	// Visible rows (chunk/tool) collapse to a single heartbeat re-tagged to root,
	// carrying the child's current tool as a progress label.
	in := []Event{
		{Kind: KindChunk, Text: "subagent thinking", SessionID: "child_1"},
		{Kind: KindToolDone, Tool: "bash", SessionID: "child_1"},
	}
	out := demoteChildEvents(in, "root")
	if len(out) != 1 || out[0].Kind != KindStep || out[0].SessionID != "root" {
		t.Fatalf("expected a single root-tagged heartbeat, got %+v", out)
	}
	if out[0].Tool != "bash" {
		t.Fatalf("heartbeat should carry the child tool label, got %q", out[0].Tool)
	}

	// Accounting and errors are preserved; a child KindResult is dropped.
	in = []Event{
		{Kind: KindStepFinish, TokensIn: 100, TokensOut: 5, TokensInKnown: true, Cost: 0.01},
		{Kind: KindError, Text: "subagent failed"},
		{Kind: KindResult},
	}
	out = demoteChildEvents(in, "root")
	if len(out) != 2 {
		t.Fatalf("want step_finish + error preserved, result dropped, got %+v", out)
	}
	if out[0].Kind != KindStepFinish || out[1].Kind != KindError {
		t.Fatalf("unexpected kinds: %+v", out)
	}

	// No input → no output (no spurious heartbeat).
	if got := demoteChildEvents(nil, "root"); len(got) != 0 {
		t.Fatalf("nil input should stay empty, got %+v", got)
	}
}

func TestIsPermissionDenied(t *testing.T) {
	yes := []string{
		"The user rejected permission to use this specific tool call.",
		"permission denied",
		"Error: rejected permission",
	}
	no := []string{"", "command failed", "file not found"}
	for _, m := range yes {
		if !isPermissionDenied(m) {
			t.Errorf("expected denied for %q", m)
		}
	}
	for _, m := range no {
		if isPermissionDenied(m) {
			t.Errorf("expected NOT denied for %q", m)
		}
	}
}
