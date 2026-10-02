// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/durable"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/workdirs"
)

func HandleSessions(w http.ResponseWriter, r *http.Request) {
	items := db.ListSessions()
	// Augment with live streaming flag so clients know which sessions to
	// re-attach after a reload.
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		raw, _ := json.Marshal(it)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		m["streaming"] = chat.IsStreaming(it.SessionID)
		m["present"] = len(chat.Present(it.SessionID))
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, out)
}

// HandleActiveStreams returns the IDs of sessions with an in-flight stream.
func HandleActiveStreams(w http.ResponseWriter, r *http.Request) {
	ids := []string{}
	chat.Streams.Range(func(k, v any) bool {
		s := v.(*chat.Stream)
		if _, done := s.Snapshot(); !done {
			ids = append(ids, k.(string))
		}
		return true
	})
	writeJSON(w, http.StatusOK, map[string]any{"sessions": ids})
}

func HandleNewSession(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	r.Body.Close()

	cfg := config.DefaultSessionConfig()
	if body != nil {
		raw, _ := json.Marshal(body)
		json.Unmarshal(raw, &cfg)
	}
	cfg.Backend = config.NormalizeBackend(cfg.Backend)

	sid := chat.NewUUID()

	// New chat: give every new chat its own workspace folder by default, named
	// with a system-generated unique id independent of the session name — so
	// files stay organized per chat and renaming a session never touches its
	// folder. This is suppressed when the client explicitly chose a working
	// directory (an existing folder, or a repo clone target) via the "+" menu /
	// Advanced options, in which case we honor that path as-is.
	autoWorkdir := false
	if !clientChoseWorkdir(body) {
		if dir, err := uniqueChatDir(); err == nil {
			cfg.Workdir = dir
			autoWorkdir = true
		} else {
			log.Printf("[sessions] auto workspace folder failed, using default: %v", err)
		}
	}

	// Durable file storage: scaffold the durable-store folder convention (inputs/, working/,
	// .system/) and write the platform-populated .system/metadata for the new
	// chat's own workspace. Temporary chats are never provisioned a durable
	// store, so they are skipped entirely. Only auto-created per-chat folders
	// are scaffolded — a client-chosen existing directory or repo clone is left
	// untouched (it may be a project workspace scaffolded elsewhere, or an
	// arbitrary folder the user picked). Best-effort: a failure here does not
	// block chat creation.
	if autoWorkdir && !cfg.Temporary {
		if err := durable.InitChatShare(cfg.Workdir, sid, ""); err != nil {
			log.Printf("[sessions] durable share init failed for %s: %v", sid, err)
		}
	}

	if err := db.CreateSession(sid, cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sid, "config": cfg})
}

