// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"strings"
	"testing"
	"time"
)

// A turn as opencode 2.2.5 prints it: a step that reads a file, then a step
// that answers.
var turnLines = []string{
	`{"type":"step_start","timestamp":1793696463398,"sessionID":"ses_1","part":{"type":"step-start"}}`,
	`{"type":"tool_use","timestamp":1793696463614,"sessionID":"ses_1","part":{"id":"prt_1","partID":"prt_1","type":"tool","tool":"read","state":{"status":"completed","input":{"path":"/w/notes.txt"},"output":"alpha beta","title":"read","time":{"start":1793696463401,"end":1793696463614}}}}`,
	`{"type":"step_finish","timestamp":1793696463617,"sessionID":"ses_1","part":{"type":"step-finish","reason":"tool-calls","cost":0.0011049,"tokens":{"input":4,"output":60,"reasoning":0,"cache":{"read":0,"write":8596}}}}`,
	`{"type":"step_start","timestamp":1793696464965,"sessionID":"ses_1","part":{"type":"step-start"}}`,
	`{"type":"text","timestamp":1793696465017,"sessionID":"ses_1","part":{"type":"text","text":"The first word is alpha.","time":{"start":1793696464969,"end":1793696465017}}}`,
	`{"type":"step_finish","timestamp":1793696465024,"sessionID":"ses_1","part":{"type":"step-finish","reason":"stop","cost":0.000108785,"tokens":{"input":2,"output":20,"reasoning":0,"cache":{"read":8596,"write":101}}}}`,
}

type recorded struct {
	events map[string][]map[string]any
	spans  map[string][]map[string]any
	output string // the tool.output event's content
	parent map[string]bool
}

func record(t *testing.T, cfg Config, info CallInfo) recorded {
	t.Helper()
	col, srv := newCollector(t)
	cfg.Endpoint = srv.URL
	Configure(map[string]Config{"google-vertex": cfg})
	c := Start(info)
	if c == nil {
		t.Fatal("no recorder for a provider with telemetry")
	}
	for _, l := range turnLines {
		c.Line(l)
	}
	c.Finish()
	For("google-vertex").flush(t.Context())

	r := recorded{events: map[string][]map[string]any{}, spans: map[string][]map[string]any{}, parent: map[string]bool{}}
	for _, body := range col.got("/v1/logs") {
		for _, rl := range msgs(body, reqResourceLogs) {
			for _, sl := range msgs(encodeOf(rl), rlScopeLogs) {
				for _, lr := range msgs(encodeOf(sl), slLogRecords) {
					a := attrMap(encodeOf(lr), logAttributes)
					name := strOf(a["event.name"])
					if body := attrBody(lr); body != "claude_code."+name {
						t.Errorf("event %s has body %q", name, body)
					}
					r.events[name] = append(r.events[name], a)
				}
			}
		}
	}
	for _, body := range col.got("/v1/traces") {
		for _, rs := range msgs(body, reqResourceSpans) {
			for _, ss := range msgs(encodeOf(rs), rsScopeSpans) {
				for _, sp := range msgs(encodeOf(ss), ssSpans) {
					name := str(sp, spanName)
					r.spans[name] = append(r.spans[name], attrMap(encodeOf(sp), spanAttributes))
					r.parent[name] = len(raw(sp, spanParentID)) > 0
					for _, ev := range msgs(encodeOf(sp), spanEvents) {
						if str(ev, eventName) == "tool.output" {
							r.output = strOf(attrMap(encodeOf(ev), eventAttributes)["content"])
						}
					}
				}
			}
		}
	}
	return r
}

func attrBody(lr []field) string {
	fs, _ := decode(raw(lr, logBody))
	return str(fs, anyString)
}

