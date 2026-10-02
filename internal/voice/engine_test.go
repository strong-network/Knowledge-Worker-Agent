// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package voice

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type fakeProc struct {
	engine   *Engine
	logPath  string
	modeFile string
	tmp      string
}

// newFakeEngine runs the test binary as the engine, with TMPDIR pointed at a
// test directory so the engine's own directories can be inspected.
func newFakeEngine(t *testing.T, mode string) *fakeProc {
	t.Helper()
	base := t.TempDir()
	tmp := filepath.Join(base, "tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmp)
	model := filepath.Join(base, "model.bin")
	os.WriteFile(model, []byte("fake"), 0o644)
	f := &fakeProc{logPath: filepath.Join(base, "engine.log"), modeFile: filepath.Join(base, "mode"), tmp: tmp}
	f.setMode(t, mode)
	bin, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	e := newEngine(bin, model)
	e.threads = 3
	e.startTimeout = 10 * time.Second
	e.extraEnv = []string{"VOICE_TEST_ROLE=engine", "VOICE_TEST_LOG=" + f.logPath, "VOICE_TEST_MODEFILE=" + f.modeFile}
	f.engine = e
	t.Cleanup(e.Close)
	return f
}

func (f *fakeProc) setMode(t *testing.T, mode string) {
	t.Helper()
	if err := os.WriteFile(f.modeFile, []byte(mode), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeProc) records(t *testing.T, event string) []map[string]any {
	t.Helper()
	data, _ := os.ReadFile(f.logPath)
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["event"] == event {
			out = append(out, m)
		}
	}
	return out
}

func (f *fakeProc) engineDirs(t *testing.T) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(f.tmp, engineDirPrefix+"*"))
	return m
}

// dead reports whether pid is gone or a zombie waiting to be reaped.
func dead(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	return i < 0 || i+2 >= len(s) || s[i+2] == 'Z' || s[i+2] == 'X'
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestEngineStartsPrivatelyAndPassesTheRequest(t *testing.T) {
	t.Setenv("VOICE_TEST_SECRET", "hunter2")
	f := newFakeEngine(t, "ok")
	text, err := f.engine.Transcribe(context.Background(), request{
		pcm: make([]int16, sampleRate), prompt: "SecurSpaces, NetScaler.", audioCtx: 114, noRetry: true})
	if err != nil || text != " heard 16000 samples" {
		t.Fatalf("Transcribe = %q, %v", text, err)
	}
	starts := f.records(t, "start")
	if len(starts) != 1 {
		t.Fatalf("starts = %d, want 1", len(starts))
	}
	s := starts[0]
	args := strings.Join(toStrings(s["args"]), " ")
	if !strings.HasPrefix(args, "-m "+f.engine.model+" -l en -nt -t 3 --host 127.0.0.1 --port ") {
		t.Fatalf("args = %q", args)
	}
	cwd := s["cwd"].(string)
	if filepath.Dir(cwd) != f.tmp || !strings.HasPrefix(filepath.Base(cwd), engineDirPrefix) || s["cwd_entries"].(float64) != 0 {
		t.Fatalf("cwd %q (%v entries) is not a fresh engine directory in %s", cwd, s["cwd_entries"], f.tmp)
	}
	if fi, err := os.Stat(cwd); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("engine directory mode = %v, %v; want 0700", fi.Mode().Perm(), err)
	}
	env := strings.Join(toStrings(s["env"]), "\n")
	if strings.Contains(env, "hunter2") || !strings.Contains(env, "HOME="+cwd) || !strings.Contains(env, "PATH=/usr/bin:/bin") {
		t.Fatalf("engine environment leaks or lacks basics:\n%s", env)
	}

	reqs := f.records(t, "request")
	r := reqs[0]
	if r["riff"] != "RIFF" || r["bytes"].(float64) != 44+2*sampleRate || r["prompt"] != "SecurSpaces, NetScaler." ||
		r["audio_ctx"] != "114" || r["temperature_inc"] != "0" || r["response_format"] != "json" || r["cwd_entries"].(float64) != 0 {
		t.Fatalf("request = %v", r)
	}

	// The accurate pass sends none of the quick pass's fields, to the same engine.
	if _, err := f.engine.Transcribe(context.Background(), request{pcm: make([]int16, 160)}); err != nil {
		t.Fatal(err)
	}
	r2 := f.records(t, "request")[1]
	if r2["prompt"] != "" || r2["audio_ctx"] != "" || r2["temperature_inc"] != "" || r2["pid"] != r["pid"] {
		t.Fatalf("second request = %v (first pid %v)", r2, r["pid"])
	}
}

func TestEngineStuckRequestIsKilledAndReplaced(t *testing.T) {
	f := newFakeEngine(t, "hang")
	f.engine.requestTimeout = 300 * time.Millisecond
	_, err := f.engine.Transcribe(context.Background(), request{pcm: make([]int16, 160)})
	if err == nil || !strings.Contains(err.Error(), "took over") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	pid := int(f.records(t, "start")[0]["pid"].(float64))
	waitFor(t, "the stuck engine to die", func() bool { return dead(pid) })
	if f.engine.running() != 0 || len(f.engineDirs(t)) != 0 {
		t.Fatalf("stuck engine left behind: pid %d, dirs %v", f.engine.running(), f.engineDirs(t))
	}
	f.setMode(t, "ok")
	if _, err := f.engine.Transcribe(context.Background(), request{pcm: make([]int16, 160)}); err != nil {
		t.Fatalf("no fresh engine after the stuck one: %v", err)
	}
	if starts := f.records(t, "start"); len(starts) != 2 || starts[1]["pid"] == starts[0]["pid"] {
		t.Fatalf("starts = %v", starts)
	}
}

