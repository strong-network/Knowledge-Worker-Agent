// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/durable"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/titler"
)

// omitKey returns a copy of m without the given key.
func omitKey(m map[string]any, key string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if k != key {
			out[k] = v
		}
	}
	return out
}

// Stream owns the lifecycle of a single in-flight opencode turn for a session.
// It buffers all SSE-shaped events (as plain map[string]any payloads) so that
// browser clients can disconnect and reconnect without killing the process or
// losing output.
type Stream struct {
	SessionID string
	// Prompt and Author describe the turn; set before the stream is published
	// and never written again, so readers need no lock. TurnID names this turn
	// on every path it reaches a browser by, so a tab can tell turns apart.
	TurnID string
	Prompt string
	Author Author

	mu     sync.Mutex
	log    []map[string]any
	done   bool
	notify chan struct{} // closed on each Append/Finish, then replaced

	next     *Stream       // the session's following turn, once OpenTurn links it
	nextOpen chan struct{} // closed when next is set
}

// Streams maps session ID → active Stream. Only one in-flight stream per
// session at a time (a new chat replaces the previous).
var Streams sync.Map // map[string]*Stream

func newStream(sid string) *Stream {
	return &Stream{
		SessionID: sid,
		notify:    make(chan struct{}),
		nextOpen:  make(chan struct{}),
	}
}

// Append publishes a new SSE event payload into the log and wakes subscribers.
func (s *Stream) Append(ev map[string]any) {
	s.mu.Lock()
	s.log = append(s.log, ev)
	old := s.notify
	s.notify = make(chan struct{})
	s.mu.Unlock()
	close(old)
}

// Finish marks the stream as done. Subscribers should drain the log then exit.
func (s *Stream) Finish() {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done = true
	old := s.notify
	s.notify = make(chan struct{})
	s.mu.Unlock()
	close(old)
}

// Finished reports whether the stream is done.
func (s *Stream) Finished() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

// Snapshot returns a copy of all events currently in the log and whether the
// stream has finished.
func (s *Stream) Snapshot() ([]map[string]any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, len(s.log))
	copy(out, s.log)
	return out, s.done
}

// Wait blocks until either there are new events past fromIdx, the stream is
// done, or the context is cancelled. Returns (events, newIdx, done).
// On context cancellation it returns (nil, fromIdx, false).
func (s *Stream) Wait(ctx context.Context, fromIdx int) ([]map[string]any, int, bool) {
	for {
		s.mu.Lock()
		if len(s.log) > fromIdx {
			batch := make([]map[string]any, len(s.log)-fromIdx)
			copy(batch, s.log[fromIdx:])
			newIdx := len(s.log)
			done := s.done
			s.mu.Unlock()
			return batch, newIdx, done
		}
		if s.done {
			s.mu.Unlock()
			return nil, fromIdx, true
		}
		wait := s.notify
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, fromIdx, false
		case <-wait:
		}
	}
}

// StartChatStream spawns an opencode process for the session and returns a
// long-lived Stream that buffers all events. It replaces any in-flight stream
// for the same session. The returned Stream is also stored in Streams.
//
// The caller (HTTP handler) becomes a subscriber and forwards events to the
// browser; if the client disconnects, the dispatcher keeps running.
func StartChatStream(sessionID, prompt string, cfg config.SessionConfig, turn ...Turn) *Stream {
	// The associated process of any replaced stream is killed inside RunStream.
	t := firstTurn(turn)
	shown := prompt
	if t.ID == "" {
		// No send path named this turn, so nobody stored its prompt as a
		// message (the project summary's instructions). Viewers are not shown
		// what the transcript does not contain.
		t.ID, shown = NewUUID(), ""
	}
	s := openTurn(sessionID, t.ID, shown, t.Author)

	// First event identifies the canonical session id (browser uses it to
	// adopt the auto-created session).
	s.Append(map[string]any{"type": "session_id", "session_id": sessionID, "turn_id": s.TurnID})

	// Derive a meaningful session title from the first prompt. Started here,
	// alongside the turn rather than after it, because the title depends only
	// on the prompt: running it concurrently means it is normally ready while
	// the agent is still working, and it never delays the response. No-ops
	// unless this is the session's first turn (see titler.Maybe).
	titler.Maybe(sessionID, prompt, cfg, func(title string) {
		s.Append(map[string]any{"type": "title", "title": title})
	})

	events, _ := RunStream(sessionID, prompt, cfg, t)

	go runDispatcher(sessionID, cfg, events, s)
	return s
}

