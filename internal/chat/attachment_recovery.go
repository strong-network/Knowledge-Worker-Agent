// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"fmt"
	"log"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// Recovery for chat sessions bricked by an attachment the model provider
// cannot accept.
//
// OpenCode's `read` tool attaches PDFs to the conversation as binary file
// parts without checking whether the provider accepts that media type. The
// github-copilot provider — the only one behind the models we offer — accepts
// only image/*, so the AI SDK rejects the turn with:
//
//	'file part media type application/pdf' functionality not supported.
//
// The damaging part is that OpenCode persists the attachment in its own
// session history and replays that history on every turn. The session is then
// permanently broken: even a message with no attachment fails, because the
// poison is in the replayed history rather than in the new message.
//
// plugin/pdf-guard.js (installed into the opencode config dir at startup)
// stops this happening again by dropping such attachments before they are
// stored. This is the other half: sessions that were already poisoned before
// the guard existed carry the bad part forever, and no future prompt can clear
// it.
//
// The recovery is to detach our chat session from the poisoned OpenCode
// session so the next turn starts a fresh one. Deliberately NOT used:
// OpenCode's /session/{id}/revert, which restores a file snapshot and would
// roll back files the agent wrote earlier in the session. Losing model-side
// context is recoverable; silently reverting someone's files is not.

// unsupportedAttachmentMarkers identifies the provider rejection above.
//
// Matching on the message text is unpleasant but it is what we get: the error
// arrives as a plain string on OpenCode's event stream with no code or type to
// key on. Both fragments must be present, so an unrelated error that happens
// to say "not supported" doesn't reset somebody's session.
var unsupportedAttachmentMarkers = []string{"file part media type", "not supported"}

// isUnsupportedAttachmentError reports whether an error from the chat backend
// is the unsupported-attachment rejection that poisons session history.
func isUnsupportedAttachmentError(message string) bool {
	lower := strings.ToLower(message)
	for _, marker := range unsupportedAttachmentMarkers {
		if !strings.Contains(lower, marker) {
			return false
		}
	}
	return true
}

// recoverPoisonedSession detaches sessionID from its OpenCode session so the
// next turn starts a clean one, and returns the message to show the user in
// place of the raw provider error.
//
// The returned text has to do three jobs: explain that the file could not be
// attached (so the failure isn't mysterious), point at the `pdf` skill (so the
// user knows this is a limit on *attaching* a PDF, not on working with one),
// and say that the conversation context was dropped (so the assistant suddenly
// not remembering the thread isn't a second, worse mystery).
func recoverPoisonedSession(sessionID, original string) string {
	notice := "This chat can't attach that file directly: the current models can only " +
		"read images, not PDFs or other binary attachments.\n\n" +
		"The file is untouched on disk. For a PDF, ask me to use the **pdf skill** — it " +
		"extracts the text, searches the document, or renders pages as images I can look " +
		"at. Otherwise, convert the file to text or images, or paste the relevant part."

	if db.GetOpencodeSession(sessionID) == "" {
		// Nothing attached yet, so there is no poisoned history to escape:
		// this is the failing turn itself, not a later one inheriting it.
		return notice
	}
	if err := db.SetOpencodeSession(sessionID, ""); err != nil {
		log.Printf("[ERROR] session=%s failed to detach poisoned opencode session: %v", sessionID, err)
		// Be honest rather than promising a recovery that didn't happen.
		return fmt.Sprintf("%s\n\n%s", notice, original)
	}

	log.Printf("[DEBUG] session=%s detached from poisoned opencode session after unsupported attachment", sessionID)
	return notice + "\n\nEarlier messages in this chat were replaying that file and " +
		"failing, so this conversation has been restarted. Your message history above " +
		"is intact, but I've lost the earlier context — please re-state anything I need."
}
