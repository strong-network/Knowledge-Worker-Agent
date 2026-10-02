// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package providers

import (
	"encoding/json"
	"net/http"
	"strings"
)

// keyStore is the credential writer, injected at startup so this package does
// not import the auth packages.
var keyStore struct {
	Set    func(provider, key string) error
	Remove func(provider string) error
}

// SetKeyStore installs the credential writer backing the key endpoints.
func SetKeyStore(set func(provider, key string) error, remove func(provider string) error) {
	keyStore.Set = set
	keyStore.Remove = remove
}

// onKeyChange is invoked after a key is stored or removed, so the caller can
// re-discover models without this package importing the discovery code.
var onKeyChange func()

// SetKeyChangeHook installs a callback fired after a successful key change.
func SetKeyChangeHook(fn func()) { onKeyChange = fn }

// resolvedPresetsFn returns the preset nominations already resolved against the
// discovered model list. Injected at startup so this package does not import
// the config layer.
var resolvedPresetsFn func() map[string]string

// SetResolvedPresets installs the source of resolved preset nominations served
// to the browser.
func SetResolvedPresets(fn func() map[string]string) { resolvedPresetsFn = fn }

// listResponse is the /api/providers payload: the registry plus the resolved
// presets.
//
// The presets are served alongside rather than derived in the browser because
// the resolution rules — first authenticated provider in assignment order,
// skipping nominations whose model was not discovered — already exist in Go.
// A second implementation in TypeScript would eventually disagree with this
// one, and the symptom would be a composer chip labelled "Default" that arms a
// different model than the server would have chosen.
type listResponse struct {
	Registry
	// Presets maps preset name → model id. Absent entries mean "no nomination",
	// and the browser falls back to its own Claude heuristic — which is what
	// always happens in built-in mode.
	Presets map[string]string `json:"presets"`
}

// HandleList serves GET /api/providers — the registry for the active mode.
//
// This is the single endpoint the Accounts modal, the model picker and the
// connected-state banner all read, replacing the per-provider status routes and
// the hard-coded markup that could only ever describe two providers.
func HandleList(w http.ResponseWriter, r *http.Request) {
	presets := map[string]string{}
	if resolvedPresetsFn != nil {
		if p := resolvedPresetsFn(); p != nil {
			presets = p
		}
	}
	writeJSON(w, http.StatusOK, listResponse{Registry: Resolve(), Presets: presets})
}

// HandleSetKey serves POST /api/providers/{id}/key for a user-key provider.
func HandleSetKey(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	p, ok := lookup(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown provider")
		return
	}
	if err := guardUserKey(p); err != "" {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.Key) == "" {
		writeErr(w, http.StatusBadRequest, "key is required")
		return
	}
	if keyStore.Set == nil {
		writeErr(w, http.StatusInternalServerError, "credential store unavailable")
		return
	}
	if err := keyStore.Set(p.ID, strings.TrimSpace(body.Key)); err != nil {
		// Deliberately not echoing err: credential-store errors can quote the
		// content being written.
		writeErr(w, http.StatusInternalServerError, "could not store the key")
		return
	}
	if onKeyChange != nil {
		onKeyChange()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// HandleRemoveKey serves DELETE /api/providers/{id}/key.
func HandleRemoveKey(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	p, ok := lookup(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown provider")
		return
	}
	if err := guardUserKey(p); err != "" {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if keyStore.Remove == nil {
		writeErr(w, http.StatusInternalServerError, "credential store unavailable")
		return
	}
	if err := keyStore.Remove(p.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not remove the key")
		return
	}
	if onKeyChange != nil {
		onKeyChange()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// guardUserKey returns a message when a provider does not accept a pasted key,
// or "" when it does.
//
// The id is matched against the registry rather than trusted, so these
// endpoints can only ever write a credential for a provider the workspace
// actually offers — an arbitrary id from the browser cannot inject an entry
// into the credential store.
func guardUserKey(p Provider) string {
	if p.Builtin {
		return "this provider uses its own sign-in flow"
	}
	if p.AuthType == AuthManaged {
		return "this provider's key is provisioned by your administrator"
	}
	return ""
}

// lookup finds a provider by id in the current registry.
func lookup(id string) (Provider, bool) {
	if id == "" {
		return Provider{}, false
	}
	for _, p := range Resolve().Providers {
		if strings.EqualFold(p.ID, id) {
			return p, true
		}
	}
	return Provider{}, false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}
