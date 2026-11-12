// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// CallInfo describes one model call.
type CallInfo struct {
	// Model is "provider/model", as KWA passes it to opencode.
	Model     string
	SessionID string
	// Prompt is what the user typed, without KWA's preamble. Empty for
	// background work.
	Prompt string
	// QuerySource is "main" for a chat turn, or names the background job.
	QuerySource string
	Effort      string
	// NewSession is set on the first prompt of a chat.
	NewSession bool
	// Started is when the call began, when that was before Start: a background
	// run is recorded once it has finished.
	Started time.Time
}

// Call turns one call's opencode events into records. A nil Call does nothing.
type Call struct {
	e       *Exporter
	info    CallInfo
	model   string
	start   time.Time
	traceID []byte
	rootID  []byte
	common  []Attr
	prompt  string

	// sent is when the current model request was sent: when the call began, or
	// when the previous step finished. opencode's step_start comes only once
	// the reply streams. toolStart is when the step's first tool began, which
	// ends the model's part of the step.
	sent       time.Time
	toolStart  time.Time
	firstText  time.Time
	stepText   strings.Builder
	finished   bool
	stepFailed bool
	// added and removed count the lines the call's tools changed.
	added, removed int
}

var sequence atomic.Int64

// Start begins recording a call, or returns nil when the call's provider has
// no telemetry.
func Start(info CallInfo) *Call {
	provider, _, _ := strings.Cut(info.Model, "/")
	e := For(provider)
	if e == nil {
		return nil
	}
	if info.QuerySource == "" {
		info.QuerySource = "main"
	}
	start := info.Started
	if start.IsZero() {
		start = time.Now()
	}
	c := &Call{
		e:       e,
		info:    info,
		model:   modelName(info.Model),
		start:   start,
		sent:    start,
		traceID: newID(16),
		rootID:  newID(8),
		common:  e.common(info.SessionID),
	}
	if info.QuerySource == "main" {
		c.prompt = hex.EncodeToString(newID(16))
		c.prompt = c.prompt[:8] + "-" + c.prompt[8:12] + "-" + c.prompt[12:16] + "-" + c.prompt[16:20] + "-" + c.prompt[20:]
		if info.NewSession {
			e.add("claude_code.session.count", "", c.metricAttrs(Attr{"start_type", "fresh"}), 1)
		}
		attrs := []Attr{{"prompt_length", fmt.Sprint(len([]rune(info.Prompt)))}}
		if e.cfg.LogUserPrompts {
			attrs = append(attrs, Attr{"prompt", info.Prompt}, Attr{"prompt_text", info.Prompt})
		}
		c.event("user_prompt", c.start, attrs)
	}
	return c
}

// modelName is the model as Claude Code reports it: without the provider or
// an "@version" suffix.
func modelName(m string) string {
	if _, after, ok := strings.Cut(m, "/"); ok {
		m = after
	}
	m, _, _ = strings.Cut(m, "@")
	return m
}

func (c *Call) metricAttrs(extra ...Attr) []Attr {
	out := append([]Attr{}, c.common...)
	return append(out, extra...)
}

func (c *Call) usageAttrs(extra ...Attr) []Attr {
	attrs := c.metricAttrs(Attr{"model", c.model}, Attr{"query_source", c.info.QuerySource})
	if c.info.Effort != "" {
		attrs = append(attrs, Attr{"effort", c.info.Effort})
	}
	return append(attrs, extra...)
}

func (c *Call) event(name string, at time.Time, attrs []Attr) {
	all := append([]Attr{}, c.common...)
	all = append(all,
		Attr{"event.name", name},
		Attr{"event.timestamp", at.UTC().Format("2006-01-02T15:04:05.000Z")},
		Attr{"event.sequence", sequence.Add(1) - 1})
	if c.prompt != "" {
		all = append(all, Attr{"prompt.id", c.prompt})
	}
	c.e.event(logRecord{time: at, name: name, attrs: append(all, attrs...), traceID: c.traceID, spanID: c.rootID})
}

