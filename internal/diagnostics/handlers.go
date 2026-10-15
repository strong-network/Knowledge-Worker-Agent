// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package diagnostics

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	viewLines     = 1000
	maxViewLines  = 5000
	viewBytes     = 4 << 20
	downloadLines = 50000
	downloadBytes = 16 << 20
)

// HandleReport serves GET /api/diagnostics.
func HandleReport(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, Collect())
}

// HandleDisk serves GET /api/diagnostics/disk.
func HandleDisk(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, CollectDisk())
}

// HandleLog serves GET /api/diagnostics/log?source=kwa|opencode. It returns
// the newest lines as JSON (lines=, up to 5000), or with download=1 a text
// file of up to 50000 lines. Lines are redacted either way.
func HandleLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	source := q.Get("source")
	if source == "" {
		source = LogKWA
	}
	if source != LogKWA && source != LogOpencode {
		http.Error(w, `{"error":"source must be kwa or opencode"}`, http.StatusBadRequest)
		return
	}
	if q.Get("download") == "1" {
		t := Tail(source, downloadLines, downloadBytes)
		name := fmt.Sprintf("%s-log-%s.txt", source, time.Now().UTC().Format("20060102-150405"))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if t.Truncated {
			fmt.Fprintf(w, "[Older lines left out: the newest %d are below.]\n", len(t.Lines))
		}
		_, _ = w.Write([]byte(strings.Join(t.Lines, "\n") + "\n"))
		return
	}
	n := viewLines
	if v, err := strconv.Atoi(q.Get("lines")); err == nil && v > 0 {
		n = min(v, maxViewLines)
	}
	writeJSON(w, Tail(source, n, viewBytes))
}

// HandleRefreshState serves GET /api/diagnostics/refresh-config: the latest
// refresh's state, or null.
func HandleRefreshState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"refresh": CurrentRefresh()})
}

// HandleRefreshStart serves POST /api/diagnostics/refresh-config. It starts a
// refresh and answers 202 at once; a refresh can take longer than a request.
func HandleRefreshStart(w http.ResponseWriter, r *http.Request) {
	st, err := StartRefresh()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "refresh": CurrentRefresh()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"refresh": st})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