// runDispatcher reads parsed events, persists durable state, and appends
// SSE-shaped payloads onto the stream log for subscribers to forward.
func runDispatcher(sessionID string, cfg config.SessionConfig, events <-chan *Event, s *Stream) {
	defer func() {
		s.Finish()
		// After the current stream is fully done, chain into the next queued
		// prompt (if any). Run in a goroutine so we don't block the dispatcher
		// shutdown and so the new stream (which will replace this one in the
		// Streams map) is started cleanly.
		if QueueLen(sessionID) > 0 {
			go StartNextQueued(sessionID)
		}
		// Don't delete from Streams immediately so a client that reconnects
		// shortly after completion still sees the buffered output. The next
		// StartChatStream for this session will replace it.
	}()

	var fullResponse strings.Builder
	toolCallCount := 0
	tokensOutAccum := 0
	sawQuestion := false
	sawError := false
	// failures are the running turn's errors, in order. They are saved with the
	// reply; otherwise a reload shows the failed turn as a silent cut-off.
	var failures []string
	// detached is set when this turn hit an attachment the model provider
	// cannot accept and we dropped the link to the opencode session to escape
	// its poisoned history. The result event that follows carries the very
	// session id we just let go of, so it must not re-attach it.
	detached := false
	// turnUsage is the JSON usage report for this turn, captured from the
	// result event and stored on the assistant message row so it survives a
	// reload. Empty when the turn produced no result event.
	turnUsage := ""
	// pendingSeparator is set after a message segment ends or a tool finishes,
	// so the next streamed chunk is visually separated from the previous one
	// (otherwise consecutive "thinking" segments concatenate into a wall of
	// text like "...discovered:Now let me copy...").
	pendingSeparator := false
	// mcpTools attributes tool calls to the MCP server that provides them, so
	// the UI can show connector activity apart from ordinary tool use. Scoped to
	// this turn: it reads the server list once, on the first tool event.
	var mcpTools mcpAttributor

	for event := range events {
		switch event.Kind {
		case "error":
			sawError = true
			message := event.Text
			if isUnsupportedAttachmentError(message) {
				message = recoverPoisonedSession(sessionID, message)
				detached = true
			}
			// A stop finishes the stream before killing the process, so what the
			// dying process prints is not a failure of the turn.
			if !s.Finished() {
				failures = append(failures, message)
			}
			s.Append(map[string]any{"type": "error", "message": message})

		case "chunk":
			text := event.Text
			if pendingSeparator && text != "" {
				if !strings.HasPrefix(text, "\n") && !endsWithBlankLine(&fullResponse) {
					text = "\n\n" + text
				}
				pendingSeparator = false
			}
			fullResponse.WriteString(text)
			s.Append(map[string]any{"type": "chunk", "text": text})

		case "message_complete":
			if event.TokensOut > 0 {
				tokensOutAccum += event.TokensOut
			}
			if fullResponse.Len() == 0 && event.Text != "" {
				fullResponse.WriteString(event.Text)
				s.Append(map[string]any{"type": "chunk", "text": event.Text})
			}
			pendingSeparator = true

		case "tool_call":
			// Synthetic calls (a skill the user armed) are shown in the
			// transcript but not counted: the model made no such call, and the
			// stat is meant to report work it did.
			if !event.Synthetic {
				toolCallCount++
			}
			pendingSeparator = true
			mcpTools.annotate(event)
			s.Append(map[string]any{
				"type": "tool_call", "call_id": event.CallID,
				"tool": event.Tool, "args": event.Args,
				"mcp_server": event.McpServer,
			})

		case "tool_done":
			pendingSeparator = true
			mcpTools.annotate(event)
			s.Append(map[string]any{
				"type": "tool_done", "call_id": event.CallID,
				"tool": event.Tool, "success": event.Success, "output": event.Output,
				"mcp_server": event.McpServer,
			})

		case "tool_blocked":
			// A tool call opencode blocked via its permission system (an
			// "ask" rule auto-rejected in headless run, or an explicit
			// "deny"). Surfaced as a distinct frame so the UI shows a clear
			// "needs approval / denied" state. "output" carries the reason.
			pendingSeparator = true
			mcpTools.annotate(event)
			s.Append(map[string]any{
				"type": "tool_blocked", "call_id": event.CallID,
				"tool": event.Tool, "args": event.Args, "reason": event.Output,
				"mcp_server": event.McpServer,
			})

		case "question":
			sawQuestion = true
			s.Append(map[string]any{
				"type": "question", "question": event.Question, "choices": event.Choices,
			})

		case "permission":
			// An interactive permission request from the opencode server
			// backend. Surfaced as its own SSE frame so the UI can render an
			// allow/deny card; the browser replies via
			// POST /api/sessions/{id}/permission which routes to the
			// opencode server's permission-respond endpoint. Counts like a
			// question so an otherwise-silent turn isn't treated as "no
			// output".
			sawQuestion = true
			s.Append(map[string]any{
				"type":          "permission",
				"permission_id": event.PermissionID,
				"permission":    event.Permission,
				"patterns":      event.Patterns,
				"call_id":       event.CallID,
			})

		case "mcp_auth_required":
			s.Append(map[string]any{"type": "mcp_auth_required", "server": event.McpServer})

		case "step":
			// Lightweight heartbeat from the opencode backend so the UI can
			// surface that the agent is actively working even before any
			// tool call or final text arrives. Currently passed through as a
			// dedicated SSE frame; the frontend treats this as a progress
			// signal (and may show a short "Working…" label). No durable
			// state to update.
			s.Append(map[string]any{"type": "step"})

		case "result":
			if event.SessionID != "" && !detached {
				db.SetOpencodeSession(sessionID, event.SessionID)
			}
			modelKey := cfg.Model
			if modelKey == "" {
				modelKey = "default"
			}
			modelKey = "opencode:" + modelKey
			tokensOut := event.TokensOut
			if tokensOut == 0 {
				tokensOut = tokensOutAccum
			}
			if err := db.IncrementModelStats(
				modelKey, event.PremiumReqs, event.TokensIn, tokensOut,
				toolCallCount, event.FilesModified, event.LinesAdded, event.LinesRemoved,
				event.Cost,
			); err != nil {
				log.Printf("[ERROR] session=%s failed to update model stats: %v", sessionID, err)
			}
			if err := db.IncrementSessionStats(sessionID, toolCallCount); err != nil {
				log.Printf("[ERROR] session=%s failed to update session stats: %v", sessionID, err)
			}
			usagePayload := map[string]any{
				"type": "usage", "tokens_in": event.TokensIn, "tokens_out": tokensOut,
				"tokens_in_reported": event.TokensInKnown,
				"cost":               event.Cost,
				"premium_reqs":       event.PremiumReqs, "tool_calls": toolCallCount,
				"files_modified": event.FilesModified,
				"lines_added":    event.LinesAdded, "lines_removed": event.LinesRemoved,
			}
			// Keep the same payload for the assistant message row, so a reloaded
			// conversation shows the per-turn figures the live stream showed.
			// "type" is the SSE frame discriminator, not usage data.
			if b, err := json.Marshal(omitKey(usagePayload, "type")); err == nil {
				turnUsage = string(b)
			}
			s.Append(usagePayload)
		}
	}

	log.Printf("[SSE] session=%s dispatcher done, response_len=%d", sessionID, fullResponse.Len())

	// If the stream finished with no assistant output and no interactive events
	// (questions, tool calls), emit an error. This catches cases like invalid
	// model names where opencode returns a result with no content.
	if fullResponse.Len() == 0 && toolCallCount == 0 && !sawQuestion && !sawError {
		errMsg := "opencode returned no response (model may not exist or an authentication error occurred)"
		log.Printf("[ERROR] session=%s empty response with no tool calls or questions", sessionID)
		if !s.Finished() {
			failures = append(failures, errMsg)
		}
		s.Append(map[string]any{"type": "error", "message": errMsg})
	}

	// The same lines the browser adds live (stores/chat.ts), so a reload shows
	// what the user saw.
	for _, f := range failures {
		if !endsWithBlankLine(&fullResponse) {
			fullResponse.WriteString("\n\n")
		}
		fullResponse.WriteString("⚠ " + f)
	}

	if fullResponse.Len() > 0 {
		db.AddTurnMessage(sessionID, "assistant", fullResponse.String(), turnUsage, s.TurnID)
	}

	// Durable file storage checkpoint. Back up the workspace to its durable
	// remote at the end of the turn (after any agent file writes), so durable
	// state keeps pace with work. Runs in a goroutine so it never blocks the
	// dispatcher. No-ops for Temporary chats and for workspaces without a
	// configured durable remote (backup not provisioned yet).
	go durable.SyncChat(sessionID, cfg)

	s.Append(map[string]any{
		"type":      "done",
		"turn_id":   s.TurnID,
		"queue_len": QueueLen(sessionID),
	})
}