func (c *Call) spanAttrs(kind string, extra ...Attr) []Attr {
	all := append([]Attr{}, c.common...)
	all = append(all, Attr{"span.type", kind})
	if c.info.SessionID != "" {
		all = append(all, Attr{"gen_ai.conversation.id", c.info.SessionID})
	}
	all = append(all, Attr{"gen_ai.provider.name", "gcp.vertex_ai"})
	return append(all, extra...)
}

// opencodeEvent is one line of `opencode run --format json`.
type opencodeEvent struct {
	Type      string `json:"type"`
	Timestamp int64  `json:"timestamp"`
	Part      struct {
		ID     string  `json:"id"`
		PartID string  `json:"partID"`
		Text   string  `json:"text"`
		Tool   string  `json:"tool"`
		Reason string  `json:"reason"`
		Cost   float64 `json:"cost"`
		Time   struct {
			Start int64 `json:"start"`
			End   int64 `json:"end"`
		} `json:"time"`
		Tokens struct {
			Input     int64 `json:"input"`
			Output    int64 `json:"output"`
			Reasoning int64 `json:"reasoning"`
			Cache     struct {
				Read  int64 `json:"read"`
				Write int64 `json:"write"`
			} `json:"cache"`
		} `json:"tokens"`
		State struct {
			Status   string          `json:"status"`
			Input    json.RawMessage `json:"input"`
			Output   json.RawMessage `json:"output"`
			Error    json.RawMessage `json:"error"`
			Metadata json.RawMessage `json:"metadata"`
			Time     struct {
				Start int64 `json:"start"`
				End   int64 `json:"end"`
			} `json:"time"`
		} `json:"state"`
	} `json:"part"`
	Error json.RawMessage `json:"error"`
}

func ms(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v)
}

// Line records one line of opencode's output.
func (c *Call) Line(raw string) {
	if c == nil {
		return
	}
	var ev opencodeEvent
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &ev) != nil {
		return
	}
	at := ms(ev.Timestamp)
	if at.IsZero() {
		at = time.Now()
	}
	switch ev.Type {
	case "step_start":
		c.toolStart, c.firstText, c.stepFailed = time.Time{}, time.Time{}, false
		c.stepText.Reset()
	case "text":
		if c.firstText.IsZero() {
			c.firstText = ms(ev.Part.Time.Start)
			if c.firstText.IsZero() {
				c.firstText = at
			}
		}
		c.stepText.WriteString(ev.Part.Text)
	case "tool_use":
		c.tool(ev, at)
	case "step_finish":
		c.step(ev, at)
	case "error":
		c.Error(errorText(ev.Error))
	}
}

func (c *Call) tool(ev opencodeEvent, at time.Time) {
	id := ev.Part.PartID
	if id == "" {
		id = ev.Part.ID
	}
	start, end := ms(ev.Part.State.Time.Start), ms(ev.Part.State.Time.End)
	if start.IsZero() {
		start = at
	}
	if end.IsZero() {
		end = at
	}
	if c.toolStart.IsZero() || start.Before(c.toolStart) {
		c.toolStart = start
	}
	ok := ev.Part.State.Status != "error"
	dur := end.Sub(start).Milliseconds()
	output := rawText(ev.Part.State.Output)
	if !ok && output == "" {
		output = rawText(ev.Part.State.Error)
	}
	input := string(ev.Part.State.Input)
	if ok {
		added, removed := lineChanges(ev.Part.State.Input, ev.Part.State.Metadata)
		c.added += added
		c.removed += removed
	}

	c.event("tool_decision", start, []Attr{
		{"decision", "accept"}, {"source", "config"}, {"tool_name", ev.Part.Tool}, {"tool_use_id", id}})
	result := []Attr{
		{"tool_name", ev.Part.Tool}, {"tool_use_id", id}, {"success", fmt.Sprint(ok)},
		{"duration_ms", fmt.Sprint(dur)}, {"tool_result_size_bytes", fmt.Sprint(len(output))}}
	if c.e.cfg.LogToolDetails && input != "" && input != "null" {
		result = append(result, Attr{"tool_input", input}, Attr{"tool_input_size_bytes", fmt.Sprint(len(input))})
	}
	c.event("tool_result", end, result)

	toolSpan := newID(8)
	attrs := []Attr{{"tool_name", ev.Part.Tool}, {"gen_ai.tool.name", ev.Part.Tool},
		{"gen_ai.tool.call.id", id}, {"tool_use_id", id}, {"gen_ai.operation.name", "execute_tool"},
		{"duration_ms", dur}, {"success", ok}}
	path := filePath(ev.Part.State.Input)
	if path != "" {
		attrs = append(attrs, Attr{"file_path", path})
	}
	var events []spanEvent
	if c.e.cfg.LogToolContent && output != "" {
		evAttrs := []Attr{{"content", output}}
		if path != "" {
			evAttrs = append(evAttrs, Attr{"file_path", path})
		}
		events = append(events, spanEvent{time: end, name: "tool.output", attrs: evAttrs})
	}
	c.e.span(span{traceID: c.traceID, spanID: toolSpan, parentID: c.parent(), name: "claude_code.tool",
		start: start, end: end, attrs: c.spanAttrs("tool", attrs...), events: events, failed: !ok})
	c.e.span(span{traceID: c.traceID, spanID: newID(8), parentID: toolSpan, name: "claude_code.tool.execution",
		start: start, end: end, failed: !ok,
		attrs: c.spanAttrs("tool.execution", Attr{"gen_ai.tool.call.id", id}, Attr{"tool_use_id", id},
			Attr{"duration_ms", dur}, Attr{"success", ok})})
}

