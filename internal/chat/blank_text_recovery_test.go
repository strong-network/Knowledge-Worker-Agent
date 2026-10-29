// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// Verbatim from a reproduction with opencode 1.19.4 and github-copilot/claude-haiku-4.5:
// the provider's JSON body arrives as the error's message.
const blankTextErr = `{"error":{"message":"messages: text content blocks must contain non-whitespace text","code":"invalid_request_body"}}`

func TestIsBlankTextError(t *testing.T) {
	for msg, want := range map[string]bool{
		blankTextErr: true,
		"messages: text content blocks must contain non-whitespace text":      true,
		"messages: text content blocks must be non-empty":                     false,
		"'file part media type application/pdf' functionality not supported.": false,
		"": false,
	} {
		if got := isBlankTextError(msg); got != want {
			t.Errorf("isBlankTextError(%q) = %v, want %v", msg, got, want)
		}
	}
}

func blankTextTestDB(t *testing.T, opencodeSession string) string {
	t.Helper()
	if err := db.Init(filepath.Join(t.TempDir(), "test.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	const sid = "sess-blank"
	if err := db.CreateSession(sid, config.DefaultSessionConfig()); err != nil {
		t.Fatal(err)
	}
	if opencodeSession != "" {
		if err := db.SetOpencodeSession(sid, opencodeSession); err != nil {
			t.Fatal(err)
		}
	}
	return sid
}

// A chat whose history holds the blank block moves to a fresh opencode session,
// and the user is told why the agent no longer remembers the thread.
func TestRecoverBlankTextSessionDetaches(t *testing.T) {
	sid := blankTextTestDB(t, "ses_poisoned")
	msg := recoverBlankTextSession(sid, blankTextErr)
	if got := db.GetOpencodeSession(sid); got != "" {
		t.Errorf("opencode session = %q, want empty so the next turn starts fresh", got)
	}
	if !strings.Contains(msg, "restarted") || !strings.Contains(msg, "Send your message again") {
		t.Errorf("message doesn't say the conversation restarted and the message needs sending again: %q", msg)
	}
	if strings.Contains(msg, "invalid_request_body") {
		t.Errorf("raw provider error leaked to the user: %q", msg)
	}
}

// With no opencode session there's no history to escape, so nothing claims a restart.
func TestRecoverBlankTextSessionWithoutSession(t *testing.T) {
	sid := blankTextTestDB(t, "")
	if msg := recoverBlankTextSession(sid, blankTextErr); msg != blankTextErr {
		t.Errorf("message = %q, want the provider's unchanged", msg)
	}
}

// Through the dispatcher: the result event that follows the error carries the
// poisoned session's id, which must not re-attach it.
func TestBlankTextErrorLeavesTheChatOnAFreshSession(t *testing.T) {
	saved, frames := runTurn(t, nil,
		func(*Stream) {
			if err := db.SetOpencodeSession("sess-failure", "ses_poisoned"); err != nil {
				t.Fatal(err)
			}
		},
		&Event{Kind: "error", Text: blankTextErr},
		&Event{Kind: "result", SessionID: "ses_poisoned"},
	)
	if got := db.GetOpencodeSession("sess-failure"); got != "" {
		t.Errorf("opencode session = %q after the turn, want empty", got)
	}
	if !strings.Contains(saved, "restarted the conversation") || strings.Contains(saved, "invalid_request_body") {
		t.Errorf("saved reply = %q, want the restart notice instead of the provider's error", saved)
	}
	if !hasFrame(frames, "error") {
		t.Error("the turn shows no error")
	}
}