// IsStreaming reports whether a non-finished stream is currently registered
// for the session.
func IsStreaming(sessionID string) bool {
	val, ok := Streams.Load(sessionID)
	if !ok {
		return false
	}
	_, done := val.(*Stream).Snapshot()
	return !done
}

// WaitingOnOwner reports whether the session's running turn has stopped on the
// owner: its latest frame asks a question, a permission or a connector sign-in.
func WaitingOnOwner(sessionID string) bool {
	val, ok := Streams.Load(sessionID)
	if !ok {
		return false
	}
	s := val.(*Stream)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done || len(s.log) == 0 {
		return false
	}
	switch s.log[len(s.log)-1]["type"] {
	case "question", "permission", "mcp_auth_required":
		return true
	}
	return false
}

// GetStream returns the active or recently-finished stream for the session,
// if any. Useful for browsers reconnecting after a reload.
func GetStream(sessionID string) *Stream {
	val, ok := Streams.Load(sessionID)
	if !ok {
		return nil
	}
	return val.(*Stream)
}

// endsWithBlankLine returns true if the buffer already ends with a blank line
// (i.e. "\n\n"), so we don't add yet another separator on top of one the model
// already produced.
func endsWithBlankLine(b *strings.Builder) bool {
	s := b.String()
	if len(s) == 0 {
		return true
	}
	return strings.HasSuffix(s, "\n\n")
}
