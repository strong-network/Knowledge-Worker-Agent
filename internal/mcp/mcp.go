// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package mcp manages MCP (Model Context Protocol) servers for the opencode
// backend. Servers are declared in opencode's global opencode.json under the
// "mcp" key; this package reads and writes that block. opencode itself handles
// MCP OAuth automatically (dynamic client registration, its own token store),
// so there is no custom OAuth flow here.
package mcp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// HandleList returns configured MCP servers from opencode.json.
func HandleList(w http.ResponseWriter, r *http.Request) {
	servers, err := listServers()
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "Failed to read MCP config: "+err.Error())
		return
	}
	// Always encode as [] (never null) for the frontend.
	if servers == nil {
		servers = []serverView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": servers})
}

// HandleGet returns details for a single server.
func HandleGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSONErr(w, http.StatusBadRequest, "name is required")
		return
	}
	sv, ok, err := mergedServer(name)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeJSONErr(w, http.StatusNotFound, "server not found: "+name)
		return
	}
	writeJSON(w, http.StatusOK, sv)
}

// provisionedRefusal is the message returned when the client tries to edit or
// remove a centrally provisioned server.
//
// This is NOT access control — the platform config is a file on the user's own
// disk and they can edit it directly. It is because the materializer
// wipes and recreates that directory on every boot, so an edit or a removal
// would appear to work and then silently undo itself at the next restart.
// Offering an action that does not survive a restart is the failure class central
// connectors exist to remove. Enabling and disabling stay available, because those write
// to the user's Global, which the materializer never touches.
func provisionedRefusal(name string) string {
	return name + " is provided by your organization, so it can't be changed here. " +
		"You can still turn it on or off — that choice is yours and it survives a restart."
}

// serverRequest is the body of an add or an edit, in the transport-oriented
// shape the frontend uses; it is translated to opencode's schema on write.
type serverRequest struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"` // stdio|http|sse
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	URL       string            `json:"url"`
	Env       map[string]string `json:"env"`
	Headers   map[string]string `json:"headers"`
	Timeout   int               `json:"timeout"`
}

// view validates the request and returns the server to store under name.
func (b serverRequest) view(name string, enabled bool) (serverView, error) {
	transport := b.Transport
	if transport == "" {
		if b.URL != "" {
			transport = "http"
		} else {
			transport = "stdio"
		}
	}
	sv := serverView{
		Name:        name,
		Transport:   transport,
		Command:     strings.TrimSpace(b.Command),
		Args:        b.Args,
		URL:         strings.TrimSpace(b.URL),
		Environment: b.Env,
		Headers:     b.Headers,
		Timeout:     b.Timeout,
		Enabled:     enabled,
	}
	switch strings.ToLower(transport) {
	case "stdio", "local":
		if sv.Command == "" {
			return serverView{}, errors.New("command is required for stdio servers")
		}
	default:
		if sv.URL == "" {
			return serverView{}, errors.New("url is required for " + transport + " servers")
		}
	}
	return sv, nil
}

// HandleAdd adds (or replaces) an MCP server.
func HandleAdd(w http.ResponseWriter, r *http.Request) {
	var body serverRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	r.Body.Close()

	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeJSONErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if IsProvisioned(name) {
		writeJSONErr(w, http.StatusConflict, provisionedRefusal(name))
		return
	}
	if IsOwned(name) {
		writeJSONErr(w, http.StatusConflict, ownedRefusal(name))
		return
	}

	// A server the user adds by hand is meant to be used right away.
	sv, err := body.view(name, true)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, err.Error())
		return
	}
	stored, err := upsertServer(sv)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	configChanged()
	writeJSON(w, http.StatusOK, stored)
}

// HandleUpdate edits a configured server in place.
// PUT /api/mcp/servers/{name}
//
// Unlike an add it keeps the server's on/off state and refuses a name that
// isn't configured. The name comes from the path and can't change: opencode
// keys sign-in tokens by it, and chats and projects record their connector
// choices under it.
func HandleUpdate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if strings.TrimSpace(name) == "" {
		writeJSONErr(w, http.StatusBadRequest, "name is required")
		return
	}
	var body serverRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	r.Body.Close()

	if IsProvisioned(name) {
		writeJSONErr(w, http.StatusConflict, provisionedRefusal(name))
		return
	}
	if IsOwned(name) {
		writeJSONErr(w, http.StatusConflict, ownedRefusal(name))
		return
	}
	existing, ok, err := getServer(name)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "Failed to read MCP config: "+err.Error())
		return
	}
	if !ok {
		writeJSONErr(w, http.StatusNotFound, "server not found: "+name)
		return
	}
	sv, err := body.view(existing.Name, existing.Enabled)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, err.Error())
		return
	}
	stored, err := upsertServer(sv)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	configChanged()
	writeJSON(w, http.StatusOK, stored)
}

// HandleSetEnabled enables or disables a configured MCP server.
// PUT /api/mcp/servers/{name}/enabled   body: {"enabled": true}
//
// Disabled servers stay listed in the MCP Servers modal but are skipped by
// opencode entirely, so none of their tools are advertised to the model. This
// is what keeps the tool surface limited to servers the user can actually use.
func HandleSetEnabled(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSONErr(w, http.StatusBadRequest, "name is required")
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	r.Body.Close()

	ok, err := setEnabled(name, body.Enabled)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeJSONErr(w, http.StatusNotFound, "server not found: "+name)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "enabled": body.Enabled})
}

// HandleRemove removes an MCP server by name.
func HandleRemove(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSONErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if IsProvisioned(name) {
		writeJSONErr(w, http.StatusConflict, provisionedRefusal(name))
		return
	}
	if IsOwned(name) {
		writeJSONErr(w, http.StatusConflict, ownedRefusal(name))
		return
	}
	if err := removeServer(name); err != nil {
		writeJSONErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	configChanged()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeJSONErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}
