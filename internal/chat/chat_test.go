// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

func TestParseOpencodeJSONL_Text(t *testing.T) {
	line := `{"type":"text","timestamp":1,"sessionID":"oc-session","part":{"type":"text","text":"Hello from OpenCode","time":{"end":1}}}`
	ev := ParseOpencodeJSONL(line)
	if ev == nil {
		t.Fatal("expected event")
	}
	if ev.Kind != "chunk" {
		t.Errorf("expected chunk event, got %q", ev.Kind)
	}
	if ev.Text != "Hello from OpenCode" {
		t.Errorf("unexpected text: %q", ev.Text)
	}
	if ev.SessionID != "oc-session" {
		t.Errorf("expected session id, got %q", ev.SessionID)
	}
}

func TestParseOpencodeJSONL_LongText(t *testing.T) {
	// A single text event can carry a very large payload (a big generated
	// response, pasted file, etc.). Parsing must preserve it in full — the
	// assistant text stream is never truncated.
	for _, size := range []int{100_000, 3_000_000} {
		body := strings.Repeat("A", size)
		payload := map[string]any{
			"type":      "text",
			"sessionID": "oc-session",
			"part":      map[string]any{"type": "text", "text": body, "time": map[string]any{"end": 1}},
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		ev := ParseOpencodeJSONL(string(raw))
		if ev == nil {
			t.Fatalf("size=%d: expected event", size)
		}
		if ev.Kind != "chunk" {
			t.Errorf("size=%d: expected chunk, got %q", size, ev.Kind)
		}
		if len(ev.Text) != size {
			t.Errorf("size=%d: expected full text preserved, got %d chars", size, len(ev.Text))
		}
	}
}

func TestParseOpencodeJSONL_LongToolOutputTruncated(t *testing.T) {
	// Tool output IS intentionally capped (to keep large command/file output
	// off the UI + out of history); assert the documented 2000-char limit so
	// the cap doesn't silently change. Parsing must not choke on the size.
	body := strings.Repeat("x", 500_000)
	payload := map[string]any{
		"type":      "tool_use",
		"sessionID": "oc-session",
		"part": map[string]any{
			"type":   "tool",
			"tool":   "bash",
			"callID": "c1",
			"id":     "p1",
			"state":  map[string]any{"status": "completed", "output": body},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ev := ParseOpencodeJSONL(string(raw))
	if ev == nil {
		t.Fatal("expected event")
	}
	if ev.Kind != "tool_done" {
		t.Errorf("expected tool_done, got %q", ev.Kind)
	}
	if len(ev.Output) != 2000 {
		t.Errorf("expected tool output capped at 2000 chars, got %d", len(ev.Output))
	}
}

func TestExtractContentText_LongParts(t *testing.T) {
	// extractContentText concatenates array parts; verify it handles large
	// parts and joins them fully (used by the text chunk path).
	a := strings.Repeat("a", 1_000_000)
	b := strings.Repeat("b", 2_000_000)
	parts := []any{
		map[string]any{"text": a},
		map[string]any{"content": b},
	}
	got := extractContentText(parts)
	if len(got) != len(a)+len(b) {
		t.Errorf("expected joined length %d, got %d", len(a)+len(b), len(got))
	}
	if !strings.HasPrefix(got, "aaaa") || !strings.HasSuffix(got, "bbbb") {
		t.Error("expected parts joined in order")
	}
}

func TestTruncate_LongInput(t *testing.T) {
	// truncate is used for log lines; it must cut a huge string to the first
	// `max` bytes plus an ellipsis marker, without panicking.
	long := strings.Repeat("z", 5_000_000)
	got := truncate(long, 500)
	if !strings.HasPrefix(got, strings.Repeat("z", 500)) {
		t.Errorf("expected 500-char 'z' prefix, got len %d", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("expected ellipsis suffix on truncated string")
	}
	// A short string must pass through unchanged.
	if truncate("hello", 500) != "hello" {
		t.Error("short string should be unchanged")
	}
}


func TestParseOpencodeJSONL_ToolUse(t *testing.T) {
	line := `{"type":"tool_use","sessionID":"oc-session","part":{"id":"tool-1","type":"tool","tool":"bash","input":{"command":"pwd"},"state":{"status":"completed","output":"/tmp\n"}}}`
	ev := ParseOpencodeJSONL(line)
	if ev == nil {
		t.Fatal("expected event")
	}
	if ev.Kind != "tool_done" {
		t.Errorf("expected tool_done event, got %q", ev.Kind)
	}
	if ev.Tool != "bash" || ev.CallID != "tool-1" {
		t.Errorf("unexpected tool event: %+v", ev)
	}
	if !ev.Success {
		t.Error("expected success=true")
	}
	if ev.Output != "/tmp\n" {
		t.Errorf("unexpected output: %q", ev.Output)
	}
}

func TestParseOpencodeJSONL_PermissionBlocked(t *testing.T) {
	// Captured from a real `opencode run --format json`: an "ask" permission
	// (external_directory) is auto-rejected in headless mode and comes back as
	// a tool error with a "rejected permission" message. We surface this as a
	// distinct tool_blocked event.
	line := `{"type":"tool_use","sessionID":"oc-session","part":{"type":"tool","tool":"read","callID":"call_abc","state":{"status":"error","input":{"filePath":"/etc/hostname"},"error":"The user rejected permission to use this specific tool call."},"id":"prt_1"}}`
	ev := ParseOpencodeJSONL(line)
	if ev == nil {
		t.Fatal("expected event")
	}
	if ev.Kind != "tool_blocked" {
		t.Fatalf("expected tool_blocked, got %q", ev.Kind)
	}
	// CallID resolution mirrors tool_done: strOr(part, "id", "callID", ...)
	// prefers the part "id". The synthetic tool_call emitted alongside uses the
	// same logic, so the two correlate in the UI regardless.
	if ev.Tool != "read" || ev.CallID != "prt_1" {
		t.Errorf("unexpected tool/callID: %+v", ev)
	}
	if ev.Args == nil || ev.Args["filePath"] != "/etc/hostname" {
		t.Errorf("expected args.filePath from state.input, got %+v", ev.Args)
	}
	if !strings.Contains(ev.Output, "rejected permission") {
		t.Errorf("expected reason in output, got %q", ev.Output)
	}
}

func TestParseOpencodeJSONL_ToolErrorNotBlocked(t *testing.T) {
	// A genuine tool error (not a permission rejection) must stay tool_done
	// with success=false, not be reclassified as blocked.
	line := `{"type":"tool_use","sessionID":"oc-session","part":{"type":"tool","tool":"bash","callID":"c1","state":{"status":"error","error":"command not found: frobnicate"},"id":"p1"}}`
	ev := ParseOpencodeJSONL(line)
	if ev == nil {
		t.Fatal("expected event")
	}
	if ev.Kind != "tool_done" {
		t.Errorf("expected tool_done for a non-permission error, got %q", ev.Kind)
	}
	if ev.Success {
		t.Error("expected success=false")
	}
}

func TestIsPermissionDenied(t *testing.T) {
	cases := map[string]bool{
		"The user rejected permission to use this specific tool call.": true,
		"permission denied":            true,
		"Permission to use bash was rejected": true,
		"command not found":            false,
		"":                             false,
		"file not found: /etc/hostname": false,
	}
	for msg, want := range cases {
		if got := isPermissionDenied(msg); got != want {
			t.Errorf("isPermissionDenied(%q) = %v, want %v", msg, got, want)
		}
	}
}

func TestParseOpencodeJSONL_Error(t *testing.T) {
	line := `{"type":"error","sessionID":"oc-session","error":{"name":"ProviderError","data":{"message":"missing auth"}}}`
	ev := ParseOpencodeJSONL(line)
	if ev == nil {
		t.Fatal("expected event")
	}
	if ev.Kind != "error" || ev.Text != "missing auth" {
		t.Errorf("unexpected error event: %+v", ev)
	}
}

func TestParseOpencodeJSONL_StepStart(t *testing.T) {
	// step_start arrives well before the final assistant text and gives the
	// UI an early "agent is working" signal. We translate it to a Kind:"step"
	// event with the session id preserved.
	line := `{"type":"step_start","sessionID":"oc-session","part":{"type":"step-start"}}`
	ev := ParseOpencodeJSONL(line)
	if ev == nil {
		t.Fatal("expected event for step_start")
	}
	if ev.Kind != "step" {
		t.Errorf("expected Kind=step, got %q", ev.Kind)
	}
	if ev.SessionID != "oc-session" {
		t.Errorf("expected SessionID=oc-session, got %q", ev.SessionID)
	}
}

func TestParseOpencodeJSONL_StepFinishTokens(t *testing.T) {
	// step_finish carries token accounting. Reasoning tokens fold into output;
	// prompt-cache read+write tokens fold into input (for caching models the
	// re-fed context is billed as cache, not plain "input").
	line := `{"type":"step_finish","sessionID":"oc-session","part":{"type":"step-finish","reason":"stop","tokens":{"total":8114,"input":5978,"output":5,"reasoning":2,"cache":{"write":100,"read":2029}},"cost":0.01502}}`
	ev := ParseOpencodeJSONL(line)
	if ev == nil {
		t.Fatal("expected event for step_finish")
	}
	if ev.Kind != "step_finish" {
		t.Errorf("expected Kind=step_finish, got %q", ev.Kind)
	}
	if ev.TokensIn != 8107 { // 5978 input + 2029 cache.read + 100 cache.write
		t.Errorf("expected TokensIn=8107 (input+cache), got %d", ev.TokensIn)
	}
	if ev.TokensOut != 7 { // 5 output + 2 reasoning
		t.Errorf("expected TokensOut=7 (output+reasoning), got %d", ev.TokensOut)
	}
	if !ev.TokensInKnown {
		t.Errorf("expected TokensInKnown=true")
	}
	if ev.Cost < 0.015 || ev.Cost > 0.0151 {
		t.Errorf("expected Cost≈0.01502, got %v", ev.Cost)
	}
}

func TestParseOpencodeJSONL_StepFinishCacheDominatesInput(t *testing.T) {
	// Regression for the "2 input tokens for a multi-dollar turn" bug: a deep
	// conversation where almost all input is cache.read, with input=2.
	line := `{"type":"step_finish","sessionID":"oc","part":{"type":"step-finish","tokens":{"total":769349,"input":2,"output":571,"reasoning":0,"cache":{"write":1136,"read":767640}},"cost":0.405205}}`
	ev := ParseOpencodeJSONL(line)
	if ev == nil {
		t.Fatal("expected event")
	}
	// 2 + 767640 + 1136 = 768778 (not 2).
	if ev.TokensIn != 768778 {
		t.Errorf("expected TokensIn=768778 (input+cache), got %d", ev.TokensIn)
	}
	if ev.TokensOut != 571 {
		t.Errorf("expected TokensOut=571, got %d", ev.TokensOut)
	}
}

func TestRunStreamOpencodeFakeBinary(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "test.db")
	if err := db.Init(dbPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	argsPath := filepath.Join(tmp, "args.txt")
	bin := filepath.Join(tmp, "opencode")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsPath + "\nprintf '%s\\n' '{\"type\":\"text\",\"timestamp\":1,\"sessionID\":\"oc-fake\",\"part\":{\"type\":\"text\",\"text\":\"Hello from fake OpenCode\",\"time\":{\"end\":1}}}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	// Pin the installer to skip-update mode so ensureOpencodeAvailable
	// short-circuits when the fake binary verifies (it does — the script
	// exits 0 on `--version`). Without this, Ensure() would try to download
	// the real opencode release because the fake doesn't print a semver.
	t.Setenv("KWA_OPENCODE_AUTO_UPDATE", "false")
	t.Setenv("KWA_OPENCODE_INSTALL_DIR", filepath.Join(tmp, "install"))
	prevOpencodeEnsured := opencodeEnsured
	opencodeEnsured = false
	t.Cleanup(func() { opencodeEnsured = prevOpencodeEnsured })

	config.Workspace = tmp
	config.OpencodeBin = bin
	cfg := config.SessionConfig{
		Backend: config.BackendOpencode,
		Workdir: tmp,
		Model:   "anthropic/claude-sonnet-4-5",
		Yolo:    true,
	}
	if err := db.CreateSession("oc-test", cfg); err != nil {
		t.Fatal(err)
	}

	events, proc := RunStream("oc-test", "hello", cfg)
	if proc == nil {
		t.Fatal("expected active process")
	}

	var sawChunk, sawResult bool
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				if !sawChunk || !sawResult {
					t.Fatalf("expected chunk and result, sawChunk=%v sawResult=%v", sawChunk, sawResult)
				}
				argsBytes, err := os.ReadFile(argsPath)
				if err != nil {
					t.Fatal(err)
				}
				args := string(argsBytes)
				for _, want := range []string{"run", "--format", "json", "--dangerously-skip-permissions", "--model", "anthropic/claude-sonnet-4-5", "--dir", tmp, "--", "hello"} {
					if !strings.Contains(args, want) {
						t.Fatalf("expected fake opencode args to contain %q, got:\n%s", want, args)
					}
				}
				return
			}
			if ev.Kind == "chunk" && ev.Text == "Hello from fake OpenCode" {
				sawChunk = true
			}
			if ev.Kind == "result" && ev.SessionID == "oc-fake" {
				sawResult = true
			}
		case <-timeout:
			t.Fatal("timed out waiting for fake opencode stream")
		}
	}
}

func TestRunStreamOpencodeHandlesHugeLine(t *testing.T) {
	// Regression: a single JSONL line larger than bufio.Scanner's max token
	// size used to kill the stream with "bufio.Scanner: token too long".
	// The reader now grows without a hard per-line cap, so a multi-MB line
	// (e.g. a big tool result or file content) must stream through and the
	// turn must still complete with a result event.
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "test.db")
	if err := db.Init(dbPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	// Fake opencode emits a ~3MB text line, then a normal one. The big line
	// is built in-shell so we don't embed megabytes in the test source.
	// `yes A | head -c N` yields "A\n" repeated, so tr -d '\n' leaves ~N/2
	// characters — use 6MB of raw bytes for ~3M characters (well past the
	// old 1MB scanner cap).
	bin := filepath.Join(tmp, "opencode")
	script := "#!/bin/sh\n" +
		"BIG=$(yes A | head -c 6000000 | tr -d '\\n')\n" +
		"printf '{\"type\":\"text\",\"sessionID\":\"oc-fake\",\"part\":{\"type\":\"text\",\"text\":\"%s\",\"time\":{\"end\":1}}}\\n' \"$BIG\"\n" +
		"printf '%s\\n' '{\"type\":\"text\",\"sessionID\":\"oc-fake\",\"part\":{\"type\":\"text\",\"text\":\"tail\",\"time\":{\"end\":1}}}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("KWA_OPENCODE_AUTO_UPDATE", "false")
	t.Setenv("KWA_OPENCODE_INSTALL_DIR", filepath.Join(tmp, "install"))
	prevOpencodeEnsured := opencodeEnsured
	opencodeEnsured = false
	t.Cleanup(func() { opencodeEnsured = prevOpencodeEnsured })

	config.Workspace = tmp
	config.OpencodeBin = bin
	cfg := config.SessionConfig{Backend: config.BackendOpencode, Workdir: tmp, Yolo: true}
	if err := db.CreateSession("oc-huge", cfg); err != nil {
		t.Fatal(err)
	}

	events, proc := RunStream("oc-huge", "hello", cfg)
	if proc == nil {
		t.Fatal("expected active process")
	}

	var bigChunkLen int
	sawTail := false
	sawResult := false
	timeout := time.After(10 * time.Second)
	for done := false; !done; {
		select {
		case ev, ok := <-events:
			if !ok {
				done = true
				break
			}
			if ev.Kind == "chunk" {
				if len(ev.Text) > bigChunkLen {
					bigChunkLen = len(ev.Text)
				}
				if ev.Text == "tail" {
					sawTail = true
				}
			}
			if ev.Kind == "result" {
				sawResult = true
			}
		case <-timeout:
			t.Fatal("timed out waiting for fake opencode stream")
		}
	}

	// The big line must have been delivered intact (well past the old 1MB
	// scanner cap), and the line AFTER it must also arrive — proving the
	// stream didn't die on the oversized line.
	if bigChunkLen < 1_500_000 {
		t.Errorf("expected the multi-MB chunk to stream through, got max chunk len %d", bigChunkLen)
	}
	if !sawTail {
		t.Error("expected the line after the huge line to be processed (stream must not die on the big line)")
	}
	if !sawResult {
		t.Error("expected a result event (turn completed)")
	}
}

func TestParseOpencodeJSONL_TodoFromStateInput(t *testing.T) {
	// Real opencode todowrite shape: the todo list lives under
	// part.state.input.todos (part.input is absent), and the event is
	// reported as a completed tool_use.
	line := `{"type":"tool_use","sessionID":"oc-session","part":{"id":"prt_1","type":"tool","tool":"todowrite","callID":"toolu_abc","state":{"status":"completed","input":{"todos":[{"content":"step one","status":"completed","priority":"medium"},{"content":"step two","status":"in_progress","priority":"medium"},{"content":"step three","status":"pending","priority":"medium"}]},"output":"3 todos"}}}`
	ev := ParseOpencodeJSONL(line)
	if ev == nil {
		t.Fatal("expected event for todowrite")
	}
	if ev.Tool != "todowrite" {
		t.Errorf("expected Tool=todowrite, got %q", ev.Tool)
	}
	if !isOpencodeTodoTool(ev.Tool) {
		t.Errorf("expected isOpencodeTodoTool(%q)=true", ev.Tool)
	}
	todos, ok := ev.Args["todos"].([]any)
	if !ok {
		t.Fatalf("expected Args.todos to be populated from state.input, got Args=%v", ev.Args)
	}
	if len(todos) != 3 {
		t.Fatalf("expected 3 todos, got %d", len(todos))
	}
	first, _ := todos[0].(map[string]any)
	if first["content"] != "step one" || first["status"] != "completed" {
		t.Errorf("unexpected first todo: %v", first)
	}
}

func TestRunStreamOpencodeTodoCollapsesToSingleRow(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "test.db")
	if err := db.Init(dbPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	// opencode emits the todo list as THREE separate tool_use events, each
	// with a distinct part.id AND callID, progressing pending →
	// in_progress → completed. The stream loop must collapse them into a
	// single tool_call row (stable id) that updates in place.
	l1 := `{"type":"tool_use","sessionID":"oc-fake","part":{"id":"prt_1","type":"tool","tool":"todowrite","callID":"toolu_1","state":{"status":"completed","input":{"todos":[{"content":"a","status":"in_progress"},{"content":"b","status":"pending"}]},"output":"ok"}}}`
	l2 := `{"type":"tool_use","sessionID":"oc-fake","part":{"id":"prt_2","type":"tool","tool":"todowrite","callID":"toolu_2","state":{"status":"completed","input":{"todos":[{"content":"a","status":"completed"},{"content":"b","status":"in_progress"}]},"output":"ok"}}}`
	l3 := `{"type":"tool_use","sessionID":"oc-fake","part":{"id":"prt_3","type":"tool","tool":"todowrite","callID":"toolu_3","state":{"status":"completed","input":{"todos":[{"content":"a","status":"completed"},{"content":"b","status":"completed"}]},"output":"ok"}}}`
	textLine := `{"type":"text","sessionID":"oc-fake","part":{"type":"text","text":"done","time":{"end":1}}}`

	bin := filepath.Join(tmp, "opencode")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' '" + l1 + "'\n" +
		"printf '%s\\n' '" + l2 + "'\n" +
		"printf '%s\\n' '" + l3 + "'\n" +
		"printf '%s\\n' '" + textLine + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("KWA_OPENCODE_AUTO_UPDATE", "false")
	t.Setenv("KWA_OPENCODE_INSTALL_DIR", filepath.Join(tmp, "install"))
	prevOpencodeEnsured := opencodeEnsured
	opencodeEnsured = false
	t.Cleanup(func() { opencodeEnsured = prevOpencodeEnsured })

	config.Workspace = tmp
	config.OpencodeBin = bin
	cfg := config.SessionConfig{Backend: config.BackendOpencode, Workdir: tmp, Yolo: true}
	if err := db.CreateSession("oc-todo", cfg); err != nil {
		t.Fatal(err)
	}

	events, proc := RunStream("oc-todo", "plan", cfg)
	if proc == nil {
		t.Fatal("expected active process")
	}

	var toolCallIDs []string
	var lastTodoArgs map[string]any
	toolDoneForTodo := false
	timeout := time.After(5 * time.Second)
	for done := false; !done; {
		select {
		case ev, ok := <-events:
			if !ok {
				done = true
				break
			}
			switch ev.Kind {
			case "tool_call":
				toolCallIDs = append(toolCallIDs, ev.CallID)
				if ev.CallID == opencodeTodoCallID {
					lastTodoArgs = ev.Args
				}
			case "tool_done":
				if ev.CallID == opencodeTodoCallID {
					toolDoneForTodo = true
				}
			}
		case <-timeout:
			t.Fatal("timed out waiting for opencode todo stream")
		}
	}

	// Exactly ONE distinct tool_call id, and it must be the stable todo id
	// (re-emitted 3 times to update the same row).
	todoCalls := 0
	for _, id := range toolCallIDs {
		if id == opencodeTodoCallID {
			todoCalls++
		} else {
			t.Errorf("unexpected non-todo tool_call id %q", id)
		}
	}
	if todoCalls != 3 {
		t.Errorf("expected 3 tool_call updates under the stable todo id, got %d (ids=%v)", todoCalls, toolCallIDs)
	}
	if !toolDoneForTodo {
		t.Errorf("expected a finalizing tool_done for the todo row")
	}
	// The final update must reflect the latest checklist (both completed).
	todos, _ := lastTodoArgs["todos"].([]any)
	if len(todos) != 2 {
		t.Fatalf("expected last todo args to carry 2 items, got %v", lastTodoArgs)
	}
	for _, it := range todos {
		m, _ := it.(map[string]any)
		if m["status"] != "completed" {
			t.Errorf("expected all items completed in final update, got %v", m)
		}
	}
}

func TestNewUUID(t *testing.T) {
	id := NewUUID()
	if len(id) != 36 {
		t.Errorf("expected UUID length 36, got %d: %q", len(id), id)
	}
	if id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		t.Errorf("unexpected UUID format: %q", id)
	}
	// Version 4 check
	if id[14] != '4' {
		t.Errorf("expected version 4, got %c in %q", id[14], id)
	}
}
