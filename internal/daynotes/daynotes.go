// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package daynotes generates a short factual note describing one day of one
// chat session, so a search over past work can retrieve what a chat *became*
// rather than only what it opened with.
//
// The problem it solves is measurable on the real corpus: session 3ffbfb3d is
// titled "SDS Chat - New", opens by asking to read some spec files, and then
// runs to 713 messages over 32 days covering backup coverage, agent
// configuration, file panels and central configuration. The free digest (title + first user
// message) describes none of that. Notes are generated only where that
// breakdown actually happens -- see db.PendingNoteDays -- not for every chat.
//
// # Why the built-in `title` agent
//
// `opencode run` has no `--tools` flag, so the only way to deny tools to a
// one-shot generation is to name an agent. A *custom* agent would work, but
// measured against opencode v1.18.30 every custom agent also carries opencode's
// full coding-agent system prompt: ~49.4k tokens, or ~41.7k with every tool
// disabled in frontmatter. Nothing removes the rest -- neither a `prompt:`
// override nor `options.systemPrompt` replaces it.
//
// opencode's built-in `title` agent is special-cased with a small prompt and no
// tools. On a real 12k-character day, measured side by side on the same input:
//
//	--agent title     input 3,667  cost $0.0044
//	custom agent      input 3,328 + 41,423 cached  cost $0.0090
//
// with no discernible difference in the notes produced. So this package reuses
// the same agent internal/titler does. Its prompt is small enough that the
// caller's instruction dominates: asked for a 120-word note, it returns a
// 120-word note, not a title.
//
// The call is contained the same way titling is: an empty scratch directory so
// no repo context files are loaded, a capped transcript so one enormous day
// cannot turn into an enormous bill, no stdin, and a timeout. Failure is always
// silent -- a missing note is the status quo, and the day stays in the backlog.
package daynotes

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

const (
	// maxTranscriptChars caps how much of a day is sent. Measured, 12.3k
	// characters cost $0.0044 on the small model; this leaves room for a busy
	// day without letting a single paste-heavy one dominate the backlog's cost.
	maxTranscriptChars = 16000

	// maxMessageChars caps any single message's contribution, so one giant
	// paste cannot crowd out the rest of the day. A day is summarised from the
	// shape of the conversation, not from the full text of its attachments.
	maxMessageChars = 1200

	// maxNoteChars is a backstop against a model that ignores the word limit.
	// The prompt asks for 120 words; this is roughly twice that.
	maxNoteChars = 1500

	// timeout bounds one note. Runs are dominated by opencode process startup
	// rather than inference, so this is headroom, not an expected wait.
	timeout = 120 * time.Second
)

// agentFallbackMarker is the warning opencode prints when `--agent <name>` does
// not resolve. It then silently runs the *default* agent instead -- a full,
// tool-enabled turn in a scratch directory. Seeing this line means the output
// is not a note and must not be stored.
const agentFallbackMarker = "Falling back to default agent"

// PromptVersion is stamped on every note this package writes. Bump it when the
// instruction below changes materially and existing notes should be rewritten.
//
// This is the only escape hatch from the append-only rule, and it exists
// because notes are otherwise permanent: nothing rewrites a row once written,
// so a weak instruction would leave every workspace carrying its output for
// good. Raising this number re-offers days whose note was written by an older
// prompt (db.PendingNoteDays) and lets the new note replace the old one
// (db.SaveSessionNote). Replacement is strictly one-directional -- an equal or
// older version never overwrites a newer note.
//
// Bumping it re-runs the whole corpus at the usual per-note cost, drained at
// the same capped rate as a first backfill, so it is a deliberate act rather
// than a free one. Days whose session has since been deleted keep their
// original note: the messages are gone, so there is nothing to regenerate from.
const PromptVersion = 1

// instruction is the note prompt. It asks for specifics because the whole point
// of a note is to carry the nouns -- files, features, decisions -- that the
// title and opening message do not. "Do not add a preamble" matters: without
// it, models open with "This conversation covers...", which wastes the budget
// and reads badly when a dozen notes are listed together.
const instruction = `Below is one day of a chat session between a user and a coding agent.

Write a factual note of at most 120 words recording what was worked on, what was decided, and what was left unfinished or open. Name the specific files, features, tickets and decisions involved. Prefer concrete nouns over general description.

Write only the note itself: no preamble, no title, no bullet points, no closing summary. If the day contains no substantive work, say so in one short sentence.`

// runNote executes the note command and returns its raw stdout+stderr. It is a
// package-level variable so tests can substitute a fake rather than invoking
// the real opencode binary.
var runNote = execNote