// clientChoseWorkdir reports whether the create request explicitly specified a
// working directory (existing-folder selection or repo clone target). When it
// did, the auto per-chat folder is suppressed and the chosen path is used.
func clientChoseWorkdir(body map[string]any) bool {
	if body == nil {
		return false
	}
	for _, k := range []string{"workdir", "workspace"} {
		if v, ok := body[k].(string); ok && strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

// uniqueChatDir creates and returns a fresh per-chat workspace folder named
// "chat-<12 hex>" from a random UUID, so the folder id is independent of the
// (user-editable) session title.
//
// It is created under the "Chats" subdirectory of the base workspace, keeping
// per-chat folders out of the workspace root alongside the user's own files and
// matching the sibling "Projects" and "Scheduled Tasks" folders. Chats created
// before this stay where they are: their absolute workdir is stored per session
// and is never recomputed, and workdirs.IsAutoChat still recognizes the legacy
// location.
func uniqueChatDir() (string, error) {
	base := workdirs.ChatsRoot()
	if strings.TrimSpace(base) == "" {
		base = filepath.Join("/home/developer", workdirs.ChatsDirName)
	}
	// Derive a short, filesystem-safe id from a UUID (strip dashes, take 12).
	id := strings.ReplaceAll(chat.NewUUID(), "-", "")
	if len(id) > 12 {
		id = id[:12]
	}
	dir := filepath.Join(base, "chat-"+id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func HandleSessionConfig(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	if !db.SessionExists(sid) {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}
	cfg, _ := db.GetSessionConfig(sid)
	writeJSON(w, http.StatusOK, cfg)
}

func HandleUpdateSessionConfig(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	if !db.SessionExists(sid) {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}

	cfg, _ := db.GetSessionConfig(sid)
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	r.Body.Close()

	// Capture the pre-merge Temporary value: it is a creation-time-only decision
	// and must never change via a mid-chat config update.
	wasTemporary := cfg.Temporary

	// Merge: serialize existing, overlay new fields, deserialize back
	existing, _ := json.Marshal(cfg)
	var merged map[string]any
	json.Unmarshal(existing, &merged)
	for k, v := range body {
		merged[k] = v
	}
	mergedJSON, _ := json.Marshal(merged)
	json.Unmarshal(mergedJSON, &cfg)
	cfg.Backend = config.NormalizeBackend(cfg.Backend)

	// Invariants:
	//  - Temporary is creation-only: restore the original value regardless of
	//    what the client sent.
	cfg.Temporary = wasTemporary
	//  - Keep is mutually exclusive with Temporary.
	if cfg.Temporary {
		cfg.Keep = false
	}
	//  - For a project chat, Keep maps to project-level Keep (the shared project
	//    workspace is the durable unit); it is not stored per-chat. Only act when
	//    the client explicitly included "keep" in this request.
	if _, sentKeep := body["keep"]; sentKeep {
		if projectID := db.GetSessionProject(sid); projectID != "" {
			_ = db.SetProjectKeep(projectID, cfg.Keep)
			cfg.Keep = false
		}
	}

	db.UpdateSessionConfig(sid, cfg)
	writeJSON(w, http.StatusOK, cfg)
}

func HandleRenameSession(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	if !db.SessionExists(sid) {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}
	var body struct {
		Name string `json:"name"`
		// Auto marks a rename the client made on the user's behalf (the
		// placeholder derived from the first prompt) rather than a name the
		// user typed. Only a deliberate rename protects the label from
		// automatic titling.
		Auto bool `json:"auto"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	r.Body.Close()
	if body.Name == "" {
		http.Error(w, "Name required", http.StatusBadRequest)
		return
	}
	cfg, _ := db.GetSessionConfig(sid)
	cfg.Label = body.Name
	if !body.Auto {
		cfg.LabelManual = true
	}
	db.UpdateSessionConfig(sid, cfg)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "label": body.Name})
}

func HandleUndoSession(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	if !db.SessionExists(sid) {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}
	removed, remaining := db.UndoLastExchange(sid)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed, "remaining": remaining})
}

func HandleExportSession(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	if !db.SessionExists(sid) {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}
	cfg, _ := db.GetSessionConfig(sid)
	msgs := db.GetMessages(sid)

	var lines []string
	lines = append(lines, "# "+cfg.Label, "")
	lines = append(lines, "> Session ID: `"+sid+"`  ")
	lines = append(lines, "> Mode: `"+cfg.Mode+"`  ")
	lines = append(lines, "> Model: `"+cfg.Model+"`  ")
	lines = append(lines, "> Working directory: `"+cfg.Workdir+"`")
	lines = append(lines, "", "---", "")

	for _, msg := range msgs {
		role := "**You**"
		if msg.Role == "assistant" {
			role = "**Assistant**"
		}
		lines = append(lines, "### "+role+"\n")
		lines = append(lines, msg.Content, "", "---", "")
	}

	md := strings.Join(lines, "\n")
	filename := fmt.Sprintf("chat-session-%s.md", sid[:8])
	w.Header().Set("Content-Type", "text/markdown")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Write([]byte(md))
}

func HandleDeleteSession(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	// Read the config before the row goes away: once the session is deleted
	// nothing records which directory belonged to it.
	cfg, _ := db.GetSessionConfig(sid)
	// Same reason, and the same rails: the opencode session id lives on the row
	// too, and without it opencode's copy of the transcript can never be found
	// again, let alone removed.
	ocSession := db.GetOpencodeSession(sid)
	if val, ok := chat.Streams.LoadAndDelete(sid); ok {
		val.(*chat.Stream).Finish()
	}
	if val, ok := chat.Procs.LoadAndDelete(sid); ok {
		proc := val.(*chat.ActiveProcess)
		proc.Kill()
	}
	chat.ClearQueue(sid)
	chat.EndSession(sid)

	// Day notes outlive the chat they
	// describe, because deleting a chat is routine hygiene and losing the
	// record of what was done that month is not. Two things follow, and both
	// happen here while the session row still exists.
	//
	// `?notes=delete` is the consent path. Keeping notes is right for the
	// common case, but someone deleting a chat *because of what is in it* --
	// a pasted credential, a personal matter -- must be able to remove the
	// summary in the same action, or the deletion does not remove the thing
	// they wanted gone. A retention default that cannot be overridden at the
	// moment of deletion is not a default, it is a trap.
	notesDeleted := 0
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("notes")), "delete") {
		n, err := db.DeleteSessionNotes(sid)
		if err != nil {
			// Reported, not fatal: the user asked for the chat to go.
			log.Printf("[sessions] could not delete notes for %s: %v", sid, err)
		}
		notesDeleted = n
	} else if err := db.MarkNotesOrphaned(sid); err != nil {
		// Also not fatal, for the same reason -- but it must be loud. An
		// unmarked note is one that claims to describe a readable chat when
		// the chat is gone, which is the exact failure `source` exists to
		// prevent.
		log.Printf("[sessions] could not mark notes orphaned for %s: %v", sid, err)
	}

	if err := db.DeleteSession(sid); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Workspace hygiene: delete the auto-created per-chat folder too. Leaving it behind
	// stranded it permanently — with no session row it was invisible in the UI
	// and unreachable by tidy-up, which surfaces containers, not directories.
	// Only auto-created folders match; a workdir the user chose is untouched.
	if workdirs.RemoveAutoChat(cfg.Workdir) {
		log.Printf("[sessions] removed chat workspace %s for deleted session %s", cfg.Workdir, sid)
	}
	// And delete opencode's own copy of the conversation, which nothing else
	// ever reclaims.
	chat.DeleteOpencodeSession(ocSession)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		// Echoed so the caller can confirm the consent choice took effect
		// rather than assuming it did.
		"notes_deleted": notesDeleted,
	})
}

// HandleStopSession stops an in-flight chat turn for a session without
// deleting the session. It kills the running backend process (which, for the
// opencode-serve backend, aborts the remote session via ActiveProcess.Abort)
// and finishes the SSE stream so connected clients see the turn end. The
// session, its config, and message history are preserved. Also clears any
// queued follow-ups so they don't start after the user asked to stop.
//
// This is necessary because the browser aborting its SSE connection alone
// does NOT stop generation: the server-side dispatcher keeps the process
// alive so work survives a page reload (see HandleAttachStream).
func HandleStopSession(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	stopped := chat.StopSession(sid)
	log.Printf("[STOP] session=%s stopped=%v", sid, stopped)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "stopped": stopped})
}

func HandleHistory(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("session_id")
	msgs := db.GetMessages(sid)
	writeJSON(w, http.StatusOK, msgs)
}

func HandlePinSession(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	if !db.SessionExists(sid) {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}
	cfg, _ := db.GetSessionConfig(sid)
	cfg.Pinned = !cfg.Pinned
	db.UpdateSessionConfig(sid, cfg)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pinned": cfg.Pinned})
}

func HandleFavoriteSession(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	if !db.SessionExists(sid) {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}
	cfg, _ := db.GetSessionConfig(sid)
	cfg.Favorite = !cfg.Favorite
	db.UpdateSessionConfig(sid, cfg)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "favorite": cfg.Favorite})
}

// HandleGetSessionDraft returns the in-progress prompt text persisted for
// the session, or { "draft": "" } when nothing has been saved yet.
func HandleGetSessionDraft(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	if !db.SessionExists(sid) {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"draft": db.GetSessionDraft(sid)})
}

// HandleSetSessionDraft persists the prompt text the user has typed but not
// yet sent. An empty string clears the draft. We cap the stored size to
// keep tiny SQLite WAL frames; the UI does not need to keep megabytes of
// draft text around.
func HandleSetSessionDraft(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	if !db.SessionExists(sid) {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}
	var body struct {
		Draft string `json:"draft"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	r.Body.Close()

	const maxDraftBytes = 64 * 1024
	if len(body.Draft) > maxDraftBytes {
		body.Draft = body.Draft[:maxDraftBytes]
	}

	if err := db.SetSessionDraft(sid, body.Draft); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "size": len(body.Draft)})
}

func HandleChat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Prompt    string `json:"prompt"`
		SessionID string `json:"session_id"`
		// Skill is a skill the user armed in the composer for this message
		// only. It rides on the request rather than session config so
		// that a turn which does not name a skill cannot have one.
		Skill string `json:"skill"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	r.Body.Close()

	prompt := strings.TrimSpace(body.Prompt)
	if prompt == "" {
		http.Error(w, "prompt is required", http.StatusBadRequest)
		return
	}

	browserSID := strings.TrimSpace(body.SessionID)
	if browserSID == "" {
		browserSID = chat.NewUUID()
	}
	if !db.SessionExists(browserSID) {
		db.CreateSession(browserSID, config.DefaultSessionConfig())
	}

	// Legacy GitHub Copilot sessions are deprecated and read-only. Reject the
	// turn before opening the SSE stream so the client can prompt the user to
	// start a new session.
	if db.IsSessionDeprecated(browserSID) {
		http.Error(w,
			"This is a deprecated GitHub Copilot session and is read-only. Please create a new session to continue.",
			http.StatusConflict)
		return
	}

	cfg, _ := db.GetSessionConfig(browserSID)

	// SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// If a stream is already in flight for this session, queue the prompt
	// instead of spawning a second copilot process. The browser receives a
	// `queued` event and disconnects; the prompt will be dispatched after the
	// current (and any earlier-queued) turn completes — handled inside the
	// dispatcher when it Finish()es.
	stream, queued := chat.SendOrEnqueue(browserSID, prompt, cfg, chat.Turn{Skill: strings.TrimSpace(body.Skill)})
	if queued != nil {
		log.Printf("[QUEUE] session=%s prompt queued via /api/chat", browserSID)
		writeSSE(w, flusher, map[string]any{"type": "session_id", "session_id": browserSID})
		writeSSE(w, flusher, map[string]any{
			"type":      "queued",
			"id":        queued.ID,
			"position":  chat.QueueLen(browserSID),
			"queue_len": chat.QueueLen(browserSID),
		})
		writeSSE(w, flusher, map[string]any{"type": "done", "queued": true})
		return
	}
	log.Printf("[SSE] session=%s stream started", browserSID)
	forwardStream(w, flusher, r.Context(), browserSID, stream, 0)
}

// HandleQueueList returns the current queue for a session.
func HandleQueueList(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	if !db.SessionExists(sid) {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}
	items := chat.SnapshotQueue(sid)
	if items == nil {
		items = []chat.QueuedPrompt{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sid,
		"queue":      items,
		"streaming":  chat.IsStreaming(sid),
	})
}

// HandleQueueAdd appends a prompt to the queue for a session. If no stream is
// active it starts one immediately.
func HandleQueueAdd(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	if !db.SessionExists(sid) {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}
	if db.IsSessionDeprecated(sid) {
		http.Error(w,
			"This is a deprecated GitHub Copilot session and is read-only. Please create a new session to continue.",
			http.StatusConflict)
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
		Skill  string `json:"skill"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	r.Body.Close()
	prompt := strings.TrimSpace(body.Prompt)
	if prompt == "" {
		http.Error(w, "prompt required", http.StatusBadRequest)
		return
	}

	cfg, _ := db.GetSessionConfig(sid)
	_, queued := chat.SendOrEnqueue(sid, prompt, cfg, chat.Turn{Skill: strings.TrimSpace(body.Skill)})
	if queued != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":        true,
			"queued":    true,
			"id":        queued.ID,
			"position":  chat.QueueLen(sid),
			"queue_len": chat.QueueLen(sid),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"queued":    false,
		"streaming": true,
		"queue_len": 0,
	})
}

