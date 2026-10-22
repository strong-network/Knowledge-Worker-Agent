// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package fileshare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/fileshare/check"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/fileshare/viewer"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/workdirs"
)

// The background fix: a turn in a chat and folder of its own rewrites a copy of
// a file so it's self-contained. The result replaces the original only if it
// passes the check and the original didn't change meanwhile. Never the
// original chat's folder: deleting a chat removes its auto-created folder.

const fixTimeout = 30 * time.Minute

// sendFix sends the fix's prompt in its chat and returns a wait for the turn's
// end, which gives the turn's error, if any. Tests stand in for it.
var sendFix = func(sessionID, prompt string) (func(context.Context) string, error) {
	cfg, err := db.GetSessionConfig(sessionID)
	if err != nil {
		return nil, err
	}
	stream, _ := chat.SendOrEnqueue(sessionID, prompt, cfg)
	if stream == nil {
		return nil, errors.New("the chat is busy")
	}
	return func(ctx context.Context) string { return waitTurn(ctx, stream) }, nil
}

func waitTurn(ctx context.Context, s *chat.Stream) string {
	idx, firstErr := 0, ""
	for {
		batch, next, done := s.Wait(ctx, idx)
		idx = next
		for _, ev := range batch {
			if ev["type"] != "error" || firstErr != "" {
				continue
			}
			for _, k := range []string{"message", "error"} {
				if v, ok := ev[k].(string); ok && strings.TrimSpace(v) != "" {
					firstErr = v
					break
				}
			}
		}
		if done || ctx.Err() != nil {
			return firstErr
		}
	}
}

// Fix is a fix as the owner follows it.
type Fix struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Name  string `json:"name"`
	State string `json:"state"`
	// Problem says why a failed fix failed.
	Problem string `json:"problem,omitempty"`
	// Findings are what's left in a result that still isn't self-contained.
	Findings   []check.Finding `json:"findings"`
	ChatID     string          `json:"chat_id"`
	StartedAt  string          `json:"started_at"`
	FinishedAt string          `json:"finished_at,omitempty"`
}

