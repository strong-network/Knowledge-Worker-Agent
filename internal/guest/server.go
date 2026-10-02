// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package guest is the surface coworkers reach through the Workspace App:
// a separate listener on its own port with its own mux, so nothing is
// reachable here unless it is registered here. It must never read the
// owner's platform token or call the workspace sidecar.
package guest

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/sessions"
)

const (
	defaultPort  = 8766
	maxPromptLen = 32 * 1024
)

var (
	available atomic.Bool
	boundPort atomic.Int32
)

// Available reports whether the guest listener is serving.
func Available() bool { return available.Load() }

// Port is the port the guest listener serves on, and whether it is serving.
// The Workspace App publishes exactly this port.
func Port() (int, bool) { return int(boundPort.Load()), available.Load() }

// server holds what the guest routes need.
type server struct {
	files  fs.FS
	index  []byte
	origin string      // the only Origin accepted on a guest POST; "" refuses all
	ready  func() bool // opencode can run turns
}

// Start binds the guest listener on 127.0.0.1 unless KWA_GUEST_PORT is
// off. files is the built frontend. It never moves to another port when the
// port is taken: the Workspace App is keyed by port. The returned func stops it.
func Start(files fs.FS, ready func() bool) func(context.Context) {
	noop := func(context.Context) {}
	port, on, err := portFromEnv()
	if err != nil {
		log.Printf("[guest] %v; sharing unavailable", err)
		return noop
	}
	if !on {
		log.Print("[guest] disabled by KWA_GUEST_PORT")
		return noop
	}
	s, err := newServer(files, originFromEnv(port), ready)
	if err != nil {
		log.Printf("[guest] %v; sharing unavailable", err)
		return noop
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		log.Printf("[guest] cannot listen on 127.0.0.1:%d: %v; sharing unavailable", port, err)
		return noop
	}
	if s.origin == "" {
		log.Print("[guest] no expected origin (STRONG_NETWORK_WORKSPACE_ID / STRONG_NETWORK_WORKSPACE_APPS_DOMAIN unset); guest POSTs will be refused")
	}
	srv := &http.Server{Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second}
	boundPort.Store(int32(port))
	available.Store(true)
	log.Printf("[guest] listening on 127.0.0.1:%d origin=%q", port, s.origin)
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[guest] serve: %v", err)
		}
		available.Store(false)
	}()
	return func(ctx context.Context) {
		available.Store(false)
		if srv.Shutdown(ctx) != nil {
			srv.Close() // open /events connections never finish on their own
		}
	}
}

func newServer(files fs.FS, origin string, ready func() bool) (*server, error) {
	index, err := fs.ReadFile(files, "index.html")
	if err != nil {
		return nil, fmt.Errorf("frontend has no index.html: %w", err)
	}
	return &server{files: files, index: index, origin: origin, ready: ready}, nil
}

// portFromEnv reads KWA_GUEST_PORT: unset is the default port, off/0 and
// friends disable the surface.
func portFromEnv() (port int, on bool, err error) {
	v := strings.ToLower(strings.TrimSpace(env.Get("KWA_GUEST_PORT")))
	switch v {
	case "":
		return defaultPort, true, nil
	case "off", "false", "no", "0":
		return 0, false, nil
	}
	p, err := strconv.Atoi(v)
	if err != nil || p < 1 || p > 65535 {
		return 0, false, fmt.Errorf("KWA_GUEST_PORT=%q is not a port", v)
	}
	return p, true, nil
}

// originFromEnv is the share URL's origin, built the way the platform builds a
// Workspace App URL (GetWorkspaceApplicationURL). It is compared against Origin,
// never Host: the sidecar rewrites Host to localhost. KWA_GUEST_ORIGIN
// overrides it for local development only.
func originFromEnv(port int) string {
	if o := strings.TrimSpace(env.Get("KWA_GUEST_ORIGIN")); o != "" {
		return strings.TrimRight(o, "/")
	}
	ws := strings.TrimSpace(os.Getenv("STRONG_NETWORK_WORKSPACE_ID"))
	domain := strings.TrimSpace(os.Getenv("STRONG_NETWORK_WORKSPACE_APPS_DOMAIN"))
	if ws == "" || domain == "" {
		return ""
	}
	return fmt.Sprintf("https://ws-%s-port-%d.%s", ws, port, domain)
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /s/{id}", s.page)
	mux.HandleFunc("GET /assets/{file...}", s.asset)
	mux.HandleFunc("GET /guest/api/identity", s.whoami)
	mux.HandleFunc("POST /guest/identify", s.identify)
	mux.HandleFunc("GET /guest/api/sessions/{id}", s.meta)
	mux.HandleFunc("GET /guest/api/sessions/{id}/history", s.history)
	mux.HandleFunc("GET /guest/api/sessions/{id}/events", s.events)
	mux.HandleFunc("POST /guest/api/chat", s.chat)
	mux.HandleFunc("GET /guest/api/sessions/{id}/permission", s.pendingPermission)
	mux.HandleFunc("POST /guest/api/sessions/{id}/permission", s.answerPermission)
	mux.HandleFunc("GET /guest/api/sessions/{id}/files/browse", s.filesBrowse)
	mux.HandleFunc("GET /guest/api/sessions/{id}/files/view", s.filesView)
	mux.HandleFunc("GET /guest/api/sessions/{id}/files/raw", s.filesRaw)
	mux.HandleFunc("GET /guest/api/sessions/{id}/files/download", s.filesDownload)
	mux.HandleFunc("POST /guest/api/sessions/{id}/files/save", s.filesSave)
	mux.HandleFunc("POST /guest/api/sessions/{id}/files/upload", s.filesUpload)
	mux.HandleFunc("POST /guest/api/sessions/{id}/files/directory", s.filesDirectory)
	return secureHeaders(mux)
}

