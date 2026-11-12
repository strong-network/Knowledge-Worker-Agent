// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcpauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/entra"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/mcp"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodev2"
)

// Stand-ins that tests replace.
var (
	remoteURL    = mcp.RemoteURL
	oauthScope   = mcp.OAuthScope
	detectEntra  = entra.Detect
	entraEnabled = opencodev2.Enabled
)

// startEntra starts Knowledge Worker Agent's own Microsoft sign-in when the
// connector signs in with Microsoft Entra ID, which opencode can't. handled is
// false for any other connector, and when its metadata can't be read: opencode
// then signs in, or says why not.
func startEntra(server string) (handled bool, err error) {
	u, ok := remoteURL(server)
	if !ok {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	target, isEntra, err := detectEntra(ctx, u)
	if !isEntra {
		return false, nil
	}
	if err != nil {
		return true, ErrOAuthUnsupported{Msg: fmt.Sprintf("%s signs in with Microsoft, but %v.", server, err)}
	}
	// Some servers publish a permission Microsoft can't resolve; the entry can name the right one.
	if s := strings.Fields(oauthScope(server)); len(s) > 0 {
		target = target.WithScopes(s)
	}
	if !entraEnabled() {
		return true, ErrOAuthUnsupported{Msg: fmt.Sprintf("%s signs in with Microsoft, which needs opencode 2.x.", server)}
	}
	app, ok := entraApp(server)
	if !ok {
		return true, ErrOAuthUnsupported{Msg: fmt.Sprintf("%s signs in with Microsoft, which needs an app registered for "+
			"Knowledge Worker Agent. Ask your administrator to set KWA_ENTRA_CLIENT_ID and KWA_ENTRA_TENANT_ID.", server)}
	}
	p := entra.Begin(server, app, target)

	auth.mu.Lock()
	defer auth.mu.Unlock()
	if (auth.running || auth.cmd != nil) && !auth.done {
		return true, fmt.Errorf("an authentication flow is already in progress")
	}
	auth.resetLocked(server)
	auth.running = true
	auth.entra = p
	auth.verifyURL = p.URL
	auth.startedAt = time.Now()
	flowCtx, stop := context.WithTimeout(context.Background(), authTimeout)
	auth.cancel = stop
	go func() {
		<-flowCtx.Done()
		if !errors.Is(flowCtx.Err(), context.DeadlineExceeded) {
			return
		}
		auth.mu.Lock()
		defer auth.mu.Unlock()
		if auth.entra == p && !auth.done {
			auth.running, auth.done = false, true
			auth.err = "authentication timed out waiting for approval. Retry and approve in your browser."
		}
	}()
	return true, nil
}

// entraApp is the app registration a connector signs in with: its artifact's,
// with the settings for any part the artifact leaves out.
func entraApp(server string) (entra.App, bool) {
	app, _ := entra.Configured()
	id, tenant := mcp.EntraApp(server)
	if id != "" {
		app.ClientID = id
	}
	if tenant != "" {
		app.Tenant = tenant
	}
	return entra.NewApp(app.ClientID, app.Tenant)
}

// pendingEntra returns the Microsoft sign-in waiting for its address, if one is.
func pendingEntra() *entra.Pending {
	auth.mu.Lock()
	defer auth.mu.Unlock()
	if auth.entra != nil && !auth.done {
		return auth.entra
	}
	return nil
}

// completeEntra finishes a Microsoft sign-in with the address the user pasted.
// A refused address or code leaves the sign-in waiting, as the sign-in page can
// be opened again; a token opencode didn't take ends it.
func completeEntra(w http.ResponseWriter, p *entra.Pending, pasted string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	rec, err := p.Complete(ctx, pasted)
	if err != nil && rec == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	auth.mu.Lock()
	if auth.entra == p && !auth.done {
		auth.running, auth.done = false, true
		auth.success = err == nil
		auth.account = rec.Account
		auth.err = ""
		if err != nil {
			auth.err = err.Error()
		}
		auth.cancel()
	}
	info := snapshotLocked()
	auth.mu.Unlock()
	if err == nil && onAuthSuccess != nil {
		go onAuthSuccess()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": err == nil, "status_code": http.StatusOK, "info": info, "error": info.Error})
}