func TestARecordedTurn(t *testing.T) {
	t.Setenv("STRONG_NETWORK_WORKSPACE_ID", "ws1")
	r := record(t, testConfig(""), CallInfo{Model: "google-vertex/claude-sonnet-5-5@default", SessionID: "chat-1",
		Prompt: "Read notes.txt", NewSession: true, Effort: "high", Started: time.UnixMilli(1793696463300)})

	p := r.events["user_prompt"]
	if len(p) != 1 || p[0]["prompt"] != "Read notes.txt" || p[0]["prompt_length"] != "14" ||
		p[0]["session.id"] != "chat-1" || p[0]["user.email"] != "alex@example.com" || p[0]["terminal.type"] != "knowledge-worker-agent" {
		t.Errorf("user_prompt = %v", p)
	}
	req := r.events["api_request"]
	if len(req) != 2 {
		t.Fatalf("%d api_request events, want one per step", len(req))
	}
	// A request runs from when it was sent (the call's start, then the end of
	// the previous step) until its first tool, or the step's end.
	if req[0]["model"] != "claude-sonnet-5-5" || req[0]["cache_creation_tokens"] != "8596" || req[0]["output_tokens"] != "60" ||
		req[0]["query_source"] != "main" || req[0]["effort"] != "high" || req[0]["duration_ms"] != "101" {
		t.Errorf("first api_request = %v", req[0])
	}
	if req[1]["cache_read_tokens"] != "8596" || req[1]["duration_ms"] != "1407" || req[1]["ttft_ms"] != "1352" {
		t.Errorf("second api_request = %v", req[1])
	}
	tr := r.events["tool_result"]
	if len(tr) != 1 || tr[0]["tool_name"] != "read" || tr[0]["success"] != "true" || tr[0]["duration_ms"] != "213" ||
		tr[0]["tool_input"] != `{"path":"/w/notes.txt"}` {
		t.Errorf("tool_result = %v", tr)
	}
	if d := r.events["tool_decision"]; len(d) != 1 || d[0]["decision"] != "accept" || d[0]["source"] != "config" {
		t.Errorf("tool_decision = %v", d)
	}
	if a := r.events["assistant_response"]; len(a) != 1 || a[0]["response"] != "The first word is alpha." {
		t.Errorf("assistant_response = %v", a)
	}
	// Every event of a prompt shares its prompt.id.
	id := p[0]["prompt.id"]
	for name, list := range r.events {
		for _, a := range list {
			if a["prompt.id"] != id || id == "" {
				t.Errorf("%s has prompt.id %v, want %v", name, a["prompt.id"], id)
			}
		}
	}

	if len(r.spans["claude_code.interaction"]) != 1 || r.parent["claude_code.interaction"] {
		t.Error("want one root interaction span")
	}
	if len(r.spans["claude_code.llm_request"]) != 2 || !r.parent["claude_code.llm_request"] {
		t.Error("want an llm_request span per step, inside the interaction")
	}
	tool := r.spans["claude_code.tool"]
	if len(tool) != 1 || tool[0]["file_path"] != "/w/notes.txt" || len(r.spans["claude_code.tool.execution"]) != 1 {
		t.Errorf("tool spans = %v", tool)
	}
	if r.output != "alpha beta" {
		t.Errorf("tool.output = %q", r.output)
	}
}

func TestContentFlagsOff(t *testing.T) {
	cfg := testConfig("")
	cfg.LogUserPrompts, cfg.LogAssistantResponses, cfg.LogToolDetails, cfg.LogToolContent = false, false, false, false
	r := record(t, cfg, CallInfo{Model: "google-vertex/claude-sonnet-5-5@default", SessionID: "c", Prompt: "secret plan"})
	check := func(where string, a map[string]any) {
		for k, v := range a {
			if s, ok := v.(string); ok && (strings.Contains(s, "secret plan") || strings.Contains(s, "alpha")) {
				t.Errorf("%s carries content in %s: %q", where, k, s)
			}
		}
	}
	for name, list := range r.events {
		for _, a := range list {
			check(name, a)
		}
	}
	for name, list := range r.spans {
		for _, a := range list {
			check(name, a)
		}
	}
	if r.output != "" {
		t.Error("tool output sent with logToolContent off")
	}
	if p := r.events["user_prompt"]; len(p) != 1 || p[0]["prompt_length"] != "11" {
		t.Errorf("user_prompt without content = %v", p)
	}
}

func TestBackgroundWorkHasNoInteraction(t *testing.T) {
	r := record(t, testConfig(""), CallInfo{Model: "google-vertex/claude-haiku-5-5@default", SessionID: "c", QuerySource: "title"})
	if len(r.events["user_prompt"]) != 0 || len(r.spans["claude_code.interaction"]) != 0 {
		t.Error("background work recorded a prompt or an interaction")
	}
	if req := r.events["api_request"]; len(req) != 2 || req[0]["query_source"] != "title" {
		t.Errorf("api_request = %v", req)
	}
}

func TestNoRecorderWithoutTelemetry(t *testing.T) {
	_, srv := newCollector(t)
	Configure(map[string]Config{"google-vertex": testConfig(srv.URL)})
	if Start(CallInfo{Model: "github-copilot/claude-sonnet-5.5"}) != nil {
		t.Error("a provider without telemetry got a recorder")
	}
	var c *Call
	c.Line(turnLines[0])
	c.Error("x")
	c.Finish()
}

func TestModelName(t *testing.T) {
	for in, want := range map[string]string{
		"google-vertex/claude-sonnet-5-5@default": "claude-sonnet-5-5",
		"google-vertex/claude-haiku-4-5@20251001": "claude-haiku-4-5",
		"claude-opus-5-5":                         "claude-opus-5-5",
	} {
		if got := modelName(in); got != want {
			t.Errorf("modelName(%q) = %q, want %q", in, got, want)
		}
	}
}
