// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package office

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// HandlePreview answers GET /api/files/office-preview?path=… with a PDF of the
// file. Errors are JSON {error}, and {missing: true} when LibreOffice is absent.
// Missing is a 409, not a 503: a workspace proxy may drop the body of a 503
// it doesn't recognise, and the viewer needs `missing` to offer the install.
func HandlePreview(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path is required", false)
		return
	}
	abs, _ := filepath.Abs(path)
	if !Previewable(abs) {
		writeError(w, http.StatusUnsupportedMediaType, "only PowerPoint, Word and Excel files are converted", false)
		return
	}
	pdf, err := defaultConverter.PDF(r.Context(), abs)
	switch {
	case errors.Is(err, ErrMissing):
		writeError(w, http.StatusConflict, err.Error(), true)
		return
	case errors.Is(err, os.ErrNotExist):
		writeError(w, http.StatusNotFound, "File not found", false)
		return
	case err != nil:
		writeError(w, http.StatusUnprocessableEntity, err.Error(), false)
		return
	}
	name := strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs)) + ".pdf"
	h := w.Header()
	h.Set("Content-Type", "application/pdf")
	h.Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, strings.ReplaceAll(name, `"`, "")))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, pdf)
}

// HandleStatus answers GET /api/office/status.
func HandleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"available":    Find() != "",
		"version":      Version,
		"download_mb":  DownloadMB,
		"installed_mb": InstalledMB,
		"install":      defaultInstaller.Status(),
	})
}

// HandleInstall answers POST /api/office/install by starting an install,
// unless one is running. Progress is read from HandleStatus.
func HandleInstall(w http.ResponseWriter, _ *http.Request) {
	started := defaultInstaller.Start()
	writeJSON(w, http.StatusAccepted, map[string]any{"started": started, "install": defaultInstaller.Status()})
}

func writeError(w http.ResponseWriter, status int, msg string, missing bool) {
	body := map[string]any{"error": msg}
	if missing {
		body["missing"] = true
	}
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