// HandleQueueDelete removes a single queued prompt by id.
func HandleQueueDelete(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	id := r.PathValue("id")
	if !db.SessionExists(sid) {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}
	if !chat.RemoveQueuedByID(sid, id) {
		http.Error(w, "queued item not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"queue_len": chat.QueueLen(sid),
	})
}

// HandleQueueClear removes all queued prompts for a session.
func HandleQueueClear(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	if !db.SessionExists(sid) {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}
	chat.ClearQueue(sid)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "queue_len": 0})
}

// HandleAttachStream re-attaches a browser to an existing in-flight (or
// recently-finished) chat stream for a session, replaying any buffered events
// and continuing live. If no stream exists, it returns immediately with a
// `done` event.
func HandleAttachStream(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	if !db.SessionExists(sid) {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	stream := chat.GetStream(sid)
	if stream == nil {
		writeSSE(w, flusher, map[string]any{"type": "session_id", "session_id": sid})
		writeSSE(w, flusher, map[string]any{"type": "done"})
		return
	}
	log.Printf("[SSE] session=%s attach (replay)", sid)
	forwardStream(w, flusher, r.Context(), sid, stream, 0)
}

// forwardStream subscribes to a chat.Stream and forwards buffered + live
// events to the SSE client. On client disconnect it returns silently — the
// dispatcher and underlying copilot process keep running.
func forwardStream(w http.ResponseWriter, flusher http.Flusher, ctx context.Context, sid string, stream *chat.Stream, fromIdx int) {
	idx := fromIdx
	for {
		batch, newIdx, done := stream.Wait(ctx, idx)
		if batch == nil && !done {
			// Context cancelled / client disconnected.
			log.Printf("[SSE] session=%s client disconnected (process keeps running)", sid)
			return
		}
		idx = newIdx
		for _, ev := range batch {
			writeSSE(w, flusher, ev)
		}
		if done {
			return
		}
	}
}

// HandlePermission answers an interactive permission request raised by the
// opencode server backend. Body: { "permission_id": "per_...", "response":
// "once"|"always"|"reject" }. Routes to the opencode server's permission
// endpoint for the session's in-flight turn.
func HandlePermission(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	var body struct {
		PermissionID string `json:"permission_id"`
		Response     string `json:"response"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	r.Body.Close()

	permID := strings.TrimSpace(body.PermissionID)
	resp := strings.TrimSpace(body.Response)
	switch resp {
	case "once", "always", "reject":
	default:
		http.Error(w, `response must be one of "once", "always", "reject"`, http.StatusBadRequest)
		return
	}
	if permID == "" {
		http.Error(w, "permission_id required", http.StatusBadRequest)
		return
	}

	log.Printf("[PERMISSION] session=%s id=%s response=%s", sid, permID, resp)
	ok, err := chat.AnswerPermission(r.Context(), sid, permID, resp, chat.Author{})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "no such request is waiting"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, data map[string]any) {
	w.Write(formatSSE(data))
	flusher.Flush()
}

func formatSSE(data map[string]any) []byte {
	b, _ := json.Marshal(data)
	if eventType, ok := data["type"].(string); ok {
		return fmt.Appendf(nil, "event: %s\ndata: %s\n\n", eventType, b)
	}
	return fmt.Appendf(nil, "data: %s\n\n", b)
}