// secureHeaders applies to every guest response. The share URL carries the
// session id, so a link clicked in a rendered answer must not leak it in
// Referer; and the page must not be framed by another site.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// shared answers 404 for a session that does not exist or is not shared, and
// the same 404 for both, so a guest cannot learn which ids exist.
func shared(w http.ResponseWriter, sid string) bool {
	if sid == "" || !db.IsSessionShared(sid) {
		http.Error(w, "Not found", http.StatusNotFound)
		return false
	}
	return true
}

func (s *server) sameOrigin(w http.ResponseWriter, r *http.Request) bool {
	if s.origin == "" || r.Header.Get("Origin") != s.origin {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func mustIdentify(w http.ResponseWriter, r *http.Request) (identity, bool) {
	id, ok := readIdentity(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identify first"})
	}
	return id, ok
}

func (s *server) page(w http.ResponseWriter, r *http.Request) {
	if !shared(w, r.PathValue("id")) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(s.index)
}

func (s *server) asset(w http.ResponseWriter, r *http.Request) {
	name := "assets/" + r.PathValue("file")
	if !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}
	if info, err := fs.Stat(s.files, name); err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.ServeFileFS(w, r, s.files, name)
}

func (s *server) whoami(w http.ResponseWriter, r *http.Request) {
	id, ok := readIdentity(r)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"identified": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"identified": true, "id": id.ID, "name": id.Name})
}

func (s *server) identify(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(w, r) {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	name := cleanName(body.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	id := newIdentity(r, name)
	writeIdentity(w, id)
	writeJSON(w, http.StatusOK, map[string]any{"identified": true, "id": id.ID, "name": id.Name})
}

// meta is readable before identifying, so the name gate can say whose chat it is.
func (s *server) meta(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("id")
	if !shared(w, sid) {
		return
	}
	cfg, _ := db.GetSessionConfig(sid)
	o := db.SessionShareOptions(sid)
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":        sid,
		"title":             cfg.Label,
		"owner_name":        config.OwnerFullName,
		"allow_permissions": o.AllowPermissions,
		"allow_files":       o.AllowFiles,
	})
}

func (s *server) history(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("id")
	if !shared(w, sid) {
		return
	}
	if _, ok := mustIdentify(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, History(db.GetMessages(sid)))
}

func (s *server) events(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("id")
	if !shared(w, sid) {
		return
	}
	id, ok := mustIdentify(w, r)
	if !ok {
		return
	}
	// Opening the live view is what makes someone a participant: it is the
	// moment they are watching the chat, not merely holding its link.
	if err := db.RecordGuest(sid, id.ID, id.Name); err != nil {
		log.Printf("[ERROR] guest: record participant: %v", err)
	}
	sessions.ServeEvents(w, r, sid, sessions.EventsOptions{
		GuestID:      id.ID,
		Filter:       frameFor(sid),
		StillAllowed: func() bool { return db.IsSessionShared(sid) },
		Options:      func() map[string]any { return optionsFrame(sid) },
	})
}

// chat accepts a prompt and answers at once: the turn itself reaches every
// viewer, this guest included, on /events.
func (s *server) chat(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(w, r) {
		return
	}
	var body struct {
		SessionID string `json:"session_id"`
		Prompt    string `json:"prompt"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPromptLen+1024)).Decode(&body) != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	sid := strings.TrimSpace(body.SessionID)
	if !shared(w, sid) {
		return
	}
	id, ok := mustIdentify(w, r)
	if !ok {
		return
	}
	prompt := strings.TrimSpace(body.Prompt)
	if prompt == "" || len(prompt) > maxPromptLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "prompt is empty or too long"})
		return
	}
	if db.IsSessionDeprecated(sid) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this chat is read-only"})
		return
	}
	if s.ready != nil && !s.ready() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the assistant is starting up"})
		return
	}
	if ok, wait := spendPrompt(sid); !ok {
		log.Printf("[guest] session=%s prompt refused: the chat's guest budget is spent", sid)
		refuseForBudget(w, wait)
		return
	}
	cfg, _ := db.GetSessionConfig(sid)
	_, queued := chat.SendOrEnqueue(sid, prompt, cfg, chat.Turn{Author: chat.Author{ID: id.ID, Name: id.Name}})
	status := "started"
	if queued != nil {
		status = "queued"
	}
	log.Printf("[guest] session=%s prompt %s by guest %s", sid, status, id.ID)
	writeJSON(w, http.StatusAccepted, map[string]any{"status": status, "queue_len": chat.QueueLen(sid)})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
