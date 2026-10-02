// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package titler derives a short, human-meaningful title for a chat session
// from the user's first prompt, so the sidebar shows what a chat was about
// instead of the first few words of the prompt.
//
// It shells out to opencode's built-in, hidden `title` agent
// (`opencode run --agent title`), which ships a purpose-written prompt: a
// single line, <=50 characters, in the user's own language, with no tool names.
// Reusing that agent means we do not maintain a titling prompt of our own.
//
// The call is deliberately cheap and contained:
//
//   - it runs in an empty scratch directory, not the session workspace. The
//     title depends only on the prompt text, and running inside a real repo
//     makes opencode load that repo's context files (AGENTS.md and friends)
//     into the request — measured at ~7.7k input tokens versus ~20 in an empty
//     directory, for an identical title. The scratch directory also keeps repo
//     content out of the titling request entirely.
//   - the prompt is capped (see maxPromptChars) so a large paste as the first
//     message cannot turn into a large bill.
//   - the `title` agent denies all tools, so nothing in the prompt can cause a
//     tool to run.
//
// Failure is always silent: the caller keeps whatever label it already had.
package titler

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

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
)

const (
	// maxPromptChars caps how much of the first prompt is sent for titling.
	// A title only needs the opening intent, and this bounds the cost of a
	// large first paste (a 40KB paste measured $0.0021 uncapped versus
	// $0.0010 capped, for an equally good title).
	maxPromptChars = 2000

	// maxTitleChars is the longest title we will store. opencode's title
	// agent targets <=50 characters; this is a backstop against a model that
	// ignores the instruction, not the primary limit.
	maxTitleChars = 80

	// timeout bounds a single titling attempt. Observed runs complete in
	// 5-12s (dominated by opencode process startup, not inference), so this
	// is headroom rather than an expected wait.
	timeout = 90 * time.Second
)

// agentFallbackMarker is the warning opencode prints when `--agent <name>`
// does not resolve. It then silently runs the *default* agent instead, which
// would be a full tool-enabled turn rather than a cheap title. Seeing this
// line means the output must not be trusted as a title.
const agentFallbackMarker = "Falling back to default agent"

// runTitle executes the titling command and returns its raw stdout+stderr.
// It is a package-level variable so tests can substitute a fake without
// invoking the real opencode binary.
var runTitle = execTitle

// Enabled reports whether automatic titling is turned on. It is on by default
// and disabled by setting KWA_AUTO_TITLE to a falsey value.
func Enabled() bool {
	switch strings.ToLower(strings.TrimSpace(env.Get("KWA_AUTO_TITLE"))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// Generate derives a title for prompt using the given model, which must be a
// resolved, runnable model id (see config.ResolveModel) — opencode has no
// built-in default provider, so an empty model would leave the run with no
// provider selected.
//
// It returns an error when the title cannot be trusted: the agent did not
// resolve, the run reported an error, or no text was produced.
func Generate(ctx context.Context, bin, model, prompt string) (string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", fmt.Errorf("empty prompt")
	}
	if strings.TrimSpace(model) == "" {
		return "", fmt.Errorf("no model resolved")
	}
	if len(prompt) > maxPromptChars {
		prompt = prompt[:maxPromptChars]
	}

	out, err := runTitle(ctx, bin, model, prompt)
	if err != nil && len(out) == 0 {
		return "", err
	}

	// `--agent title` not resolving is not a soft failure: opencode falls back
	// to the default agent, which is a full tool-enabled turn. Reject it.
	if bytes.Contains(out, []byte(agentFallbackMarker)) {
		return "", fmt.Errorf("title agent unavailable")
	}

	title, opencodeSession, runErr := parseTitleOutput(out)

	// Best-effort cleanup of the throwaway session opencode created for this
	// one-shot run, so title generation does not accumulate sessions in
	// opencode's own storage. Never blocks and never fails the title.
	if opencodeSession != "" {
		go deleteOpencodeSession(bin, opencodeSession)
	}

	if runErr != "" {
		return "", fmt.Errorf("title run reported an error: %s", runErr)
	}
	title = cleanTitle(title)
	if title == "" {
		return "", fmt.Errorf("no title produced")
	}
	return title, nil
}

// execTitle runs the real opencode binary in an empty scratch directory.
func execTitle(ctx context.Context, bin, model, prompt string) ([]byte, error) {
	// A dedicated empty directory keeps the session workspace's context files
	// out of the request (see package doc) and gives the run nothing to read.
	scratch, err := os.MkdirTemp("", "chat-title-")
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

// parseTitleOutput scans opencode's JSONL run output. It returns the last text
// part (the title), the opencode session id, and any reported error message.
//
// Non-JSON lines are ignored: opencode prints human-readable warnings to the
// same stream, and a run that both warns and succeeds is still parseable.
func parseTitleOutput(out []byte) (title, sessionID, runErr string) {
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
				title = t
			}
		case "error":
			// A failed run (e.g. an unavailable model) exits 0 and reports
			// only this event, so the exit code cannot be relied on.
			if msg := strings.TrimSpace(ev.Error.Data.Message); msg != "" {
				runErr = msg
			} else if ev.Error.Name != "" {
				runErr = ev.Error.Name
			} else {
				runErr = "unknown error"
			}
		}
	}
	return title, sessionID, runErr
}

// cleanTitle reduces a model response to a single presentable line.
func cleanTitle(s string) string {
	// Some models wrap their thinking in <think> blocks.
	if i := strings.Index(s, "</think>"); i >= 0 {
		s = s[i+len("</think>"):]
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		// Models occasionally quote the title or lead with a markdown bullet.
		line = strings.TrimLeft(line, "-*# ")
		line = strings.Trim(line, "\"'`")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len([]rune(line)) > maxTitleChars {
			line = string([]rune(line)[:maxTitleChars-1]) + "…"
		}
		return line
	}
	return ""
}

// deleteOpencodeSession removes the throwaway session created by a titling run.
func deleteOpencodeSession(bin, sessionID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, bin, "session", "delete", sessionID).Run(); err != nil {
		log.Printf("[TITLE] could not delete throwaway opencode session %s: %v", sessionID, err)
	}
}
