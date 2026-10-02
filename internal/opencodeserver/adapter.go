// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeserver

import (
	"encoding/json"
	"fmt"
	"strings"
)

// EventKind enumerates the neutral event kinds emitted by the adapter. The
// chat package translates these into its own SSE-facing Event type. Keeping a
// package-local type here avoids an import cycle (chat imports opencodeserver,
// not the other way around).
type EventKind string

const (
	KindChunk       EventKind = "chunk"        // assistant text
	KindToolCall    EventKind = "tool_call"    // a tool started
	KindToolDone    EventKind = "tool_done"    // a tool finished (ok/err)
	KindToolBlocked EventKind = "tool_blocked" // a tool was denied by permission
	KindPermission  EventKind = "permission"   // interactive permission request
	KindStep        EventKind = "step"         // lightweight progress heartbeat
	KindStepFinish  EventKind = "step_finish"  // token/cost accounting for one step
	KindError       EventKind = "error"        // session error
	KindResult      EventKind = "result"       // terminal: session went idle
)

// Event is a neutral, transport-agnostic event produced from the opencode
// /event stream for a single chat turn.
type Event struct {
	Kind      EventKind
	Text      string
	Tool      string
	CallID    string
	Args      map[string]any
	Success   bool
	Output    string
	SessionID string

	// Permission fields (Kind == KindPermission).
	PermissionID string
	Permission   string   // permission type/name (e.g. "bash")
	Patterns     []string // patterns the request would allow

	// Accounting (Kind == KindStepFinish / KindResult).
	TokensIn      int
	TokensOut     int
	TokensInKnown bool
	Cost          float64
}

// turnState tracks per-turn bookkeeping while translating the /event stream.
// opencode emits message.part.updated for BOTH the user's message (the echoed
// prompt) and the assistant's reply. We classify message roles from
// message.updated events and suppress parts that belong to a user message so
// the prompt isn't re-rendered as assistant output.
type turnState struct {
	userMessages map[string]bool // message IDs known to be role=user
}

func newTurnState() *turnState {
	return &turnState{userMessages: map[string]bool{}}
}

// isUserPart reports whether a part belongs to a message we've classified as
// the user's. Parts with no messageID, or belonging to an unknown/assistant
// message, are not suppressed.
func (st *turnState) isUserPart(part map[string]any) bool {
	if st == nil {
		return false
	}
	mid, _ := part["messageID"].(string)
	if mid == "" {
		return false
	}
	return st.userMessages[mid]
}

// sessionScope tracks which opencode sessions belong to a single chat turn: the
// root session the prompt was sent to, plus any descendant sessions opencode
// spins up for subagents (the `task` tool runs each subagent in its OWN child
// session, whose events carry a different sessionID than the root turn).
// Without this, a subagent's activity is dropped as "not our session", so the
// parent turn appears to hang on an opaque `task` tool call and can trip the
// inactivity watchdog before the subagent finishes.
type sessionScope struct {
	root     string
	sessions map[string]bool // in-scope session IDs (root + descendants)
}

func newSessionScope(root string) *sessionScope {
	return &sessionScope{root: root, sessions: map[string]bool{root: true}}
}

// observe inspects an event for parent→child session lineage and, when a
// session's parent is already in scope, admits the child. It must be called for
// EVERY event before any scope filtering, because a child session is first
// introduced by its own session event (whose sessionID is not yet in scope).
func (s *sessionScope) observe(ev ServerEvent) {
	if s == nil {
		return
	}
	switch ev.Type {
	case "session.updated", "session.created":
		info, _ := ev.Properties["info"].(map[string]any)
		id, _ := info["id"].(string)
		parent, _ := info["parentID"].(string)
		if id != "" && parent != "" && s.sessions[parent] {
			s.sessions[id] = true
		}
	}
}

// contains reports whether a session ID belongs to this turn.
func (s *sessionScope) contains(sid string) bool {
	if s == nil {
		return false
	}
	return s.sessions[sid]
}

// isRoot reports whether a session ID is the turn's root session (vs a subagent
// child session).
func (s *sessionScope) isRoot(sid string) bool {
	return s != nil && sid == s.root
}

