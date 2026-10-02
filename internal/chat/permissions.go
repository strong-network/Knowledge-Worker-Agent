// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"context"
	"maps"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodeserver"
)

// RespondPermission answers the opencode server; a variable so tests can stand in.
var RespondPermission = func(ctx context.Context, sessionID, permissionID, response string) (bool, error) {
	return opencodeserver.Permissions.Respond(ctx, sessionID, permissionID, response)
}

// PendingPermission returns the permission request the session's running turn
// is waiting on: the latest one nobody has answered. The frame is a copy.
func PendingPermission(sessionID string) (map[string]any, bool) {
	val, ok := Streams.Load(sessionID)
	if !ok {
		return nil, false
	}
	s := val.(*Stream)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return nil, false
	}
	answered := map[any]bool{}
	for i := len(s.log) - 1; i >= 0; i-- {
		switch ev := s.log[i]; ev["type"] {
		case "permission_answered":
			answered[ev["permission_id"]] = true
		case "permission":
			if !answered[ev["permission_id"]] {
				return maps.Clone(ev), true
			}
		}
	}
	return nil, false
}

// AnswerPermission answers the request the turn is waiting on, and only that
// one: the id reaches the opencode server's URL, so it is never taken on trust.
// The answer is recorded on the turn, so every viewer, owner and guests alike,
// stops offering a choice that has been made. It reports false when no request
// with that id is pending.
func AnswerPermission(ctx context.Context, sessionID, permissionID, response string, by Author) (bool, error) {
	pending, ok := PendingPermission(sessionID)
	if !ok || pending["permission_id"] != permissionID {
		return false, nil
	}
	ok, err := RespondPermission(ctx, sessionID, permissionID, response)
	if err != nil || !ok {
		return ok, err
	}
	if val, found := Streams.Load(sessionID); found {
		ev := map[string]any{"type": "permission_answered", "permission_id": permissionID, "response": response, "author_id": nil, "author_name": nil}
		if by.ID != "" {
			ev["author_id"], ev["author_name"] = by.ID, by.Name
		}
		val.(*Stream).Append(ev)
	}
	return true, nil
}
