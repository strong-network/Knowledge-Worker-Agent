// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package guest

import (
	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// errorForGuests replaces an error's text, which can carry paths and provider
// responses from the owner's workspace.
const errorForGuests = "The assistant ran into a problem."

// Frame is the allow-list every turn frame passes through on its way to a
// guest. It never mutates ev, which is shared with every
// other viewer of the turn. Unknown types are dropped: a frame added later
// stays private until someone decides otherwise.
func Frame(ev map[string]any) (map[string]any, bool) {
	t, _ := ev["type"].(string)
	switch t {
	case "session_id", "title", "step", "chunk", "done", "question":
		return ev, true
	case "tool_call":
		out := pick(ev, "type", "call_id", "tool")
		if tool, _ := ev["tool"].(string); chat.IsTodoTool(tool) {
			out["args"] = ev["args"] // the plan itself
		}
		return out, true
	case "tool_done":
		return pick(ev, "type", "call_id", "tool", "success"), true
	case "tool_blocked":
		return pick(ev, "type", "call_id", "tool"), true
	case "permission":
		return map[string]any{"type": "waiting_on_owner", "reason": "approval"}, true
	case "permission_answered":
		return pick(ev, "type", "permission_id", "response", "author_id", "author_name"), true
	case "mcp_auth_required":
		return map[string]any{"type": "waiting_on_owner", "reason": "connector"}, true
	case "error":
		return map[string]any{"type": "error", "message": errorForGuests}, true
	default: // usage, and anything not decided on
		return nil, false
	}
}

func pick(ev map[string]any, keys ...string) map[string]any {
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		if v, ok := ev[k]; ok {
			out[k] = v
		}
	}
	return out
}

// Message is a transcript entry as guests see it: no usage, which is the
// owner's spend.
type Message struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	CreatedAt  string `json:"created_at,omitempty"`
	AuthorID   string `json:"author_id,omitempty"`
	AuthorName string `json:"author_name,omitempty"`
	TurnID     string `json:"turn_id,omitempty"`
}

// History converts a session's stored messages for guests.
func History(msgs []db.ChatMessage) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, Message{Role: m.Role, Content: m.Content, CreatedAt: m.CreatedAt, AuthorID: m.AuthorID, AuthorName: m.AuthorName, TurnID: m.TurnID})
	}
	return out
}
