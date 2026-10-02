// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package providers

import (
	"context"
	"net/http"
	"strings"
)

// signOutFns holds the per-provider credential removers. A built-in signs out
// through its own auth package (gcloud, opencode); a user-key provider signs
// out by dropping its stored key, which keyStore already does.
var signOutFns = map[string]func() error{}

// SetSignOut registers how a built-in provider discards its credentials.
func SetSignOut(providerID string, fn func() error) {
	reachMu.Lock()
	defer reachMu.Unlock()
	signOutFns[providerID] = fn
}

func signOutFor(id string) func() error {
	reachMu.RLock()
	defer reachMu.RUnlock()
	return signOutFns[id]
}

// CanSignOut reports whether a provider has a way to sign out. Used by the
// handler and mirrored to the browser so the UI only offers the control when
// it will work.
func CanSignOut(p Provider) bool {
	if !p.Authenticated {
		return false
	}
	if signOutFor(p.ID) != nil {
		return true
	}
	// A user-key provider signs out by dropping the key it stores.
	return p.AuthType == AuthUserKey && keyStore.Remove != nil
}

// HandleSignOut serves POST /api/providers/{id}/signout: discard this
// workspace's credentials for one provider so another can be used instead.
//
// Managed providers are refused. Their credential is supplied by the platform,
// so "signing out" would either do nothing or break the workspace until an
// administrator restored it, and the user has no way to sign back in.
func HandleSignOut(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	p, ok := lookup(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown provider")
		return
	}
	if p.AuthType == AuthManaged {
		writeErr(w, http.StatusBadRequest, "this provider is managed by your organization and cannot be signed out here")
		return
	}

	var err error
	switch {
	case signOutFor(p.ID) != nil:
		err = signOutFor(p.ID)()
	case p.AuthType == AuthUserKey && keyStore.Remove != nil:
		err = keyStore.Remove(p.ID)
	default:
		writeErr(w, http.StatusBadRequest, "this provider does not support signing out")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not sign out: "+err.Error())
		return
	}

	// The old verdict describes credentials that no longer exist.
	Forget(p.ID)
	if onKeyChange != nil {
		onKeyChange()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// HandleProbe serves POST /api/providers/{id}/probe: re-test one provider now
// and return the fresh verdict, so the user does not have to wait for the
// background cycle after fixing something.
func HandleProbe(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	p, ok := lookup(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown provider")
		return
	}
	if !p.Authenticated {
		writeErr(w, http.StatusBadRequest, "sign in before testing this provider")
		return
	}
	state, note := Probe(r.Context(), p.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               state != ReachUnreachable,
		"reachability":     state,
		"reachabilityNote": note,
	})
}

// HandleRefreshReachability serves POST /api/providers/probe — re-test every
// authenticated provider.
func HandleRefreshReachability(w http.ResponseWriter, r *http.Request) {
	RefreshReachability(r.Context())
	writeJSON(w, http.StatusOK, Resolve())
}

// ProbeAllInBackground runs a full reachability sweep without blocking the
// caller. Used at startup and after the model list is rebuilt.
func ProbeAllInBackground() {
	go RefreshReachability(context.Background())
}
