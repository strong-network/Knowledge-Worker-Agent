// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodev2

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	neturl "net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/skilltokens"
)

const (
	startTimeout = 60 * time.Second
	// Connectors that haven't connected by then are left to connect in the
	// background, at the cost of the reminder in the turn that sees them.
	warmTimeout = 20 * time.Second
	// A server that failed to start isn't tried again for this long; commands
	// use a private server meanwhile.
	retryAfter = 15 * time.Second
)

type server struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	exited   chan struct{}
	url      string
	password string
	stderr   *tailBuffer
	failedAt time.Time
	lastErr  error
	// stopped is set by Stop, at shutdown: a request arriving after it must
	// not start a server that would outlive KWA.
	stopped bool
	// overlay is the configuration document the server was started with.
	overlay string
	// env is the environment the server was started with, without its password.
	env []string
}

var srv server

// Ensure returns the URL and password of KWA's opencode server, starting it
// and waiting for its connectors when it isn't running.
func Ensure(ctx context.Context) (url, password string, err error) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.runningLocked() {
		return srv.url, srv.password, nil
	}
	if srv.stopped {
		return "", "", errors.New("the opencode server has been stopped")
	}
	if !srv.failedAt.IsZero() && time.Since(srv.failedAt) < retryAfter {
		return "", "", srv.lastErr
	}
	if err := srv.startLocked(ctx); err != nil {
		srv.failedAt, srv.lastErr = time.Now(), err
		log.Printf("[opencode-v2] server did not start: %v", err)
		return "", "", err
	}
	srv.failedAt, srv.lastErr = time.Time{}, nil
	return srv.url, srv.password, nil
}

// Running returns the server's URL and password when it is up, without
// starting it.
func Running() (url, password string, ok bool) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if !srv.runningLocked() {
		return "", "", false
	}
	return srv.url, srv.password, true
}

// Stop ends the server for good, at shutdown. Safe to call when it isn't
// running.
func Stop() {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	srv.stopped = true
	srv.stopLocked()
	removeScratch()
}

// Restart replaces the server with a fresh one, which reads the configuration
// again.
func Restart(ctx context.Context) error {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	srv.stopLocked()
	srv.failedAt, srv.lastErr = time.Time{}, nil
	if err := srv.startLocked(ctx); err != nil {
		srv.failedAt, srv.lastErr = time.Now(), err
		return err
	}
	return nil
}

func (s *server) runningLocked() bool {
	if s.cmd == nil {
		return false
	}
	select {
	case <-s.exited:
		return false
	default:
		return true
	}
}

func (s *server) startLocked(ctx context.Context) error {
	bin := config.OpencodeBin
	if bin == "" {
		return errors.New("no opencode binary")
	}
	keepDatabaseBeforeV2()
	port, err := freePort()
	if err != nil {
		return fmt.Errorf("allocate a port: %w", err)
	}
	password, err := randomPassword()
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, "serve", "--hostname", "127.0.0.1", "--port", fmt.Sprint(port))
	cmd.Dir = workDir()
	env := configEnv(processEnv())
	cmd.Env = append(slices.Clone(env), "OPENCODE_PASSWORD="+password, "OPENCODE_DISABLE_AUTOUPDATE=1")
	s.env = env
	s.overlay = ""
	for _, kv := range cmd.Env {
		if v, ok := strings.CutPrefix(kv, "OPENCODE_CONFIG="); ok {
			s.overlay = v
		}
	}
	// Its own process group, so stopping it also stops the local connectors it runs.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	tail := &tailBuffer{max: 8 << 10}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s serve: %w", bin, err)
	}
	exited := make(chan struct{})
	go func() {
		err := cmd.Wait()
		close(exited)
		log.Printf("[opencode-v2] server pid=%d exited: %v %s", cmd.Process.Pid, err, strings.TrimSpace(tail.String()))
	}()
	s.cmd, s.exited, s.stderr = cmd, exited, tail
	s.url, s.password = fmt.Sprintf("http://127.0.0.1:%d", port), password

	startCtx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	if err := waitHealthy(startCtx, s.url, s.password, exited); err != nil {
		s.stopLocked()
		if msg := strings.TrimSpace(tail.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, lastLine(msg))
		}
		return err
	}
	log.Printf("[opencode-v2] server pid=%d listening on %s", cmd.Process.Pid, s.url)

	warmCtx, cancelWarm := context.WithTimeout(ctx, warmTimeout)
	defer cancelWarm()
	// 2.x lists no connectors until it has loaded its configuration, which a
	// large one takes longer than the grace for.
	grace := 1500 * time.Millisecond
	if declaredConnectors(globalConfigPath(), s.overlay) > 0 {
		grace = warmTimeout
	}
	n, pending := warm(warmCtx, s.url, s.password, "/api/mcp", grace)
	connectorCount.Store(int64(n))
	log.Printf("[opencode-v2] connectors ready: %d, still connecting: %d", n-pending, pending)
	return nil
}

