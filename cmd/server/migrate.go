// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/migration"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodeinstaller"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/workspaceheartbeat"
)

// migrationAtStartup reads KWA_MIGRATE.
func migrationAtStartup() migration.Mode {
	mode, err := migration.ModeFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ⚠ %v; nothing changed\n", err)
		return migration.Off
	}
	if mode == migration.Off && os.Getenv(migration.EnvMode) == "" {
		fmt.Fprintf(os.Stderr, "  · One-folder migration: undone earlier, so it doesn't run; %s=on runs it again\n", migration.EnvMode)
	}
	return mode
}

// migrationView is the migration's state, as /api/status reports it for the
// app's banner. Sizes are already formatted for people.
type migrationView struct {
	State   string   `json:"state"` // running, planned, blocked, failed or stray
	Phase   string   `json:"phase,omitempty"`
	Done    int      `json:"done,omitempty"`
	Total   int      `json:"total,omitempty"`
	Minutes int      `json:"minutes,omitempty"`
	Need    string   `json:"need,omitempty"`
	Free    string   `json:"free,omitempty"`
	Short   string   `json:"short,omitempty"` // to free up, when disk space is short
	Reasons []string `json:"reasons,omitempty"`
	Path    string   `json:"path,omitempty"`
	Undo    bool     `json:"undo,omitempty"` // of undoing the migration
}

var migrationState atomic.Pointer[migrationView]

// Test hooks.
var (
	prepareOpencode = ensureOpencode
	runMigration    = migration.Run
)

// viewOf turns a report into the banner's state: planned when the migration
// could run, blocked with the reasons when it couldn't.
func viewOf(r migration.Report) *migrationView {
	v := &migrationView{State: "planned", Minutes: minutes(r.Duration), Need: migration.Bytes(r.NeedBytes), Free: migration.Bytes(r.FreeBytes), Undo: r.Undo}
	if r.Ready() {
		return v
	}
	v.State = "blocked"
	v.Reasons = append(v.Reasons, r.Errors...)
	for _, p := range r.Problems {
		v.Reasons = append(v.Reasons, p.String())
	}
	if r.NeedBytes > r.FreeBytes {
		v.Short = migration.Bytes(r.NeedBytes - r.FreeBytes)
	}
	return v
}

func minutes(d time.Duration) int { return int((d + time.Minute - 1) / time.Minute) }

// runMigrationAtStartup carries the migration out, or with mode Undo undoes
// it, before anything else opens a file it moves. Meanwhile a status page
// answers on KWA's port, and the workspace heartbeat reports the workspace as
// busy, so it isn't stopped for being idle. On success, the configuration is
// read again, now in the root or back at the old places.
func runMigrationAtStartup(mode migration.Mode) {
	undo := mode == migration.Undo
	if layout.Migrated() != undo {
		if undo {
			fmt.Fprintf(os.Stderr, "  ⚠ %s=%s: the one-folder migration hasn't run, so there's nothing to undo\n", migration.EnvMode, mode)
		}
		return
	}
	if reason, failed := migration.LastFailure(version, mode); failed {
		fmt.Fprintf(os.Stderr, "  ⚠ %s=%s failed with this release (%s); it won't run again until the next one\n", migration.EnvMode, mode, reason)
		migrationState.Store(&migrationView{State: "failed", Reasons: []string{reason}, Undo: undo})
		return
	}
	var migrating atomic.Bool
	migrating.Store(true)
	stopHeartbeat := workspaceheartbeat.Start(context.Background(), migrating.Load)
	status := &statusPage{undo: undo}
	stop, err := startStatusServer(fmt.Sprintf("%s:%d", config.Host, config.Port), status)
	if err != nil {
		stopHeartbeat()
		fmt.Fprintf(os.Stderr, "  ⚠ One-folder migration: port %d is in use, so another server may be running; nothing changed\n", config.Port)
		return
	}
	// Bootstrap installs the pinned opencode only later, and the checks refuse any other version.
	if err := prepareOpencode(); err != nil {
		fmt.Fprintf(os.Stderr, "  ⚠ One-folder migration: opencode %s: %v\n", opencodeinstaller.PinnedVersion, err)
	}
	err = runMigration(migration.Options{
		OurDB: config.DBPath, OpencodeBin: config.OpencodeBin, Host: config.Host, Port: config.Port,
		PortHeld: true, Release: version, Out: os.Stdout, Progress: status.set, Undo: undo,
	})
	stop()
	migrating.Store(false)
	stopHeartbeat()

	var refused *migration.Refused
	switch {
	case err == nil:
		config.Init()
	case errors.As(err, &refused):
		v := &migrationView{State: "blocked", Reasons: refused.Reasons, Undo: undo}
		if refused.Report != nil {
			v = viewOf(*refused.Report)
		}
		migrationState.Store(v)
	default:
		log.Printf("%s=%s: %v", migration.EnvMode, mode, err)
		migrationState.Store(&migrationView{State: "failed", Reasons: []string{err.Error()}, Undo: undo})
	}
}

// statusPage holds the running migration's progress.
type statusPage struct {
	mu   sync.Mutex
	p    migration.Progress
	undo bool
}

func (s *statusPage) set(p migration.Progress) {
	s.mu.Lock()
	s.p = p
	s.mu.Unlock()
}

func (s *statusPage) view() migrationView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return migrationView{State: "running", Phase: s.p.Phase, Done: s.p.Done, Total: s.p.Total, Minutes: minutes(s.p.Left), Undo: s.undo}
}

//go:embed migrating.html
var migratingHTML []byte

// statusHandler answers every request while the migration runs: the status
// page for pages, 503 for the API, and ok for health checks, so the
// container isn't restarted meanwhile.
func statusHandler(s *statusPage) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		v := s.view()
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "migrating", "opencode_ready": false, "migration": v})
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "10")
		http.Error(w, `{"error":"Knowledge Worker Agent is reorganizing its files"}`, http.StatusServiceUnavailable)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(migratingHTML)
	})
	return mux
}

// startStatusServer serves the status page on addr until the returned
// function stops it, freeing the port for the server proper.
func startStatusServer(addr string, s *statusPage) (func(), error) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: statusHandler(s), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(l)
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}, nil
}

// runMigrate handles `migrate dry-run`, which prints the report of the
// migration, or once it has run, of its undo; and `migrate on` and `migrate
// undo`, which carry them out with the server stopped. It exits 0 on success,
// 1 when something stopped it, and 2 on wrong usage.
func runMigrate(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: knowledge-worker-agent migrate dry-run|on|undo")
		return 2
	}
	mode := migration.Mode(args[0])
	if mode != migration.DryRun && mode != migration.On && mode != migration.Undo {
		fmt.Fprintln(os.Stderr, "Usage: knowledge-worker-agent migrate dry-run|on|undo")
		return 2
	}
	if err := migration.Recover(os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "  ✗ One-folder migration: %v\n", err)
		return 1
	}
	config.Init()
	if mode == migration.DryRun {
		assess := migration.Assess
		if layout.Migrated() {
			assess = migration.AssessUndo
		}
		r := assess(config.DBPath, config.OpencodeBin)
		r.Write(os.Stdout, migration.DryRun)
		if !r.Ready() {
			return 1
		}
		return 0
	}
	err := migration.Run(migration.Options{
		OurDB: config.DBPath, OpencodeBin: config.OpencodeBin,
		Host: config.Host, Port: config.Port, Release: version, Out: os.Stdout, Undo: mode == migration.Undo,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ✗ migrate %s: %v\n", mode, err)
		return 1
	}
	return 0
}
