// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package fileshare

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/fileshare/viewer"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/workspaceapps"
)

// previewAppName names the app that publishes the preview listener. An app on
// the preview port with another name is never changed.
const previewAppName = "Knowledge Worker Agent preview"

const defaultPreviewPort = 8769

// scriptsOff starts every reason the preview can't run scripts; the app then
// previews the file with scripts off.
const scriptsOff = "Scripts are off in this preview: "

// previewPort reads KWA_PREVIEW_PORT. "off" turns scripts in the preview off
// and gives 0.
func previewPort() (int, error) {
	v := strings.ToLower(strings.TrimSpace(env.Get("KWA_PREVIEW_PORT")))
	switch v {
	case "":
		return defaultPreviewPort, nil
	case "off", "false", "no", "0":
		return 0, nil
	}
	p, err := strconv.Atoi(v)
	if err != nil || p < 1 || p > 65535 || reserved[p] {
		return 0, fmt.Errorf("KWA_PREVIEW_PORT=%q is not a port", v)
	}
	return p, nil
}

type previewRequest struct {
	Path string `json:"path"`
}

// HandlePreviewLink serves POST /api/files/preview-link: a link that runs an
// HTML file's scripts on the preview listener's own origin, published by an
// app that only the owner can open.
func HandlePreviewLink(w http.ResponseWriter, r *http.Request) {
	var req previewRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if !filepath.IsAbs(req.Path) {
		writeFailure(w, &failure{http.StatusBadRequest, "The path must be absolute."})
		return
	}
	path := filepath.Clean(req.Path)
	f, _, err := viewer.OpenPreview(path)
	if err != nil {
		writeFailure(w, &failure{http.StatusConflict, err.Error()})
		return
	}
	f.Close()
	port, err := previewPort()
	switch {
	case err != nil:
		writeFailure(w, &failure{http.StatusConflict, scriptsOff + err.Error()})
		return
	case port == 0:
		writeFailure(w, &failure{http.StatusConflict, scriptsOff + "they're turned off in this workspace."})
		return
	case viewer.AppsDomain() == "":
		writeFailure(w, &failure{http.StatusConflict, scriptsOff + "Knowledge Worker Agent isn't running in a workspace."})
		return
	}
	token := workspaceapps.OwnerToken(r)
	if token == "" {
		writeFailure(w, &failure{http.StatusForbidden, scriptsOff + "this request did not carry your workspace credential. Open Knowledge Worker Agent from your workspace."})
		return
	}
	mu.Lock()
	err = ensurePreview(r.Context(), token, port)
	mu.Unlock()
	if err != nil {
		writeFailure(w, err)
		return
	}
	tok, exp := viewer.NewPreview(path)
	origin := viewer.Origin(port)
	writeJSON(w, http.StatusOK, map[string]string{
		"url": origin + "/p/" + tok, "origin": origin, "expires": exp.UTC().Format(time.RFC3339),
	})
}

// ensurePreview serves the preview listener on port and makes sure an app
// publishes it to nobody but the owner.
func ensurePreview(ctx context.Context, token string, port int) error {
	if err := viewer.BindPreview(port); err != nil {
		log.Printf("[fileshare] preview on port %d: %v", port, err)
		return &failure{http.StatusConflict, fmt.Sprintf(scriptsOff+"port %d is in use.", port)}
	}
	all, err := listApps(ctx, token)
	if err != nil {
		return platformFailureAs(scriptsOff, "list apps", err)
	}
	app, has := byPort(all)[port]
	switch {
	case !has:
		if _, err := createApp(ctx, token, uint32(port), previewAppName, false, nil); err != nil {
			return platformFailureAs(scriptsOff, "create the preview app", err)
		}
		log.Printf("[fileshare] created the preview app on port %d", port)
	case app.Name != previewAppName:
		return &failure{http.StatusConflict, fmt.Sprintf(scriptsOff+"port %d belongs to another app, %q.", port, app.Name)}
	case app.IsPublic || app.IsSharedWithTeam || len(app.UserIDs) > 0:
		app.IsPublic, app.IsSharedWithTeam, app.UserIDs = false, false, nil
		if err := editApp(ctx, token, app); err != nil {
			return platformFailureAs(scriptsOff, "edit the preview app", err)
		}
		log.Printf("[fileshare] the preview app was shared with others; it is the owner's only again")
	}
	return nil
}
