// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The engine's working directory, a fresh private one per start. It must stay
// empty: whisper.cpp loads CPU code from its working directory.
const engineDirPrefix = "kwa-voice-"

// request is one transcription. A zero audioCtx means the full 30-second
// window, the accurate pass; noRetry turns off the engine's retry loop.
type request struct {
	pcm      []int16
	prompt   string
	audioCtx int
	noRetry  bool
}

// transcriber is what a dictation needs from the engine.
type transcriber interface {
	Transcribe(ctx context.Context, r request) (string, error)
	Warm()
}

// Engine runs whisper-server on loopback while dictation is in use.
type Engine struct {
	bin, model string
	threads    int

	requestTimeout time.Duration
	idleAfter      time.Duration
	startTimeout   time.Duration
	extraEnv       []string // tests only

	mu   sync.Mutex
	proc *engineProc
	busy int
	idle *time.Timer
}

type engineProc struct {
	cmd    *exec.Cmd
	port   int
	dir    string
	done   chan struct{} // closed once the process has exited
	client *http.Client
}

func newEngine(bin, model string) *Engine {
	return &Engine{
		bin:            bin,
		model:          model,
		threads:        runtime.GOMAXPROCS(0),
		requestTimeout: 20 * time.Second,
		idleAfter:      5 * time.Minute,
		startTimeout:   30 * time.Second,
	}
}

// Warm starts the engine ahead of the first request, so a dictation's first
// words don't wait for the model to load.
func (e *Engine) Warm() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.ensureLocked(); err != nil {
		log.Printf("[voice] engine failed to start: %v", err)
		return
	}
	e.armIdleLocked()
}

// Transcribe sends one request, starting the engine if needed. A request over
// the timeout ends the engine: a stuck engine ignores SIGTERM, and the next
// request starts a fresh one.
func (e *Engine) Transcribe(ctx context.Context, r request) (string, error) {
	e.mu.Lock()
	p, err := e.ensureLocked()
	if err != nil {
		e.mu.Unlock()
		return "", err
	}
	e.busy++
	if e.idle != nil {
		e.idle.Stop()
	}
	e.mu.Unlock()

	tctx, cancel := context.WithTimeout(ctx, e.requestTimeout)
	text, err := p.infer(tctx, r)
	timedOut := err != nil && errors.Is(tctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	cancel()

	e.mu.Lock()
	defer e.mu.Unlock()
	e.busy--
	switch {
	case timedOut:
		log.Printf("[voice] engine request took over %s; stopping the engine", e.requestTimeout)
		e.stopLocked(p)
		err = fmt.Errorf("engine request took over %s", e.requestTimeout)
	case p.exited():
		log.Print("[voice] engine exited during a request")
		e.stopLocked(p)
	}
	e.armIdleLocked()
	return text, err
}

// Close stops the engine, at shutdown.
func (e *Engine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.idle != nil {
		e.idle.Stop()
	}
	if e.proc != nil {
		e.stopLocked(e.proc)
	}
}

// running reports the engine's pid, or 0.
func (e *Engine) running() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.proc == nil || e.proc.exited() {
		return 0
	}
	return e.proc.cmd.Process.Pid
}

func (e *Engine) armIdleLocked() {
	if e.busy > 0 || e.proc == nil {
		return
	}
	p := e.proc
	if e.idle != nil {
		e.idle.Stop()
	}
	e.idle = time.AfterFunc(e.idleAfter, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.busy == 0 && e.proc == p {
			log.Printf("[voice] engine idle for %s; stopping it", e.idleAfter)
			e.stopLocked(p)
		}
	})
}

func (e *Engine) ensureLocked() (*engineProc, error) {
	if e.proc != nil {
		if !e.proc.exited() {
			return e.proc, nil
		}
		log.Print("[voice] engine had exited; starting a new one")
		e.stopLocked(e.proc)
	}
	var err error
	// A second try covers the loopback port being taken between picking it and
	// the engine binding it.
	for attempt := 0; attempt < 2; attempt++ {
		var p *engineProc
		if p, err = e.start(); err == nil {
			e.proc = p
			return p, nil
		}
	}
	return nil, err
}

