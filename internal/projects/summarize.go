// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package projects

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/git"
)

// summaryFileName is the single consolidated file the summarizer writes into
// the project workspace. It is (re)written on each run so the project always
// has one current summary rather than accumulating timestamped copies.
const summaryFileName = "PROJECT_SUMMARY.md"

// activeWindow defines "active within the last week" for pre-selection.
const activeWindow = 7 * 24 * time.Hour

// summarizeTimeout bounds the whole summarization turn.
const summarizeTimeout = 5 * time.Minute

// SummarizeCandidate is one selectable chat in the summarize dialog.
type SummarizeCandidate struct {
	SessionID       string `json:"session_id"`
	Title           string `json:"title"`
	Messages        int    `json:"messages"`
	UpdatedAt       string `json:"updated_at"`
	ActiveLastWeek  bool   `json:"active_last_week"`
}

// HandleSummarizeCandidates — GET /api/projects/{id}/summarize/candidates.
// Lists the project's chats with an "active in the last week" flag so the UI
// can pre-select recent chats while still showing (unchecked) older ones.
func HandleSummarizeCandidates(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := db.GetProject(id)
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "project not found"})
		return
	}
	cutoff := time.Now().Add(-activeWindow)
	items := db.ListProjectSessions(id)
	out := make([]SummarizeCandidate, 0, len(items))
	for _, s := range items {
		// Skip empty chats (nothing to summarize).
		if s.Messages == 0 {
			continue
		}
		out = append(out, SummarizeCandidate{
			SessionID:      s.SessionID,
			Title:          titleOrDefault(s.Label),
			Messages:       s.Messages,
			UpdatedAt:      s.UpdatedAt,
			ActiveLastWeek: isRecent(s.UpdatedAt, cutoff),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"chats": out})
}

type summarizeRequest struct {
	SessionIDs   []string `json:"session_ids"`
	Instructions string   `json:"instructions"`
}

// HandleSummarize — POST /api/projects/{id}/summarize.
// Builds a transcript from the selected chats, runs one LLM turn that writes a
// consolidated PROJECT_SUMMARY.md into the project's shared workspace, then (if
// the workspace is a git repo) commits and pushes it.
func HandleSummarize(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := db.GetProject(id)
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "project not found"})
		return
	}
	workspace := strings.TrimSpace(p.WorkspacePath)
	if workspace == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "project has no workspace"})
		return
	}

	var req summarizeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json body"})
		return
	}
	r.Body.Close()
	if len(req.SessionIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "select at least one chat"})
		return
	}

	// Build a labeled transcript from the selected chats (that belong to this
	// project). Skip chats that aren't in the project or have no messages.
	transcript, included := buildTranscript(id, req.SessionIDs)
	if included == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "selected chats have no content to summarize"})
		return
	}

	prompt := buildSummarizePrompt(p.Name, p.Description, req.Instructions, transcript)

	// Run the summarization as a normal turn whose working directory is the
	// shared project workspace, so the agent writes the summary file there.
	cfg := config.DefaultSessionConfig()
	cfg.Backend = config.NormalizeBackend(cfg.Backend)
	cfg.Workdir = workspace
	cfg.Yolo = true // auto-approve the file write; this is a server-initiated action
	// Named deliberately: this chat is a project artifact, not a user
	// conversation, so automatic titling must leave its label alone.
	cfg.Label = "Project summary"
	cfg.LabelManual = true
	cfg.ProjectInstructions = p.Instructions

	sid := chat.NewUUID()
	if err := db.CreateSession(sid, cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	// Link the summarization chat to the project so its output/history stays
	// with the project (and the user can inspect what was summarized).
	_ = db.SetSessionProject(sid, id)

	stream := chat.StartChatStream(sid, prompt, cfg)

	// Do NOT block the request on the turn. Return immediately with the chat's
	// session id so the UI can open the summarization chat and watch progress
	// live. When the turn finishes, a background goroutine auto-commits and
	// pushes the produced summary file.
	go finishSummarize(stream, workspace, sid)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"session_id":     sid,
		"chats_included": included,
		"file":           summaryFileName,
	})
}

