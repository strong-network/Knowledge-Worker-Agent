// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// migrationView is what /api/status reports of the one-folder migration for
// the app's banner: a database an older release left at the old place.
type migrationView struct {
	State string `json:"state"` // stray
	Path  string `json:"path,omitempty"`
}

var migrationState atomic.Pointer[migrationView]

//go:embed unmoved.html
var unmovedHTML string

// serveUnmoved stops a workspace whose files were never moved into the root,
// before anything opens them: this release can't move them, and starting
// would show none of them. Every page says so, and why, until the workspace
// is started with a release that moves them.
func serveUnmoved(why string) {
	fmt.Fprintf(os.Stderr, "  ✗ Knowledge Worker Agent can't start: %s Start release 2.1 once, which moves the files into %s, then this one.\n", why, layout.Root())
	srv := &http.Server{Handler: unmovedHandler(why), ReadHeaderTimeout: 10 * time.Second}
	config.InitListen()
	ls, err := listenAll(config.ListenAddrs())
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ✗ %v\n", err)
		os.Exit(1)
	}
	for _, l := range ls[1:] {
		go srv.Serve(l)
	}
	_ = srv.Serve(ls[0])
	os.Exit(1)
}

// unmovedHandler answers every request: the page for pages, 503 for the API,
// and ok for health checks, so the container isn't restarted over and over.
func unmovedHandler(why string) http.Handler {
	page := []byte(strings.Replace(unmovedHTML, "{{why}}", html.EscapeString(why), 1))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "blocked", "opencode_ready": false})
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"Knowledge Worker Agent can't start: its files haven't been moved"}`, http.StatusServiceUnavailable)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(page)
	})
	return mux
}