func (c *Call) step(ev opencodeEvent, at time.Time) {
	start := c.sent
	end := at
	if !c.toolStart.IsZero() && c.toolStart.After(start) {
		end = c.toolStart
	}
	t := ev.Part.Tokens
	output := t.Output + t.Reasoning
	dur := end.Sub(start).Milliseconds()
	var ttft int64
	if !c.firstText.IsZero() {
		ttft = c.firstText.Sub(start).Milliseconds()
	}
	cost := ev.Part.Cost

	for _, u := range []struct {
		kind string
		n    int64
	}{{"input", t.Input}, {"output", output}, {"cacheRead", t.Cache.Read}, {"cacheCreation", t.Cache.Write}} {
		c.e.add("claude_code.token.usage", "tokens", c.usageAttrs(Attr{"type", u.kind}), float64(u.n))
	}
	c.e.add("claude_code.cost.usage", "USD", c.usageAttrs(), cost)

	req := []Attr{{"model", c.model}, {"input_tokens", fmt.Sprint(t.Input)}, {"output_tokens", fmt.Sprint(output)},
		{"cache_read_tokens", fmt.Sprint(t.Cache.Read)}, {"cache_creation_tokens", fmt.Sprint(t.Cache.Write)},
		{"cost_usd", fmt.Sprint(cost)}, {"cost_usd_micros", fmt.Sprint(int64(cost * 1e6))},
		{"duration_ms", fmt.Sprint(dur)}, {"query_source", c.info.QuerySource}, {"speed", "normal"}}
	if ttft > 0 {
		req = append(req, Attr{"ttft_ms", fmt.Sprint(ttft)})
	}
	if c.info.Effort != "" {
		req = append(req, Attr{"effort", c.info.Effort})
	}
	c.event("api_request", end, req)

	if text := c.stepText.String(); text != "" {
		resp := []Attr{{"model", c.model}, {"response_length", fmt.Sprint(len([]rune(text)))}, {"query_source", c.info.QuerySource}}
		if c.e.cfg.LogAssistantResponses {
			resp = append(resp, Attr{"response", text})
		}
		c.event("assistant_response", at, resp)
	}

	span := []Attr{{"model", c.model}, {"gen_ai.request.model", c.model}, {"gen_ai.system", "anthropic"},
		{"input_tokens", t.Input}, {"output_tokens", output}, {"cache_read_tokens", t.Cache.Read},
		{"cache_creation_tokens", t.Cache.Write}, {"duration_ms", dur}, {"attempt", 1},
		{"stop_reason", ev.Part.Reason}, {"gen_ai.response.finish_reasons", ev.Part.Reason},
		{"success", !c.stepFailed}, {"llm_request.context", "interaction"}, {"query_source_safe", c.info.QuerySource}}
	if ttft > 0 {
		span = append(span, Attr{"ttft_ms", ttft}, Attr{"first_content_ms", ttft})
	}
	if c.info.Effort != "" {
		span = append(span, Attr{"effort", c.info.Effort})
	}
	c.e.span(span2(c, "claude_code.llm_request", start, end, c.spanAttrs("llm_request", span...),
		[]spanEvent{{time: start, name: "gen_ai.request.attempt", attrs: []Attr{{"attempt", 1}}}}, c.stepFailed))
	c.sent, c.toolStart, c.firstText = at, time.Time{}, time.Time{}
	c.stepText.Reset()
}