// Generate produces the note text for one day of one session.
//
// model must be a resolved, runnable model id: opencode has no built-in default
// provider, so an empty model leaves the run with nothing selected.
//
// It returns an error whenever the result cannot be trusted -- the agent did not
// resolve, the run reported an error, or no text came back -- so that the caller
// stores nothing and the day is retried on a later pass.
func Generate(ctx context.Context, bin, model string, msgs []db.RecallMessage) (string, error) {
	if strings.TrimSpace(model) == "" {
		return "", fmt.Errorf("no model resolved")
	}
	transcript := BuildTranscript(msgs)
	if transcript == "" {
		return "", fmt.Errorf("no content to summarise")
	}

	out, err := runNote(ctx, bin, model, instruction+"\n\n--- TRANSCRIPT ---\n"+transcript+"\n--- END TRANSCRIPT ---")
	if err != nil && len(out) == 0 {
		return "", err
	}
	if bytes.Contains(out, []byte(agentFallbackMarker)) {
		return "", fmt.Errorf("title agent unavailable")
	}

	note, opencodeSession, runErr := parseNoteOutput(out)

	// Best-effort cleanup of the throwaway session opencode created for this
	// one-shot run, so the backlog does not accumulate hundreds of sessions in
	// opencode's own storage. Never blocks and never fails the note.
	if opencodeSession != "" {
		go deleteOpencodeSession(bin, opencodeSession)
	}

	if runErr != "" {
		return "", fmt.Errorf("note run reported an error: %s", runErr)
	}
	note = cleanNote(note)
	if note == "" {
		return "", fmt.Errorf("no note produced")
	}
	return note, nil
}

// BuildTranscript renders a day's messages into the text sent to the model.
//
// When the day does not fit in the budget it drops messages from the MIDDLE and
// says so inline, rather than truncating the tail. A day's ending -- what was
// concluded, what was left open -- is the part a note most needs, and a plain
// prefix cut would silently discard exactly that while still producing a
// confident-sounding note about the morning.
func BuildTranscript(msgs []db.RecallMessage) string {
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		if len(content) > maxMessageChars {
			content = content[:maxMessageChars] + " […]"
		}
		role := strings.ToUpper(strings.TrimSpace(m.Role))
		if role == "" {
			role = "UNKNOWN"
		}
		parts = append(parts, role+": "+content)
	}
	if len(parts) == 0 {
		return ""
	}

	if total := joinedLen(parts); total <= maxTranscriptChars {
		return strings.Join(parts, "\n\n")
	}

	// Grow a head and a tail alternately until the budget is spent, so both
	// ends of the day survive.
	head, tail := 0, 0
	used := 0
	for head+tail < len(parts) {
		next := parts[head]
		fromTail := false
		if head > tail {
			next = parts[len(parts)-1-tail]
			fromTail = true
		}
		if used+len(next)+2 > maxTranscriptChars {
			break
		}
		used += len(next) + 2
		if fromTail {
			tail++
		} else {
			head++
		}
	}
	omitted := len(parts) - head - tail
	if omitted <= 0 {
		return strings.Join(parts, "\n\n")
	}

	kept := make([]string, 0, head+tail+1)
	kept = append(kept, parts[:head]...)
	kept = append(kept, fmt.Sprintf("[… %d messages omitted from the middle of this day …]", omitted))
	kept = append(kept, parts[len(parts)-tail:]...)
	return strings.Join(kept, "\n\n")
}

func joinedLen(parts []string) int {
	n := 0
	for _, p := range parts {
		n += len(p) + 2
	}
	return n
}

// execNote runs the real opencode binary in an empty scratch directory.
func execNote(ctx context.Context, bin, model, prompt string) ([]byte, error) {
	// A dedicated empty directory keeps the workspace's context files
	// (AGENTS.md and friends) out of the request. internal/titler measured
	// that at ~7.7k input tokens versus ~20 for an identical result.
	scratch, err := os.MkdirTemp("", "chat-daynote-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)

	cmd := exec.CommandContext(ctx, bin,
		"run", "--agent", "title", "--model", model, "--format", "json", prompt)
	cmd.Dir = scratch
	// Leave stdin nil: `opencode run` reads stdin when it is attached, which
	// would let it consume the parent's input.
	return cmd.CombinedOutput()
}

// parseNoteOutput scans opencode's JSONL run output. It returns the last text
// part (the note), the opencode session id, and any reported error message.
//
// This deliberately does not share internal/titler's parser. The two features
// call the same agent for different reasons and validate the result against
// different rules; coupling them would mean a change to titling could silently
// change what gets written into the permanent note record.
func parseNoteOutput(out []byte) (note, sessionID, runErr string) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionID"`
			Part      struct {
				Text string `json:"text"`
			} `json:"part"`
			Error struct {
				Name string `json:"name"`
				Data struct {
					Message string `json:"message"`
				} `json:"data"`
			} `json:"error"`
		}
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if ev.SessionID != "" {
			sessionID = ev.SessionID
		}
		switch ev.Type {
		case "text":
			if t := strings.TrimSpace(ev.Part.Text); t != "" {
				note = t
			}
		case "error":
			// A failed run (e.g. an unavailable model) exits 0 and reports only
			// this event, so the exit code cannot be relied on.
			if msg := strings.TrimSpace(ev.Error.Data.Message); msg != "" {
				runErr = msg
			} else if ev.Error.Name != "" {
				runErr = ev.Error.Name
			} else {
				runErr = "unknown error"
			}
		}
	}
	return note, sessionID, runErr
}

// cleanNote reduces a model response to storable prose.
func cleanNote(s string) string {
	// Some models wrap their reasoning in <think> blocks.
	if i := strings.LastIndex(s, "</think>"); i >= 0 {
		s = s[i+len("</think>"):]
	}
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'`")
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxNoteChars {
		s = string(r[:maxNoteChars-1]) + "…"
	}
	return s
}

// deleteOpencodeSession removes the throwaway session created by a note run.
func deleteOpencodeSession(bin, sessionID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, bin, "session", "delete", sessionID).Run(); err != nil {
		log.Printf("[DAYNOTE] could not delete throwaway opencode session %s: %v", sessionID, err)
	}
}