func fixView(f db.ArtifactFix) Fix {
	out := Fix{
		ID: f.ID, Path: filepath.Join(f.Folder, f.RelPath), Name: filepath.Base(f.RelPath), State: f.State,
		Problem: f.Problem, Findings: []check.Finding{}, ChatID: f.FixSessionID, StartedAt: f.StartedAt, FinishedAt: f.FinishedAt,
	}
	if f.Findings != "" {
		_ = json.Unmarshal([]byte(f.Findings), &out.Findings)
	}
	return out
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hiddenPath(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

// fixFolder makes the fix chat's folder where new chats get theirs, so
// deleting the fix chat removes it.
func fixFolder() (string, error) {
	base := workdirs.ChatsRoot()
	if base == "" {
		return "", errors.New("there is no workspace folder to fix it in")
	}
	id := strings.ReplaceAll(chat.NewUUID(), "-", "")[:12]
	dir := filepath.Join(base, workdirs.AutoChatPrefix+id)
	return dir, os.MkdirAll(dir, 0o755)
}

// copyForFix puts the file, and the other files it uses inside its folder,
// into dir at the same paths, and returns those others. Hidden files and
// anything outside the folder are never copied.
func copyForFix(folder, rel string, src []byte, dir string, res check.Result) ([]string, error) {
	dst, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer dst.Close()
	if err := dst.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		return nil, err
	}
	if err := dst.WriteFile(rel, src, 0o644); err != nil {
		return nil, err
	}
	from, err := os.OpenRoot(folder)
	if err != nil {
		return nil, err
	}
	defer from.Close()
	copied := []string{}
	for _, f := range res.Findings {
		if f.File == "" || strings.HasPrefix(f.File, "/") {
			continue
		}
		ref := filepath.Clean(filepath.Join(filepath.Dir(rel), filepath.FromSlash(f.File)))
		if ref == rel || hiddenPath(ref) {
			continue
		}
		info, err := from.Stat(ref)
		if err != nil || !info.Mode().IsRegular() || info.Size() > viewer.MaxSize {
			continue
		}
		data, err := from.ReadFile(ref)
		if err != nil {
			continue
		}
		if dst.MkdirAll(filepath.Dir(ref), 0o755) == nil && dst.WriteFile(ref, data, 0o644) == nil {
			copied = append(copied, filepath.ToSlash(ref))
		}
	}
	return copied, nil
}

// fixPrompt is the fix's fixed text, with what blocks the file filled in.
func fixPrompt(rel string, res check.Result, copied []string) string {
	var b strings.Builder
	name := filepath.ToSlash(rel)
	fmt.Fprintf(&b, "Make the file %s in this folder self-contained, so it can be shared: when it's opened, it must load nothing from the internet or from other files, and its scripts must open no connections. Browsers block those for the people it's shared with.\n\nWhat it loads now:\n", name)
	for _, f := range res.Findings {
		if f.Blocking {
			fmt.Fprintf(&b, "- %s (line %d)\n", f.What, f.Line)
		}
	}
	if len(copied) > 0 {
		fmt.Fprintf(&b, "\nThe other files it uses are copied beside it: %s.\n", strings.Join(copied, ", "))
	}
	if k, _ := viewer.KindOf(rel); k == viewer.KindMarkdown {
		fmt.Fprintf(&b, "\nRewrite %s in place, under the same name. Turn every picture into a data: URL, as PNG, JPEG, GIF or WebP: an SVG picture isn't shown. Keep the text as it is.\n", name)
		return b.String()
	}
	fmt.Fprintf(&b, "\nRewrite %s in place, under the same name:\n", name)
	b.WriteString("- Put every script, style sheet and font inside it, and every picture as a data: URL.\n")
	b.WriteString("- Put the data it needs in a <script type=\"application/json\"> element.\n")
	b.WriteString("- Keep how it looks and what it does.\n")
	b.WriteString("- No network calls (fetch, XMLHttpRequest, WebSocket, EventSource) and no browser storage (localStorage, sessionStorage, indexedDB).\n")
	b.WriteString("- Where a library can't be fetched, rebuild that part without it, for example drawing a chart as inline SVG, rather than leave a load in.\n")
	return b.String()
}

// HandleStartFix serves POST /api/artifacts/fixes: start making a file
// self-contained, or follow the fix already running for it.
func HandleStartFix(w http.ResponseWriter, r *http.Request) {
	var req fileRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	folder, rel, err := resolve(req.SessionID, req.Path)
	if err != nil {
		writeFailure(w, err)
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if f, ok := db.RunningFixAt(folder, rel); ok {
		writeJSON(w, http.StatusOK, fixView(f))
		return
	}
	file, _, err := viewer.Open(folder, rel)
	if err != nil {
		writeFailure(w, &failure{http.StatusConflict, err.Error()})
		return
	}
	src, err := io.ReadAll(file)
	file.Close()
	if err != nil {
		writeFailure(w, err)
		return
	}
	res := check.File(rel, src)
	if !res.Blocking {
		writeFailure(w, &failure{http.StatusConflict, "This file is already self-contained."})
		return
	}
	dir, err := fixFolder()
	if err != nil {
		writeFailure(w, err)
		return
	}
	copied, err := copyForFix(folder, rel, src, dir, res)
	if err != nil {
		os.RemoveAll(dir)
		writeFailure(w, err)
		return
	}
	cfg := config.DefaultSessionConfig()
	cfg.Backend = config.NormalizeBackend(cfg.Backend)
	cfg.Yolo, cfg.Temporary = true, true
	cfg.Label = fmt.Sprintf("Make %s self-contained", filepath.Base(rel))
	cfg.Workdir = dir
	if orig, err := db.GetSessionConfig(req.SessionID); err == nil && orig.Model != "" && config.ModelRunnable(orig.Model) {
		cfg.Model = orig.Model
	}
	f := db.ArtifactFix{ID: newID(), Folder: folder, RelPath: rel, FixSessionID: chat.NewUUID(), SourceSHA256: sum(src)}
	if err := db.CreateSession(f.FixSessionID, cfg); err != nil {
		os.RemoveAll(dir)
		writeFailure(w, err)
		return
	}
	if err := db.CreateArtifactFix(f); err != nil {
		writeFailure(w, err)
		return
	}
	wait, err := sendFix(f.FixSessionID, fixPrompt(rel, res, copied))
	if err != nil {
		_ = db.FinishArtifactFix(f.ID, "failed", "The fix could not start: "+err.Error(), "")
	} else {
		log.Printf("[fileshare] fixing %s in chat %s", rel, f.FixSessionID)
		go finishFix(f, dir, wait)
	}
	stored, _ := db.GetArtifactFix(f.ID)
	writeJSON(w, http.StatusOK, fixView(stored))
}

// HandleFix serves GET /api/artifacts/fixes/{id}: how a fix is going.
func HandleFix(w http.ResponseWriter, r *http.Request) {
	f, ok := db.GetArtifactFix(r.PathValue("id"))
	if !ok {
		writeFailure(w, &failure{http.StatusNotFound, "There's no such fix."})
		return
	}
	writeJSON(w, http.StatusOK, fixView(f))
}

func finishFix(f db.ArtifactFix, dir string, wait func(context.Context) string) {
	ctx, cancel := context.WithTimeout(context.Background(), fixTimeout)
	defer cancel()
	turnErr := wait(ctx)
	state, problem, findings := "failed", "The fix didn't finish within 30 minutes.", []check.Finding{}
	if ctx.Err() == nil {
		state, problem, findings = settleFix(f, dir, turnErr)
	}
	b, _ := json.Marshal(findings)
	if err := db.FinishArtifactFix(f.ID, state, problem, string(b)); err != nil {
		log.Printf("[fileshare] record the fix of %s: %v", f.RelPath, err)
	}
	log.Printf("[fileshare] fix of %s: %s %s", f.RelPath, state, problem)
}

// settleFix checks the fix's result and puts it in place when it passes and
// the original is as the fix started from it, keeping the original as a backup.
func settleFix(f db.ArtifactFix, dir, turnErr string) (state, problem string, findings []check.Finding) {
	none := []check.Finding{}
	out, _, err := viewer.Open(dir, f.RelPath)
	if err != nil {
		if turnErr != "" {
			return "failed", "The fix stopped: " + turnErr, none
		}
		return "failed", "The fix left no file to share: " + err.Error(), none
	}
	data, err := io.ReadAll(out)
	out.Close()
	if err != nil {
		return "failed", "The fixed file couldn't be read.", none
	}
	if res := check.File(f.RelPath, data); res.Blocking {
		if turnErr != "" {
			return "failed", "The fix stopped: " + turnErr, res.Findings
		}
		return "failed", "It still isn't self-contained.", res.Findings
	}
	orig, info, err := viewer.Open(f.Folder, f.RelPath)
	if err != nil {
		return "failed", "The original file is no longer there, so the fixed file stays in the fix chat's folder.", none
	}
	before, err := io.ReadAll(orig)
	orig.Close()
	if err != nil || sum(before) != f.SourceSHA256 {
		return "failed", "The file changed while it was being fixed, so it wasn't replaced. The fixed file is in the fix chat's folder.", none
	}
	if err := replaceFixed(f.Folder, f.RelPath, before, data, info.Mode().Perm()); err != nil {
		return "failed", "The fixed file couldn't be put in place: " + err.Error(), none
	}
	return "done", "", check.File(f.RelPath, data).Findings
}

func replaceFixed(folder, rel string, before, after []byte, perm os.FileMode) error {
	root, err := os.OpenRoot(folder)
	if err != nil {
		return err
	}
	defer root.Close()
	backup := filepath.Join(".system", "artifact-backups", filepath.Base(rel)+"."+time.Now().UTC().Format("20060102T150405Z"))
	if err := root.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
		return err
	}
	if err := root.WriteFile(backup, before, 0o644); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(rel), "."+filepath.Base(rel)+".fixing")
	if err := root.WriteFile(tmp, after, perm); err != nil {
		return err
	}
	return root.Rename(tmp, rel)
}