// demoteChildEvents adapts events emitted by a subagent's child session for the
// minimal ("Option A") presentation: the subagent's internal steps are NOT
// rendered as top-level rows (the parent's `task` tool row already carries its
// final result), but we still surface a lightweight progress heartbeat so the
// UI shows activity and the turn's inactivity watchdog keeps resetting, and we
// preserve token/cost accounting and hard errors. Any emitted heartbeat is
// re-tagged to the root session so downstream consumers never see child IDs.
func demoteChildEvents(in []Event, root string) []Event {
	if len(in) == 0 {
		return in
	}
	out := make([]Event, 0, len(in))
	heartbeat := false
	label := ""
	for _, e := range in {
		switch e.Kind {
		case KindStepFinish, KindError:
			out = append(out, e)
		case KindResult:
			// A child session's terminal sentinel must not end the parent turn.
		case KindToolCall, KindToolDone, KindToolBlocked:
			// Summarize the subagent's activity into a progress label (the
			// child tool's NAME only — never its args or output) that the
			// parent's live `task` row can display. This keeps the signal
			// ("reading files", "running bash") without leaking the child's
			// internal tool cards into the transcript.
			heartbeat = true
			if e.Tool != "" {
				label = e.Tool
			}
		default:
			heartbeat = true
		}
	}
	if heartbeat {
		out = append(out, Event{Kind: KindStep, SessionID: root, Tool: label})
	}
	return out
}

// classifyEvent converts one raw server event (already filtered to the target
// session) into zero or more neutral Events. It returns (events, done) where
// done is true once the turn is complete (session.idle) and the caller should
// stop consuming the stream for this turn.
//
// The opencode /event envelope is:
//
//	{ "type": "<name>", "properties": { "sessionID": "...", "part": {...} | "info": {...} | ... } }
//
// Parts carried by message.part.updated use the same schema as `opencode run
// --format json` (text / tool / step-start / step-finish / reasoning), except
// the step types are hyphenated here ("step-start", "step-finish").
func classifyEvent(ev ServerEvent, st *turnState) (out []Event, done bool) {
	props := ev.Properties
	sid := strProp(props, "sessionID")

	switch ev.Type {
	case "message.updated":
		// Record message role so we can suppress the user's echoed prompt in
		// message.part.updated. properties.info = { id, role, ... }.
		if info, ok := props["info"].(map[string]any); ok {
			id, _ := info["id"].(string)
			role, _ := info["role"].(string)
			if id != "" && role == "user" && st != nil {
				st.userMessages[id] = true
			}
		}
		return nil, false

	case "message.part.updated":
		part, _ := props["part"].(map[string]any)
		if part == nil {
			return nil, false
		}
		if st.isUserPart(part) {
			// The user's echoed prompt — do not render as assistant output.
			return nil, false
		}
		return classifyPart(part, sid, st), false

	case "permission.asked":
		out = append(out, permissionEvent(props, sid))
		return out, false

	case "session.error":
		msg := extractSessionError(props["error"])
		if msg == "" {
			msg = "opencode returned an error"
		}
		out = append(out, Event{Kind: KindError, Text: msg, SessionID: sid})
		return out, false

	case "session.idle":
		// Terminal for the turn.
		return nil, true
	}
	return nil, false
}

// classifyPart maps a single Part object to neutral events.
func classifyPart(part map[string]any, sid string, st *turnState) []Event {
	ptype, _ := part["type"].(string)
	switch ptype {
	case "text":
		text := stringify(part["text"])
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []Event{{Kind: KindChunk, Text: text, SessionID: sid}}

	case "reasoning":
		// Reasoning summaries are not surfaced as visible chunks today; treat
		// as a progress heartbeat so the UI shows activity.
		return []Event{{Kind: KindStep, SessionID: sid}}

	case "step-start":
		return []Event{{Kind: KindStep, SessionID: sid}}

	case "step-finish":
		tokens, _ := part["tokens"].(map[string]any)
		// opencode's token object is
		//   { input, output, reasoning, cache: { read, write } }
		// For prompt-caching models (e.g. Claude via Copilot) the bulk of the
		// re-fed context — file contents, prior turns — is billed as cache
		// read/write tokens, NOT as plain "input". Counting only `input` here
		// wildly under-reports input (e.g. 14 tokens for a $1.27 turn), so we
		// include cache read+write in the input total.
		cache, _ := tokens["cache"].(map[string]any)
		inputTokens := toInt(tokens["input"]) + toInt(cache["read"]) + toInt(cache["write"])
		ev := Event{
			Kind:          KindStepFinish,
			SessionID:     sid,
			TokensIn:      inputTokens,
			TokensOut:     toInt(tokens["output"]) + toInt(tokens["reasoning"]),
			TokensInKnown: tokens != nil,
			Cost:          toFloat(part["cost"]),
		}
		return []Event{ev}

	case "tool":
		return toolEvents(part, sid)
	}
	return nil
}