func (e *Engine) start() (*engineProc, error) {
	dir, err := os.MkdirTemp("", engineDirPrefix)
	if err != nil {
		return nil, fmt.Errorf("engine directory: %w", err)
	}
	port, err := freePort()
	if err != nil {
		os.Remove(dir)
		return nil, err
	}
	cmd := exec.Command(e.bin, "-m", e.model, "-l", "en", "-nt", "-t", strconv.Itoa(e.threads),
		"--host", "127.0.0.1", "--port", strconv.Itoa(port))
	cmd.Dir = dir
	// Nothing of Knowledge Worker Agent's environment, which holds credentials, reaches it.
	cmd.Env = append([]string{"HOME=" + dir, "LANG=C.UTF-8", "PATH=/usr/bin:/bin"}, e.extraEnv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	p := &engineProc{
		cmd:  cmd,
		port: port,
		dir:  dir,
		done: make(chan struct{}),
		client: &http.Client{Transport: &http.Transport{
			Proxy:             nil,
			DisableKeepAlives: true,
		}},
	}
	began := time.Now()
	if err := p.launch(); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("start engine: %w", err)
	}
	if err := p.waitReady(e.startTimeout); err != nil {
		p.kill()
		return nil, err
	}
	log.Printf("[voice] engine started: pid %d, %d threads, ready in %s",
		cmd.Process.Pid, e.threads, time.Since(began).Round(time.Millisecond))
	return p, nil
}

func (e *Engine) stopLocked(p *engineProc) {
	p.kill()
	if e.proc == p {
		e.proc = nil
	}
}

// launch starts the process from a goroutine locked to its OS thread, which
// stays alive until the process exits. Linux sends Pdeathsig when the thread
// that started the child exits, not the process, and Go may retire threads.
func (p *engineProc) launch() error {
	started := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(p.done)
		if err := p.cmd.Start(); err != nil {
			started <- err
			return
		}
		started <- nil
		_ = p.cmd.Wait()
	}()
	return <-started
}

func (p *engineProc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *engineProc) waitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(p.port))
	for time.Now().Before(deadline) {
		if p.exited() {
			return errors.New("engine exited during startup")
		}
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("engine not ready after %s", timeout)
}

// kill ends the process with SIGKILL and removes its directory.
func (p *engineProc) kill() {
	if !p.exited() && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		log.Printf("[voice] engine pid %d did not exit after SIGKILL", p.cmd.Process.Pid)
	}
	os.RemoveAll(p.dir)
}

func (p *engineProc) infer(ctx context.Context, r request) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(wav(r.pcm)); err != nil {
		return "", err
	}
	fields := map[string]string{"response_format": "json"}
	if r.prompt != "" {
		fields["prompt"] = r.prompt
	}
	if r.audioCtx > 0 {
		fields["audio_ctx"] = strconv.Itoa(r.audioCtx)
	}
	if r.noRetry {
		fields["temperature_inc"] = "0"
	}
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return "", err
		}
	}
	if err := mw.Close(); err != nil {
		return "", err
	}
	url := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(p.port)) + "/inference"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("engine request: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Text  string `json:"text"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", fmt.Errorf("engine response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || out.Error != "" {
		return "", fmt.Errorf("engine answered %d: %s", resp.StatusCode, out.Error)
	}
	return out.Text, nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("pick a loopback port: %w", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// removeStaleDirs removes engine directories an earlier Knowledge Worker Agent left behind.
// Only real directories owned by this user; a symlink is never followed.
func removeStaleDirs() {
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), engineDirPrefix+"*"))
	old, _ := filepath.Glob(filepath.Join(os.TempDir(), "sds-chat-voice-*")) // the prefix before the rename
	matches = append(matches, old...)
	for _, m := range matches {
		fi, err := os.Lstat(m)
		if err != nil || !fi.IsDir() {
			continue
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
			continue
		}
		if err := os.RemoveAll(m); err != nil {
			log.Printf("[voice] remove stale engine directory: %v", err)
		}
	}
}

// checkEngine reports why dictation can't run here, or nil: absolute paths,
// a model file, and an engine that starts (--help, from a private directory).
func checkEngine(bin, model string, extraEnv []string) error {
	if !filepath.IsAbs(bin) || !filepath.IsAbs(model) {
		return errors.New("engine and model paths must be absolute")
	}
	if fi, err := os.Stat(model); err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 {
		return fmt.Errorf("no model at %s", model)
	}
	if fi, err := os.Stat(bin); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("no engine at %s", bin)
	}
	dir, err := os.MkdirTemp("", engineDirPrefix)
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--help")
	cmd.Dir = dir
	cmd.Env = append([]string{"HOME=" + dir, "LANG=C.UTF-8", "PATH=/usr/bin:/bin"}, extraEnv...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("engine does not run: %v: %s", err, strings.TrimSpace(lastLine(out)))
	}
	return nil
}

func lastLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