// A caller that gives up (a discarded dictation) must not cost the engine.
func TestEngineSurvivesACancelledRequest(t *testing.T) {
	f := newFakeEngine(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := f.engine.Transcribe(ctx, request{pcm: make([]int16, 160)}); err == nil {
		t.Fatal("want an error")
	}
	if f.engine.running() == 0 {
		t.Fatal("a cancelled request stopped the engine")
	}
}

func TestEngineRestartsAfterCrash(t *testing.T) {
	f := newFakeEngine(t, "crash")
	if _, err := f.engine.Transcribe(context.Background(), request{pcm: make([]int16, 160)}); err == nil {
		t.Fatal("want an error from a crashed engine")
	}
	waitFor(t, "the crashed engine to be forgotten", func() bool { return f.engine.running() == 0 })
	f.setMode(t, "ok")
	if text, err := f.engine.Transcribe(context.Background(), request{pcm: make([]int16, 160)}); err != nil || text == "" {
		t.Fatalf("after a crash: %q, %v", text, err)
	}
	if n := len(f.engineDirs(t)); n != 1 {
		t.Fatalf("engine directories = %d, want only the running one", n)
	}
}

func TestEngineStopsWhenIdle(t *testing.T) {
	f := newFakeEngine(t, "ok")
	f.engine.idleAfter = 200 * time.Millisecond
	if _, err := f.engine.Transcribe(context.Background(), request{pcm: make([]int16, 160)}); err != nil {
		t.Fatal(err)
	}
	pid := f.engine.running()
	if pid == 0 {
		t.Fatal("engine not running after a request")
	}
	waitFor(t, "the idle engine to stop", func() bool { return f.engine.running() == 0 && dead(pid) })
	if len(f.engineDirs(t)) != 0 {
		t.Fatalf("idle stop left %v", f.engineDirs(t))
	}
}

func TestEngineThatFailsToStart(t *testing.T) {
	f := newFakeEngine(t, "fail-start")
	_, err := f.engine.Transcribe(context.Background(), request{pcm: make([]int16, 160)})
	if err == nil || !strings.Contains(err.Error(), "exited during startup") {
		t.Fatalf("err = %v", err)
	}
	if len(f.records(t, "start")) != 2 || len(f.engineDirs(t)) != 0 {
		t.Fatalf("want two attempts and no directories left: %d, %v", len(f.records(t, "start")), f.engineDirs(t))
	}
}

// Killing Knowledge Worker Agent, even with SIGKILL, must not leave a 2 GB engine running.
func TestEngineDiesWithTheServer(t *testing.T) {
	f := newFakeEngine(t, "ok")
	parent := exec.Command(os.Args[0])
	parent.Env = append(os.Environ(), "VOICE_TEST_ROLE=parent", "VOICE_TEST_MODEL="+f.engine.model)
	out, err := parent.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	pid, _ := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || pid <= 0 || dead(pid) {
		parent.Process.Kill()
		t.Fatalf("parent did not start an engine: %q, %v", line, err)
	}
	if err := parent.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	parent.Wait()
	waitFor(t, "the engine to die with its parent", func() bool { return dead(pid) })
}

func TestRemoveStaleDirs(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	stale := filepath.Join(tmp, engineDirPrefix+"old")
	os.MkdirAll(filepath.Join(stale, "sub"), 0o700)
	os.WriteFile(filepath.Join(stale, "libggml-cpu-evil.so"), []byte("x"), 0o600)
	beforeRename := filepath.Join(tmp, "sds-chat-voice-old")
	os.Mkdir(beforeRename, 0o700)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "keep"), []byte("x"), 0o600)
	link := filepath.Join(tmp, engineDirPrefix+"link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(tmp, "not-ours")
	os.Mkdir(other, 0o700)

	removeStaleDirs()

	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Fatal("stale engine directory kept")
	}
	if _, err := os.Lstat(beforeRename); !os.IsNotExist(err) {
		t.Fatal("stale engine directory from before the rename kept")
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatal("followed a symlink and removed its target")
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatal("removed a symlink it should have ignored")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("removed a directory that isn't the engine's")
	}
}

func TestCheckEngine(t *testing.T) {
	f := newFakeEngine(t, "ok")
	bin, model := f.engine.bin, f.engine.model
	env := []string{"VOICE_TEST_ROLE=engine", "VOICE_TEST_MODEFILE=" + f.modeFile}
	if err := checkEngine(bin, model, env); err != nil {
		t.Fatalf("a working engine: %v", err)
	}
	for name, err := range map[string]error{
		"relative engine":  checkEngine("whisper-server", model, env),
		"relative model":   checkEngine(bin, "model.bin", env),
		"missing model":    checkEngine(bin, model+".missing", env),
		"missing engine":   checkEngine(bin+".missing", model, env),
		"model not a file": checkEngine(bin, filepath.Dir(model), env),
	} {
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	f.setMode(t, "no-help")
	if err := checkEngine(bin, model, env); err == nil || !strings.Contains(err.Error(), "cannot load backend") {
		t.Fatalf("an engine that doesn't run: %v", err)
	}
	if len(f.engineDirs(t)) != 0 {
		t.Fatalf("check left %v", f.engineDirs(t))
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
