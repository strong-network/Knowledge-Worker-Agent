// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// startTimeout bounds how long we wait for a freshly-started `opencode serve`
// to report healthy. The github-copilot provider can do OAuth token exchange
// on first use, so keep this generous.
const startTimeout = 60 * time.Second

// instance is a single running `opencode serve` process bound to one working
// directory.
type instance struct {
	workdir string
	cmd     *exec.Cmd
	client  *Client
	baseURL string

	mu       sync.Mutex
	lastUsed time.Time
	stale    bool
}

func (in *instance) touch() {
	in.mu.Lock()
	in.lastUsed = time.Now()
	in.mu.Unlock()
}

// markStale flags the instance for replacement on next use without killing it,
// so an in-flight turn finishes on the old process while the next turn spins up
// a fresh one that re-reads on-disk config (e.g. newly created agent files).
func (in *instance) markStale() {
	in.mu.Lock()
	in.stale = true
	in.mu.Unlock()
}

func (in *instance) isStale() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.stale
}

func (in *instance) idleSince() time.Time {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.lastUsed
}

// alive reports whether the process is still running.
func (in *instance) alive() bool {
	if in.cmd == nil || in.cmd.Process == nil {
		return false
	}
	// Signal 0 probes liveness without affecting the process.
	return in.cmd.Process.Signal(syscall.Signal(0)) == nil
}

func (in *instance) kill() {
	if in.cmd != nil && in.cmd.Process != nil {
		_ = in.cmd.Process.Kill()
		_, _ = in.cmd.Process.Wait()
	}
}

// Supervisor manages a pool of `opencode serve` processes, one per working
// directory. Instances are created lazily on first use for a directory and
// reaped after an idle period.
type Supervisor struct {
	bin      string        // path to the opencode binary
	hostname string        // bind hostname (loopback)
	password string        // OPENCODE_SERVER_PASSWORD for all instances
	idleTTL  time.Duration // reap instances idle longer than this
	env      []string      // base environment for child processes

	mu        sync.Mutex
	instances map[string]*instance // keyed by canonical workdir
	reaperOn  bool
	closed    bool
}

// Options configures a Supervisor.
type Options struct {
	Bin      string        // opencode binary path (required)
	Hostname string        // default 127.0.0.1
	Password string        // default: a random secret generated per process
	IdleTTL  time.Duration // default 10m; <=0 disables reaping
	Env      []string      // default os.Environ()
}

// NewSupervisor builds a Supervisor. It does not start any process; instances
// are created on demand by Ensure.
func NewSupervisor(opts Options) *Supervisor {
	hostname := opts.Hostname
	if hostname == "" {
		hostname = "127.0.0.1"
	}
	password := opts.Password
	if password == "" {
		password = randomSecret()
	}
	idle := opts.IdleTTL
	if idle == 0 {
		idle = 10 * time.Minute
	}
	env := opts.Env
	if env == nil {
		env = os.Environ()
	}
	return &Supervisor{
		bin:       opts.Bin,
		hostname:  hostname,
		password:  password,
		idleTTL:   idle,
		env:       env,
		instances: map[string]*instance{},
	}
}

// canonicalDir normalizes a workdir for use as a pool key, falling back to a
// usable directory when the supplied one is empty or invalid.
func canonicalDir(workdir, fallback string) string {
	d := workdir
	if info, err := os.Stat(d); err != nil || !info.IsDir() {
		d = fallback
	}
	if abs, err := filepath.Abs(d); err == nil {
		d = abs
	}
	return d
}

// Ensure returns a healthy Client for a server bound to the given working
// directory, starting one if necessary. If an existing instance for that
// directory has died, it is replaced.
func (s *Supervisor) Ensure(ctx context.Context, workdir, fallbackDir string) (*Client, string, error) {
	dir := canonicalDir(workdir, fallbackDir)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, "", fmt.Errorf("supervisor is closed")
	}
	if in, ok := s.instances[dir]; ok && in.alive() && !in.isStale() {
		in.touch()
		client := in.client
		s.mu.Unlock()
		return client, dir, nil
	}
	// Drop a dead or stale instance if present.
	if in, ok := s.instances[dir]; ok {
		in.kill()
		delete(s.instances, dir)
	}
	s.mu.Unlock()

	// Start outside the lock (it blocks on health).
	in, err := s.start(ctx, dir)
	if err != nil {
		return nil, "", err
	}

	s.mu.Lock()
	// Another goroutine may have started one concurrently; if so, keep theirs.
	if existing, ok := s.instances[dir]; ok && existing.alive() {
		s.mu.Unlock()
		in.kill()
		existing.touch()
		return existing.client, dir, nil
	}
	s.instances[dir] = in
	s.startReaperLocked()
	s.mu.Unlock()
	return in.client, dir, nil
}

