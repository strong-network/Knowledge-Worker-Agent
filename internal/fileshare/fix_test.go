// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package fileshare

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

const (
	cdnHTML   = `<script src="https://cdn.jsdelivr.net/npm/chart.js"></script><img src="logo.png"><img src=".system/secret.png"><img src="../outside.png"><p>chart</p>`
	fixedHTML = `<script>/* chart drawn inline */</script><svg></svg><p>chart</p>`
)

// fixRig stands in for the fix's turn: agent does its work in the fix chat's
// folder when the test plays the turn, and returns the turn's error, if any.
type fixRig struct {
	*rig
	agent  func(dir string) string
	prompt string
	dir    string
	turns  chan func()
}

func setupFix(t *testing.T) *fixRig {
	t.Helper()
	r := &fixRig{rig: setup(t), turns: make(chan func(), 4)}
	prevWS, prevSend := config.Workspace, sendFix
	t.Cleanup(func() { config.Workspace, sendFix = prevWS, prevSend })
	config.Workspace = t.TempDir()
	sendFix = func(sid, prompt string) (func(context.Context) string, error) {
		cfg, err := db.GetSessionConfig(sid)
		if err != nil {
			return nil, err
		}
		r.prompt, r.dir = prompt, cfg.Workdir
		done := make(chan string, 1)
		r.turns <- func() { done <- r.agent(cfg.Workdir) }
		return func(ctx context.Context) string {
			select {
			case e := <-done:
				return e
			case <-ctx.Done():
				return ""
			}
		}, nil
	}
	write(t, filepath.Join(r.folder, "dash.html"), cdnHTML)
	write(t, filepath.Join(r.folder, "logo.png"), "PNG")
	write(t, filepath.Join(r.folder, ".system", "secret.png"), "secret")
	write(t, filepath.Join(filepath.Dir(r.folder), "outside.png"), "outside")
	return r
}

func (r *fixRig) start(t *testing.T, name string) Fix {
	t.Helper()
	rec := call(t, HandleStartFix, "POST", "/api/artifacts/fixes", "", fileRequest{SessionID: r.chat, Path: filepath.Join(r.folder, name)}, true)
	var f Fix
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil || rec.Code != 200 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	return f
}