// toolEvents maps a ToolPart update to zero or one neutral events. opencode
// emits a message.part.updated for every tool state transition
// (pending → running → completed/error). We normally emit only on the TERMINAL
// states (completed/error) so the chat layer gets a single tool_done/tool_blocked
// carrying the fully-populated input args and output — avoiding the empty-args
// and duplicate-row problems that arise from forwarding every intermediate
// update.
//
// The one exception is the `task` (subagent delegation) tool: a subagent can
// run for many seconds, so we ALSO emit a KindToolCall the moment it reaches
// `running`. That gives the UI a live in-flight `task` row immediately instead
// of the row appearing only once the subagent finishes. The chat layer
// reconciles this running row with the later terminal tool_done via call_id.
func toolEvents(part map[string]any, sid string) []Event {
	state, _ := part["state"].(map[string]any)
	status, _ := state["status"].(string)

	tool, _ := part["tool"].(string)
	callID, _ := part["callID"].(string)

	args, _ := part["input"].(map[string]any)
	if len(args) == 0 {
		if in, ok := state["input"].(map[string]any); ok {
			args = in
		}
	}

	switch status {
	case "completed", "error":
		// terminal — emit below
	case "running":
		// Live in-flight row for subagent delegations only (see doc comment).
		if tool == "task" {
			return []Event{{Kind: KindToolCall, Tool: tool, CallID: callID, Args: args, SessionID: sid}}
		}
		return nil
	default:
		// pending — not yet actionable
		return nil
	}

	success := status != "error"

	output := extractToolOutput(state)
	if output == "" && state["error"] != nil {
		output = stringify(state["error"])
	}
	if len(output) > 2000 {
		output = output[:2000]
	}

	if status == "error" && isPermissionDenied(output) {
		return []Event{{Kind: KindToolBlocked, Tool: tool, CallID: callID, Args: args, Output: output, SessionID: sid}}
	}
	return []Event{{Kind: KindToolDone, Tool: tool, CallID: callID, Args: args, Success: success, Output: output, SessionID: sid}}
}

// permissionEvent maps a permission.asked event.
func permissionEvent(props map[string]any, sid string) Event {
	permID := strProp(props, "id")
	permName := strProp(props, "permission")
	var patterns []string
	if arr, ok := props["patterns"].([]any); ok {
		for _, p := range arr {
			if s, ok := p.(string); ok {
				patterns = append(patterns, s)
			}
		}
	}
	// The tool call this permission gates (messageID/callID) rides in "tool".
	var callID string
	if tool, ok := props["tool"].(map[string]any); ok {
		callID, _ = tool["callID"].(string)
	}
	return Event{
		Kind:         KindPermission,
		SessionID:    sid,
		PermissionID: permID,
		Permission:   permName,
		Patterns:     patterns,
		CallID:       callID,
	}
}

// --- helpers (kept package-local; mirror the ones in internal/chat) ---

func strProp(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	}
	return 0
}

func stringify(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func extractToolOutput(state map[string]any) string {
	if state == nil {
		return ""
	}
	for _, key := range []string{"output", "result"} {
		if v, ok := state[key]; ok {
			switch o := v.(type) {
			case string:
				return o
			default:
				b, _ := json.Marshal(o)
				return string(b)
			}
		}
	}
	// opencode nests tool output under state.metadata for some tools.
	if meta, ok := state["metadata"].(map[string]any); ok {
		if out, ok := meta["output"].(string); ok {
			return out
		}
	}
	return ""
}

func extractSessionError(v any) string {
	switch e := v.(type) {
	case string:
		return e
	case map[string]any:
		if data, ok := e["data"].(map[string]any); ok {
			if msg := strProp(data, "message"); msg != "" {
				return msg
			}
		}
		if msg := strProp(e, "message"); msg != "" {
			return msg
		}
		if name := strProp(e, "name"); name != "" {
			return name
		}
		b, _ := json.Marshal(e)
		return string(b)
	default:
		return ""
	}
}

// isPermissionDenied reports whether a tool error message indicates the call
// was blocked by opencode's permission system.
func isPermissionDenied(msg string) bool {
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "rejected permission"):
		return true
	case strings.Contains(low, "permission denied"):
		return true
	case strings.Contains(low, "permission to use") && strings.Contains(low, "reject"):
		return true
	default:
		return false
	}
}

// errString is a small helper for wrapping.
func errString(prefix string, err error) string {
	return fmt.Sprintf("%s: %v", prefix, err)
}
