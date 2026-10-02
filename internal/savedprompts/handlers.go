// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package savedprompts serves the "Saved prompts" API: the user-owned,
// persisted starter chips shown on the New Chat surface. It is a thin CRUD +
// reorder layer over db.SavedPrompt; the agent resolution (Default / Thinking /
// custom) and the pre-fill-don't-send behavior live in the frontend, which
// reuses the composer's existing selector and session-config plumbing.
package savedprompts

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// validAgentKinds are the agent selections a saved prompt may store. They map
// to the composer's friendly selector: "default"/"thinking" are model presets
// (no custom agent), "custom" pins a specific agent by id.
var validAgentKinds = map[string]bool{"default": true, "thinking": true, "custom": true}

// promptPayload is the create/update request body.
type promptPayload struct {
	Name      string `json:"name"`
	Prompt    string `json:"prompt"`
	AgentKind string `json:"agent_kind"`
	AgentID   string `json:"agent_id"`
}

// normalize trims fields and applies the agent-kind rules: an unknown/blank
// kind falls back to "default"; only "custom" keeps an agent id. Returns the
// cleaned name, prompt, kind, id and whether name/prompt are both present.
func (p promptPayload) normalize() (name, prompt, kind, id string, ok bool) {
	name = strings.TrimSpace(p.Name)
	prompt = strings.TrimSpace(p.Prompt)
	kind = strings.TrimSpace(p.AgentKind)
	if !validAgentKinds[kind] {
		kind = "default"
	}
	if kind == "custom" {
		id = strings.TrimSpace(p.AgentID)
		if id == "" {
			// A custom kind without an id is meaningless; treat as Default.
			kind = "default"
		}
	}
	ok = name != "" && prompt != ""
	return
}

// HandleList — GET /api/saved-prompts. Seeds the defaults on first ever
// call, applies the one-time "Research a company" upgrade for installs that
// were seeded before it existed, then returns the prompts in display order.
func HandleList(w http.ResponseWriter, r *http.Request) {
	_ = db.SeedDefaultSavedPromptsOnce()
	_ = db.UpgradeResearchCompanyPromptOnce()
	writeJSON(w, http.StatusOK, map[string]any{"saved_prompts": db.ListSavedPrompts()})
}

// HandleCreate — POST /api/saved-prompts. Enforces the required fields and the
// 12-chip cap.
func HandleCreate(w http.ResponseWriter, r *http.Request) {
	var req promptPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "The request body wasn't valid.")
		return
	}
	name, prompt, kind, id, ok := req.normalize()
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "A name and a prompt are both required.")
		return
	}
	if db.CountSavedPrompts() >= db.MaxSavedPrompts {
		writeError(w, http.StatusConflict, "You've reached the maximum of 12 saved prompts. Delete one to add another.")
		return
	}
	created, err := db.CreateSavedPrompt(chat.NewUUID(), name, prompt, kind, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't save your prompt.")
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// HandleUpdate — PATCH /api/saved-prompts/{id}. Edits name/prompt/agent.
func HandleUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !db.SavedPromptExists(id) {
		writeError(w, http.StatusNotFound, "That saved prompt no longer exists.")
		return
	}
	var req promptPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "The request body wasn't valid.")
		return
	}
	name, prompt, kind, agentID, ok := req.normalize()
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "A name and a prompt are both required.")
		return
	}
	updated, err := db.UpdateSavedPrompt(id, name, prompt, kind, agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't update your prompt.")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// HandleDelete — DELETE /api/saved-prompts/{id}. Built-in seeds delete like any
// other; deleting the last chip leaves an empty row (no re-seed).
func HandleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := db.DeleteSavedPrompt(id); err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't delete that prompt.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// reorderRequest is the body for PUT /api/saved-prompts/reorder.
type reorderRequest struct {
	IDs []string `json:"ids"`
}

// HandleReorder — PUT /api/saved-prompts/reorder. Persists the drag-to-reorder
// order; the ids are assigned ascending positions in the order given.
func HandleReorder(w http.ResponseWriter, r *http.Request) {
	var req reorderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "The request body wasn't valid.")
		return
	}
	if err := db.ReorderSavedPrompts(req.IDs); err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't save the new order.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"saved_prompts": db.ListSavedPrompts()})
}