// play runs the turn and waits for the fix to settle.
func (r *fixRig) play(t *testing.T, id string) Fix {
	t.Helper()
	(<-r.turns)()
	for i := 0; i < 300; i++ {
		rec := call(t, HandleFix, "GET", "/api/artifacts/fixes/"+id, id, nil, true)
		var f Fix
		_ = json.Unmarshal(rec.Body.Bytes(), &f)
		if f.State != "running" {
			return f
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the fix never settled")
	return Fix{}
}

func read(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func backups(folder string) []string {
	m, _ := filepath.Glob(filepath.Join(folder, ".system", "artifact-backups", "*"))
	return m
}

func TestAFixReplacesTheFileWhenItPasses(t *testing.T) {
	r := setupFix(t)
	r.agent = func(dir string) string {
		os.WriteFile(filepath.Join(dir, "dash.html"), []byte(fixedHTML), 0o644)
		return ""
	}
	f := r.start(t, "dash.html")
	cfg, _ := db.GetSessionConfig(f.ChatID)
	if f.State != "running" || cfg.Label != "Make dash.html self-contained" || !cfg.Yolo || !cfg.Temporary {
		t.Fatalf("fix %+v, chat %+v", f, cfg)
	}
	if !strings.HasPrefix(cfg.Workdir, filepath.Join(config.Workspace, "Chats", "chat-")) {
		t.Fatalf("the fix must work in a chat folder of its own: %s", cfg.Workdir)
	}
	if read(filepath.Join(r.dir, "dash.html")) != cdnHTML || read(filepath.Join(r.dir, "logo.png")) != "PNG" {
		t.Error("the fix folder must hold a copy of the file and of the file it uses")
	}
	if _, err := os.Stat(filepath.Join(r.dir, ".system", "secret.png")); err == nil {
		t.Error("a hidden file was copied")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(r.dir), "outside.png")); err == nil {
		t.Error("a file outside the chat's folder was copied")
	}
	for _, want := range []string{"Make the file dash.html in this folder self-contained", "- Loads a script from cdn.jsdelivr.net (line 1)",
		"- Shows the picture logo.png from another file (line 1)", "copied beside it: logo.png", "drawing a chart as inline SVG"} {
		if !strings.Contains(r.prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, r.prompt)
		}
	}
	if again := r.start(t, "dash.html"); again.ID != f.ID || len(r.turns) != 1 {
		t.Error("a second start must follow the running fix, not start another")
	}
	done := r.play(t, f.ID)
	if done.State != "done" || read(filepath.Join(r.folder, "dash.html")) != fixedHTML {
		t.Fatalf("done: %+v; file %q", done, read(filepath.Join(r.folder, "dash.html")))
	}
	if b := backups(r.folder); len(b) != 1 || !strings.HasPrefix(filepath.Base(b[0]), "dash.html.") || read(b[0]) != cdnHTML {
		t.Errorf("backup: %v", b)
	}
	if read(filepath.Join(r.dir, "dash.html")) != fixedHTML {
		t.Error("the fixed file stays in the fix chat's folder too")
	}
}

func TestAFixThatStillFailsChangesNothing(t *testing.T) {
	r := setupFix(t)
	r.agent = func(dir string) string {
		os.WriteFile(filepath.Join(dir, "dash.html"), []byte(`<script src="https://cdn.example/x.js"></script>`), 0o644)
		return ""
	}
	done := r.play(t, r.start(t, "dash.html").ID)
	if done.State != "failed" || done.Problem != "It still isn't self-contained." || len(done.Findings) != 1 || done.Findings[0].What != "Loads a script from cdn.example" {
		t.Fatalf("%+v", done)
	}
	if read(filepath.Join(r.folder, "dash.html")) != cdnHTML || len(backups(r.folder)) != 0 {
		t.Error("the original must be untouched")
	}
}

func TestAFixDoesntReplaceAFileThatChangedMeanwhile(t *testing.T) {
	r := setupFix(t)
	edited := cdnHTML + "<p>edited meanwhile</p>"
	r.agent = func(dir string) string {
		os.WriteFile(filepath.Join(dir, "dash.html"), []byte(fixedHTML), 0o644)
		write(t, filepath.Join(r.folder, "dash.html"), edited)
		return ""
	}
	done := r.play(t, r.start(t, "dash.html").ID)
	if done.State != "failed" || !strings.HasPrefix(done.Problem, "The file changed while it was being fixed") {
		t.Fatalf("%+v", done)
	}
	if read(filepath.Join(r.folder, "dash.html")) != edited || len(backups(r.folder)) != 0 {
		t.Error("the edit made meanwhile must survive")
	}
	if read(filepath.Join(r.dir, "dash.html")) != fixedHTML {
		t.Error("the fixed file stays in the fix chat's folder")
	}
}

func TestAFixThatStopsFails(t *testing.T) {
	r := setupFix(t)
	r.agent = func(dir string) string {
		os.Remove(filepath.Join(dir, "dash.html"))
		return "The model isn't available."
	}
	done := r.play(t, r.start(t, "dash.html").ID)
	if done.State != "failed" || done.Problem != "The fix stopped: The model isn't available." || read(filepath.Join(r.folder, "dash.html")) != cdnHTML {
		t.Fatalf("%+v", done)
	}
}

func TestATurnErrorIsTheReasonGiven(t *testing.T) {
	r := setupFix(t)
	r.agent = func(string) string { return "The model isn't available." }
	done := r.play(t, r.start(t, "dash.html").ID)
	if done.State != "failed" || done.Problem != "The fix stopped: The model isn't available." || len(done.Findings) == 0 {
		t.Fatalf("%+v", done)
	}
}

func TestOnlyAFileThatIsntSelfContainedIsFixed(t *testing.T) {
	r := setupFix(t)
	for _, name := range []string{"report.html", ".system/secret.png", "../outside.png"} {
		rec := call(t, HandleStartFix, "POST", "/api/artifacts/fixes", "", fileRequest{SessionID: r.chat, Path: filepath.Join(r.folder, name)}, true)
		if rec.Code == 200 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if len(db.RunningFixes()) != 0 || len(r.turns) != 0 {
		t.Error("a fix was started")
	}
}

func TestAFixLeftRunningFailsAtStart(t *testing.T) {
	r := setupFix(t)
	f := r.start(t, "dash.html")
	Start()
	got, _ := db.GetArtifactFix(f.ID)
	if got.State != "failed" || got.Problem != "The fix stopped when Knowledge Worker Agent restarted." {
		t.Errorf("%+v", got)
	}
}

func TestASharedFileBeingFixedShowsFixing(t *testing.T) {
	r := setupFix(t)
	r.share(t, "report.html", db.Audience{})
	write(t, filepath.Join(r.folder, "report.html"), cdnHTML)
	f := r.start(t, "report.html")
	rec := call(t, HandleList, "GET", "/api/artifacts", "", nil, true)
	var list listResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Artifacts) != 1 || list.Artifacts[0].Status != "fixing" || len(list.Fixes) != 1 || list.Fixes[0].ID != f.ID {
		t.Fatalf("list: %s", rec.Body)
	}
}