// finishSummarize waits (bounded) for the summarization turn to complete, then
// commits + pushes the produced summary file. Runs in a background goroutine,
// decoupled from the HTTP request, so the modal doesn't block.
func finishSummarize(stream *chat.Stream, workspace, sid string) {
	ctx, cancel := context.WithTimeout(context.Background(), summarizeTimeout)
	defer cancel()
	idx := 0
	for {
		_, next, done := stream.Wait(ctx, idx)
		idx = next
		if done {
			break
		}
		if ctx.Err() != nil {
			log.Printf("[summarize] session=%s turn timed out before commit", sid)
			return
		}
	}

	// Only commit if the summary file was actually produced.
	summaryPath := filepath.Join(workspace, summaryFileName)
	if _, err := os.Stat(summaryPath); err != nil {
		log.Printf("[summarize] session=%s finished but no %s was produced", sid, summaryFileName)
		return
	}
	if out, err := git.CommitAndPush(workspace, "Add project summary ("+summaryFileName+")"); err != nil {
		log.Printf("[summarize] session=%s commit/push failed: %v\n%s", sid, err, out)
		return
	}
	log.Printf("[summarize] session=%s committed & pushed %s", sid, summaryFileName)
}

// buildTranscript concatenates the selected chats' messages into a labeled
// transcript, returning the text and the number of chats actually included.
func buildTranscript(projectID string, sessionIDs []string) (string, int) {
	var b strings.Builder
	included := 0
	for _, sid := range sessionIDs {
		// Only summarize chats that belong to this project.
		if db.GetSessionProject(sid) != projectID {
			continue
		}
		msgs := db.GetMessages(sid)
		if len(msgs) == 0 {
			continue
		}
		included++
		title := titleOrDefault(sessionTitle(sid))
		fmt.Fprintf(&b, "\n===== CHAT: %s =====\n", title)
		for _, m := range msgs {
			role := "User"
			if m.Role == "assistant" {
				role = "Assistant"
			}
			fmt.Fprintf(&b, "\n[%s]\n%s\n", role, strings.TrimSpace(m.Content))
		}
	}
	return b.String(), included
}

// buildSummarizePrompt composes the instruction for the summarization turn.
func buildSummarizePrompt(projectName, projectDesc, extra, transcript string) string {
	var b strings.Builder
	b.WriteString("You are consolidating the important context from several chats in a project so it can be reused by future chats and teammates.\n\n")
	b.WriteString("Project: " + projectName + "\n")
	if strings.TrimSpace(projectDesc) != "" {
		b.WriteString("Project description: " + projectDesc + "\n")
	}
	b.WriteString("\nWrite a single consolidated Markdown file named `" + summaryFileName + "` in the current working directory (overwrite it if it already exists). Capture the durable, important context — key decisions, requirements, facts, artifacts produced, and open questions — organized under clear headings. Be concise and factual; omit chit-chat and transient debugging. Do not include secrets.\n")
	if strings.TrimSpace(extra) != "" {
		b.WriteString("\nAdditional instructions from the user:\n" + strings.TrimSpace(extra) + "\n")
	}
	b.WriteString("\nHere are the conversations to summarize:\n")
	b.WriteString(transcript)
	b.WriteString("\n\nWhen done, the file `" + summaryFileName + "` must exist in the working directory with the consolidated summary.")
	return b.String()
}

// ── small helpers ──

func titleOrDefault(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "Untitled chat"
	}
	return s
}

// sessionTitle reads a session's label from its stored config.
func sessionTitle(sid string) string {
	cfg, err := db.GetSessionConfig(sid)
	if err != nil {
		return ""
	}
	return cfg.Label
}

// isRecent reports whether an updated_at timestamp (SQLite CURRENT_TIMESTAMP
// format, UTC) is at or after the cutoff.
func isRecent(updatedAt string, cutoff time.Time) bool {
	t := parseDBTime(updatedAt)
	if t.IsZero() {
		return false
	}
	return !t.Before(cutoff)
}

// parseDBTime parses SQLite's CURRENT_TIMESTAMP ("2006-01-02 15:04:05", UTC),
// with a couple of RFC3339 fallbacks.
func parseDBTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