func (s *server) stopLocked() {
	if s.cmd == nil || s.cmd.Process == nil {
		s.cmd = nil
		return
	}
	cmd, exited := s.cmd, s.exited
	s.cmd = nil
	pgid := -cmd.Process.Pid
	_ = syscall.Kill(pgid, syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		_ = syscall.Kill(pgid, syscall.SIGKILL)
		<-exited
	}
}

func waitHealthy(ctx context.Context, url, password string, exited <-chan struct{}) error {
	for {
		var info map[string]any
		if err := get(ctx, url, password, "/api/info", &info); err == nil {
			return nil
		}
		select {
		case <-exited:
			return errors.New("the server exited")
		case <-ctx.Done():
			return errors.New("the server did not answer in time")
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// warm waits until no connector is still connecting, and returns how many
// there are and how many still were when it stopped waiting. Asking for the
// list is what makes the server start connecting them. An empty list counts
// as final only after grace: right after start it can still fill.
func warm(ctx context.Context, url, password, path string, grace time.Duration) (total, pending int) {
	start := time.Now()
	for {
		var resp struct {
			Data []struct {
				Status struct {
					Status string `json:"status"`
				} `json:"status"`
			} `json:"data"`
		}
		if err := get(ctx, url, password, path, &resp); err == nil {
			total, pending = len(resp.Data), 0
			for _, m := range resp.Data {
				if m.Status.Status == "pending" {
					pending++
				}
			}
			if pending == 0 && (total > 0 || time.Since(start) >= grace) {
				return total, 0
			}
		}
		select {
		case <-ctx.Done():
			return total, pending
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// folderWarmTimeout bounds how long a turn waits for its folder's connectors.
const folderWarmTimeout = 10 * time.Second

// connectorCount is how many connectors the server last listed.
var connectorCount atomic.Int64

// WarmFolder waits, when the server is running, until the connectors of the
// folder dir have connected. 2.x connects them per folder, when a folder is
// first used. A chat's first turn would otherwise start before they connect,
// and when they do, 2.x tells the model mid-turn in a note that small models
// answer instead of the user. Once a folder is warm this is one quick request.
//
// A new folder's list is empty at first, so an empty list means "not yet"
// unless there are no connectors at all.
func WarmFolder(ctx context.Context, dir string) {
	base, password, ok := Running()
	if !ok || dir == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, folderWarmTimeout)
	defer cancel()
	grace := time.Duration(0)
	if connectorCount.Load() > 0 {
		grace = folderWarmTimeout
	}
	start := time.Now()
	path := "/api/mcp?" + neturl.Values{"location[directory]": {dir}}.Encode()
	n, pending := warm(ctx, base, password, path, grace)
	if n > 0 {
		connectorCount.Store(int64(n))
	}
	if took := time.Since(start); took > time.Second || pending > 0 {
		log.Printf("[opencode-v2] connectors for %s: %d ready, %d still connecting, after %s",
			dir, n-pending, pending, took.Round(100*time.Millisecond))
	}
}

var scratch struct {
	sync.Mutex
	dir string
}

// ScratchDir returns an empty folder for runs that need no folder of their own
// (titles, day notes). 2.x sets up each folder it runs in, connectors included,
// and keeps that for an hour after its last use, so a new folder per title
// would keep a set per title. One folder for the life of KWA keeps one.
func ScratchDir() (string, error) {
	scratch.Lock()
	defer scratch.Unlock()
	if scratch.dir != "" {
		if info, err := os.Stat(scratch.dir); err == nil && info.IsDir() {
			return scratch.dir, nil
		}
	}
	dir, err := os.MkdirTemp("", "kwa-opencode-")
	if err != nil {
		return "", err
	}
	scratch.dir = dir
	return dir, nil
}

func removeScratch() {
	scratch.Lock()
	defer scratch.Unlock()
	if scratch.dir != "" {
		_ = os.RemoveAll(scratch.dir)
		scratch.dir = ""
	}
}

func workDir() string {
	if info, err := os.Stat(config.Workspace); err == nil && info.IsDir() {
		return config.Workspace
	}
	return os.TempDir()
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func randomPassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// processEnv is the environment of every opencode 2.x process: KWA's, with
// the tokens the user saved for skills.
func processEnv() []string { return skilltokens.Add(childEnv(os.Environ())) }

// childEnv is KWA's environment for an opencode 2.x process, less what must
// not pass through: an opencode password, which the caller sets, and PWD.
// 2.x takes its folder from PWD before the real working directory, and Go only
// rewrites PWD for a command without its own environment, so KWA's own PWD
// would put every chat in the folder KWA was started from.
func childEnv(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "OPENCODE_PASSWORD=") || strings.HasPrefix(kv, "OPENCODE_SERVER_PASSWORD=") ||
			strings.HasPrefix(kv, "PWD=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = append(t.b[:0:0], t.b[len(t.b)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