func span2(c *Call, name string, start, end time.Time, attrs []Attr, events []spanEvent, failed bool) span {
	return span{traceID: c.traceID, spanID: newID(8), parentID: c.parent(), name: name,
		start: start, end: end, attrs: attrs, events: events, failed: failed}
}

// parent is the interaction span for a chat turn; background work has none.
func (c *Call) parent() []byte {
	if c.info.QuerySource == "main" {
		return c.rootID
	}
	return nil
}

// Error records a failed request.
func (c *Call) Error(msg string) {
	if c == nil {
		return
	}
	now := time.Now()
	start := c.sent
	c.stepFailed = true
	c.event("api_error", now, []Attr{{"model", c.model}, {"error", msg},
		{"duration_ms", fmt.Sprint(now.Sub(start).Milliseconds())}, {"query_source", c.info.QuerySource}})
}

// Finish ends the call.
func (c *Call) Finish() {
	if c == nil || c.finished {
		return
	}
	c.finished = true
	end := time.Now()
	c.e.add("claude_code.active_time.total", "s", c.metricAttrs(Attr{"type", "cli"}), end.Sub(c.start).Seconds())
	if c.added > 0 {
		c.e.add("claude_code.lines_of_code.count", "", c.metricAttrs(Attr{"type", "added"}), float64(c.added))
	}
	if c.removed > 0 {
		c.e.add("claude_code.lines_of_code.count", "", c.metricAttrs(Attr{"type", "removed"}), float64(c.removed))
	}
	if c.info.QuerySource != "main" {
		return
	}
	attrs := []Attr{{"gen_ai.operation.name", "invoke_agent"}, {"parent.source", "none"},
		{"interaction.duration_ms", end.Sub(c.start).Milliseconds()}, {"interaction.sequence", 1},
		{"user_prompt_length", len([]rune(c.info.Prompt))}}
	if c.e.cfg.LogUserPrompts {
		attrs = append(attrs, Attr{"user_prompt", c.info.Prompt})
	}
	c.e.span(span{traceID: c.traceID, spanID: c.rootID, name: "claude_code.interaction",
		start: c.start, end: end, attrs: c.spanAttrs("interaction", attrs...)})
}

// RecordRun records a finished `opencode run --format json` whose whole output
// is at hand: chat titles, day notes and the provider check.
func RecordRun(info CallInfo, output []byte) {
	c := Start(info)
	if c == nil {
		return
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "{") {
			c.Line(line)
		}
	}
	c.Finish()
}

func rawText(r json.RawMessage) string {
	if len(r) == 0 || string(r) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(r, &s) == nil {
		return s
	}
	return string(r)
}

func errorText(r json.RawMessage) string {
	var e struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	}
	if json.Unmarshal(r, &e) == nil && (e.Data.Message != "" || e.Name != "") {
		if e.Data.Message != "" {
			return e.Data.Message
		}
		return e.Name
	}
	return rawText(r)
}

// filePath takes a file tool's path from its input: 2.x writes "path", 1.x
// wrote "filePath".
func filePath(input json.RawMessage) string {
	var in struct {
		Path     string `json:"path"`
		FilePath string `json:"filePath"`
	}
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	if in.Path != "" {
		return in.Path
	}
	return in.FilePath
}