// start launches a new `opencode serve` in dir and waits for it to be healthy.
func (s *Supervisor) start(ctx context.Context, dir string) (*instance, error) {
	port, err := freePort(s.hostname)
	if err != nil {
		return nil, fmt.Errorf("allocate port: %w", err)
	}
	baseURL := fmt.Sprintf("http://%s:%d", s.hostname, port)

	cmd := exec.Command(s.bin, "serve",
		"--hostname", s.hostname,
		"--port", fmt.Sprintf("%d", port),
	)
	cmd.Dir = dir
	cmd.Env = append(append([]string{}, s.env...),
		"OPENCODE_SERVER_PASSWORD="+s.password,
		"OPENCODE_SERVER_USERNAME=opencode",
	)
	// Discard server stdout/stderr; failures surface via the health check.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start opencode serve in %s: %w", dir, err)
	}

	client := NewClient(baseURL, "opencode", s.password)
	in := &instance{
		workdir:  dir,
		cmd:      cmd,
		client:   client,
		baseURL:  baseURL,
		lastUsed: time.Now(),
	}

	if err := client.waitHealthy(ctx, startTimeout); err != nil {
		in.kill()
		return nil, fmt.Errorf("opencode serve (%s) unhealthy: %w", dir, err)
	}
	log.Printf("[opencode-serve] started dir=%s url=%s pid=%d", dir, baseURL, cmd.Process.Pid)
	return in, nil
}

// startReaperLocked starts the idle-reaper goroutine once. Caller holds s.mu.
func (s *Supervisor) startReaperLocked() {
	if s.reaperOn || s.idleTTL <= 0 {
		return
	}
	s.reaperOn = true
	go s.reapLoop()
}

func (s *Supervisor) reapLoop() {
	ticker := time.NewTicker(s.idleTTL / 2)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		now := time.Now()
		for dir, in := range s.instances {
			if !in.alive() {
				delete(s.instances, dir)
				continue
			}
			if now.Sub(in.idleSince()) > s.idleTTL {
				log.Printf("[opencode-serve] reaping idle server dir=%s", dir)
				in.kill()
				delete(s.instances, dir)
			}
		}
		empty := len(s.instances) == 0
		if empty {
			s.reaperOn = false
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
	}
}

// Reload flags every live instance for replacement so the next turn for each
// working directory starts a fresh `opencode serve` that re-reads on-disk
// config (agents, etc.). Running turns are not interrupted — the swap happens
// the next time Ensure is called for that directory. Safe to call when no
// instances exist.
func (s *Supervisor) Reload() {
	s.mu.Lock()
	insts := make([]*instance, 0, len(s.instances))
	for _, in := range s.instances {
		insts = append(insts, in)
	}
	s.mu.Unlock()
	for _, in := range insts {
		in.markStale()
	}
}

// Close terminates all managed server processes.
func (s *Supervisor) Close() {
	s.mu.Lock()
	s.closed = true
	insts := make([]*instance, 0, len(s.instances))
	for dir, in := range s.instances {
		insts = append(insts, in)
		delete(s.instances, dir)
	}
	s.mu.Unlock()
	for _, in := range insts {
		in.kill()
	}
}

// Count returns the number of live managed instances (for tests/observability).
func (s *Supervisor) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.instances)
}

// Client returns the Client for an already-running instance bound to the given
// working directory, without starting one. ok is false when no live instance
// exists for that directory (e.g. no chat turn has run yet, or it was reaped).
// Use this for out-of-band operations (like live MCP connect/disconnect) that
// should act only on a process that is already serving the session.
func (s *Supervisor) Client(workdir, fallbackDir string) (*Client, bool) {
	dir := canonicalDir(workdir, fallbackDir)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false
	}
	if in, ok := s.instances[dir]; ok && in.alive() {
		return in.client, true
	}
	return nil, false
}

// freePort asks the OS for an unused TCP port on the given host.
func freePort(host string) (int, error) {
	l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func randomSecret() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Extremely unlikely; fall back to a time-based value.
		return fmt.Sprintf("opencode-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
