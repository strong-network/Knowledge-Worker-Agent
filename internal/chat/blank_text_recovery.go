// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"log"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// A whitespace-only text block in a chat's OpenCode history makes the provider
// reject every later turn, since each turn replays the history.
// plugin/blank-text-guard.js leaves such blocks out of each request; this is the
// fallback when the error comes through anyway, for example if the plugin didn't
// load: the chat moves to a fresh OpenCode session instead of staying stuck.

const blankTextMarker = "text content blocks must contain non-whitespace text"

// isBlankTextError reports whether an error from the chat backend is the
// provider rejecting a whitespace-only text block.
func isBlankTextError(message string) bool {
	return strings.Contains(strings.ToLower(message), blankTextMarker)
}

// recoverBlankTextSession detaches sessionID from its OpenCode session, so the
// next turn starts a fresh one, and returns the message to show the user in
// place of the provider's.
func recoverBlankTextSession(sessionID, original string) string {
	if db.GetOpencodeSession(sessionID) == "" {
		// No history to escape: this is the failing turn itself.
		return original
	}
	if err := db.SetOpencodeSession(sessionID, ""); err != nil {
		log.Printf("[ERROR] session=%s failed to detach opencode session after a blank text block: %v", sessionID, err)
		return original
	}
	log.Printf("[DEBUG] session=%s detached from opencode session after a blank text block", sessionID)
	return "An earlier reply in this chat left a blank block that every new message was failing on, " +
		"so I've restarted the conversation. Your messages above are still here, but I've lost the " +
		"earlier context. Send your message again, with anything I need to know from before."
}
