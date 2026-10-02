// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package agents

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/agentbuilder"
)

// Handlers exposes the Agent Builder management API over a Store. Register with
// RegisterRoutes.
type Handlers struct {
	Store *Store
	// ModelsFn reports the models the user can access, for the import
	// unavailable-model warning. Optional; nil means "assume all available".
	ModelsFn func() []string
	// AssistFn runs a one-shot model turn for the writing assists (prompt
	// polish, description improve). Optional; nil means the assists return 503.
	AssistFn AssistFn
	// ReloadFn is invoked after a successful create/update/delete/import so the
	// backend can pick up the on-disk change immediately (e.g. cycle the
	// long-lived `opencode serve` so a new agent is usable without a restart).
	// Optional; nil means no notification.
	ReloadFn func()
}

func (h *Handlers) reloaded() {
	if h.ReloadFn != nil {
		h.ReloadFn()
	}
}

func (h *Handlers) models() []string {
	if h.ModelsFn == nil {
		return nil
	}
	return h.ModelsFn()
}

// RegisterRoutes wires the builder's management endpoints onto mux:
//
//	GET    /api/agent-builder/agents        list the user's own agents
//	POST   /api/agent-builder/agents        create an agent
//	GET    /api/agent-builder/agents/{id}   fetch one agent (for editing)
//	PUT    /api/agent-builder/agents/{id}   update an agent (handles rename)
//	DELETE /api/agent-builder/agents/{id}   delete an agent
//	GET    /api/agent-builder/agents/{id}/export   download the agent's .md
//	POST   /api/agent-builder/import/preview        validate + preview an upload
//	POST   /api/agent-builder/import                place a validated upload
func (h *Handlers) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agent-builder/agents", h.list)
	mux.HandleFunc("POST /api/agent-builder/agents", h.create)
	mux.HandleFunc("GET /api/agent-builder/agents/{id}", h.get)
	mux.HandleFunc("PUT /api/agent-builder/agents/{id}", h.update)
	mux.HandleFunc("DELETE /api/agent-builder/agents/{id}", h.delete)
	mux.HandleFunc("GET /api/agent-builder/agents/{id}/export", h.export)
	mux.HandleFunc("POST /api/agent-builder/import/preview", h.importPreview)
	mux.HandleFunc("POST /api/agent-builder/import", h.importCommit)
	mux.HandleFunc("POST /api/agent-builder/assist/prompt", h.polishPrompt)
	mux.HandleFunc("POST /api/agent-builder/assist/description", h.improveDescription)
	// Prompt assist: the chat composer's "Optimize my prompt" assist reuses the same
	// AssistFn path as the Agent Builder polisher, but with a user-prompt
	// instruction. It is a sibling of the assist routes above.
	mux.HandleFunc("POST /api/chat/assist/prompt", h.optimizePrompt)
}

// agentPayload is the create/update request body: the typed agent fields the
// form produces. It mirrors agentbuilder.Agent so the frontend sends one shape.
type agentPayload struct {
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	Prompt      string                   `json:"prompt"`
	Model       string                   `json:"model"`
	Mode        agentbuilder.Mode        `json:"mode"`
	Temperature *float64                 `json:"temperature"`
	TopP        *float64                 `json:"top_p"`
	Steps       *int                     `json:"steps"`
	Color       string                   `json:"color"`
	Hidden      bool                     `json:"hidden"`
	Disable     bool                     `json:"disable"`
	Permission  *agentbuilder.Permission `json:"permission"`
}

func (p *agentPayload) toAgent() *agentbuilder.Agent {
	return &agentbuilder.Agent{
		Name:        p.Name,
		Description: p.Description,
		Prompt:      p.Prompt,
		Model:       p.Model,
		Mode:        p.Mode,
		Temperature: p.Temperature,
		TopP:        p.TopP,
		Steps:       p.Steps,
		Color:       p.Color,
		Hidden:      p.Hidden,
		Disable:     p.Disable,
		Permission:  p.Permission,
	}
}

func fromAgent(a *agentbuilder.Agent) agentPayload {
	return agentPayload{
		Name:        a.Name,
		Description: a.Description,
		Prompt:      a.Prompt,
		Model:       a.Model,
		Mode:        a.Mode,
		Temperature: a.Temperature,
		TopP:        a.TopP,
		Steps:       a.Steps,
		Color:       a.Color,
		Hidden:      a.Hidden,
		Disable:     a.Disable,
		Permission:  a.Permission,
	}
}

func (h *Handlers) list(w http.ResponseWriter, r *http.Request) {
	items, err := h.Store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't read your agents.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": items})
}

func (h *Handlers) get(w http.ResponseWriter, r *http.Request) {
	a, err := h.Store.Get(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, fromAgent(a))
}

func (h *Handlers) create(w http.ResponseWriter, r *http.Request) {
	h.save(w, r, "")
}

func (h *Handlers) update(w http.ResponseWriter, r *http.Request) {
	h.save(w, r, r.PathValue("id"))
}

func (h *Handlers) save(w http.ResponseWriter, r *http.Request, originalID string) {
	var p agentPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "The request body wasn't valid.")
		return
	}
	item, err := h.Store.Save(p.toAgent(), originalID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	h.reloaded()
	status := http.StatusOK
	if originalID == "" {
		status = http.StatusCreated
	}
	writeJSON(w, status, item)
}

func (h *Handlers) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.Store.Delete(r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	h.reloaded()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// export streams the agent's raw `.md` as a file download.
func (h *Handlers) export(w http.ResponseWriter, r *http.Request) {
	filename, content, err := h.Store.Export(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

// importRequest is the upload body for preview/commit: the raw file contents,
// the original filename (identity hint), and an optional name the importer
// chose to resolve a collision or rename on adopt.
type importRequest struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
	Name     string `json:"name"`
}

// importPreview validates an uploaded file and returns its pre-adopt preview.
// A malformed file is not a transport error: it returns 200 with valid=false and
// the blocking errors, so the UI can show them in the preview dialog.
func (h *Handlers) importPreview(w http.ResponseWriter, r *http.Request) {
	var req importRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "The request body wasn't valid.")
		return
	}
	preview := h.Store.PreviewImport([]byte(req.Content), req.Filename, req.Name, h.models())
	writeJSON(w, http.StatusOK, preview)
}

// importCommit validates and places an uploaded file. Collisions (409) and
// malformed files (422) are reported via the shared store-error mapping.
func (h *Handlers) importCommit(w http.ResponseWriter, r *http.Request) {
	var req importRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "The request body wasn't valid.")
		return
	}
	item, err := h.Store.Import([]byte(req.Content), req.Filename, req.Name)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	h.reloaded()
	writeJSON(w, http.StatusCreated, item)
}

// writeStoreError maps store-layer errors to appropriate HTTP responses.
func writeStoreError(w http.ResponseWriter, err error) {
	var ve ValidationErrors
	var ce ErrCollision
	switch {
	case errors.As(err, &ve):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":  "The agent has problems that need fixing.",
			"fields": []agentbuilder.ValidationError(ve),
		})
	case errors.As(err, &ce):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "You already have an agent with this name. Enter a different name.",
			"slug":  ce.Slug,
		})
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "That agent doesn't exist.")
	case errors.Is(err, ErrProtected):
		writeError(w, http.StatusForbidden, "This is a built-in agent and can't be changed.")
	default:
		writeError(w, http.StatusInternalServerError, "Something went wrong saving your agent.")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}
