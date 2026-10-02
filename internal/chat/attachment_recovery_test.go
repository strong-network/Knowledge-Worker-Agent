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

func TestIsUnsupportedAttachmentError(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want bool
	}{
		{
			// Verbatim from a reproduction against opencode v1.18.16 with the
			// github-copilot provider.
			name: "real provider rejection",
			msg:  "'file part media type application/pdf' functionality not supported.",
			want: true,
		},
		{
			// The guard must not be PDF-specific: any media type the provider
			// refuses poisons the history in exactly the same way.
			name: "other media type",
			msg:  "'file part media type audio/mpeg' functionality not supported.",
			want: true,
		},
		{
			name: "case insensitive",
			msg:  "'File Part Media Type application/pdf' Functionality Not Supported.",
			want: true,
		},
		{
			// Both markers are required precisely so this kind of error never
			// silently resets someone's conversation.
			name: "unrelated not-supported error",
			msg:  "model gpt-4.1 is not supported by this provider",
			want: false,
		},
		{
			name: "unrelated media type mention",
			msg:  "could not determine media type for upload",
			want: false,
		},
		{name: "empty", msg: "", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUnsupportedAttachmentError(tc.msg); got != tc.want {
				t.Errorf("isUnsupportedAttachmentError(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}

// A session already carrying the poisoned attachment must be detached from its
// opencode session, otherwise every later turn replays the bad part and fails.
func TestRecoverPoisonedSessionDetaches(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if err := db.Init(dbPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	const sessionID = "sess-poisoned"
	if err := db.CreateSession(sessionID, config.DefaultSessionConfig()); err != nil {
		t.Fatal(err)
	}
	if err := db.SetOpencodeSession(sessionID, "ses_poisoned123"); err != nil {
		t.Fatal(err)
	}

	msg := recoverPoisonedSession(sessionID, "'file part media type application/pdf' functionality not supported.")

	if got := db.GetOpencodeSession(sessionID); got != "" {
		t.Errorf("opencode session = %q, want empty so the next turn starts fresh", got)
	}
	// The user has to be told the thread was restarted; otherwise the assistant
	// losing context looks like a second, stranger bug.
	if !strings.Contains(strings.ToLower(msg), "restarted") {
		t.Errorf("message does not mention the conversation restart: %q", msg)
	}
	if strings.Contains(msg, "file part media type") {
		t.Errorf("raw provider error leaked to the user: %q", msg)
	}
}

// The failing turn itself has no opencode session yet. There is nothing to
// escape, so the user gets the explanation without a misleading "restarted".
func TestRecoverPoisonedSessionWithoutAttachedSession(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if err := db.Init(dbPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	const sessionID = "sess-fresh"
	if err := db.CreateSession(sessionID, config.DefaultSessionConfig()); err != nil {
		t.Fatal(err)
	}

	msg := recoverPoisonedSession(sessionID, "'file part media type application/pdf' functionality not supported.")

	if strings.Contains(strings.ToLower(msg), "restarted") {
		t.Errorf("claimed a restart that did not happen: %q", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "pdf") {
		t.Errorf("message should explain the unreadable file type: %q", msg)
	}
}

// The guard stops a PDF being *attached*; it does not stop the agent working
// with one. Since the `pdf` skill exists, a message that only says "convert it
// yourself" now understates what the product can do, so the recovery notice
// has to name the skill.
func TestRecoverPoisonedSessionPointsAtPdfSkill(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if err := db.Init(dbPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	const sessionID = "sess-skill-hint"
	if err := db.CreateSession(sessionID, config.DefaultSessionConfig()); err != nil {
		t.Fatal(err)
	}

	msg := strings.ToLower(recoverPoisonedSession(sessionID, "'file part media type application/pdf' functionality not supported."))

	if !strings.Contains(msg, "pdf skill") {
		t.Errorf("message does not point the user at the pdf skill: %q", msg)
	}
	// Naming the skill is not enough — the user needs to know it is worth
	// asking for, so the message must say what it actually gets them.
	for _, want := range []string{"text", "image"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not say the skill can produce %q: %q", want, msg)
		}
	}
	// The fallback stays for deployments without the skill installed.
	if !strings.Contains(msg, "paste") {
		t.Errorf("message dropped the manual fallback: %q", msg)
	}
}
