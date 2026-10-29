// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package versions

import (
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

type versionJSON struct {
	V      int    `json:"v"`
	At     string `json:"at"`
	Author string `json:"author"`
	Name   string `json:"name,omitempty"`
	Size   int64  `json:"size"`
}

// HandleList answers GET /api/files/versions?path=: the file's versions, oldest
// first. It first takes a version of any change since the latest, so the
// newest is what the file holds now.
func HandleList(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	abs, _ := filepath.Abs(path)
	list := []versionJSON{}
	if store != "" && Versioned(filepath.Base(abs)) {
		Current(abs)
		versions, err := db.FileVersions(abs)
		if err != nil {
			http.Error(w, "Could not read the file's versions", http.StatusInternalServerError)
			return
		}
		for _, fv := range versions {
			vj := versionJSON{V: fv.V, At: time.UnixMilli(fv.At).UTC().Format(time.RFC3339Nano), Author: fv.Author, Size: fv.Size}
			if name, ok := strings.CutPrefix(fv.Author, "guest:"); ok {
				vj.Author, vj.Name = "guest", name
			}
			list = append(list, vj)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{"versions": list})
}

// HandleContent answers GET /api/files/version?path=&v=: that version's text,
// or with download=1 the file as it was then, named name-vN.ext.
func HandleContent(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	abs, _ := filepath.Abs(q.Get("path"))
	v, err := strconv.Atoi(q.Get("v"))
	if q.Get("path") == "" || err != nil {
		http.Error(w, "path and v are required", http.StatusBadRequest)
		return
	}
	fv, ok, err := db.FileVersionAt(abs, v)
	if err != nil || !ok {
		http.Error(w, "That version isn't there", http.StatusNotFound)
		return
	}
	data, err := read(fv.SHA)
	if err != nil {
		http.Error(w, "That version isn't there", http.StatusNotFound)
		return
	}
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	if q.Get("download") != "" {
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": versionName(filepath.Base(abs), v)}))
	} else {
		h.Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.Write(data)
}

func versionName(name string, v int) string {
	ext := filepath.Ext(name)
	return fmt.Sprintf("%s-v%d%s", strings.TrimSuffix(name, ext), v, ext)
}
