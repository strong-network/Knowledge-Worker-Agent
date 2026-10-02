// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// fakeOpencodeRecordingArgs installs a fake opencode that appends each
// invocation's arguments to argsPath, separated by a marker line, and answers
// with one line of text. Appending matters: other opencode calls in a turn
// (session cleanup, titling) would otherwise overwrite the one under test.
func fakeOpencodeRecordingArgs(t *testing.T) (argsPath string) {
	t.Helper()
	tmp := t.TempDir()
	if err := db.Init(filepath.Join(tmp, "test.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	argsPath = filepath.Join(tmp, "args.txt")
	bin := filepath.Join(tmp, "opencode")
	script := "#!/bin/sh\n{ printf '%s\\n' \"$@\"; echo '<<END>>'; } >> " + argsPath + "\nprintf '%s\\n' '{\"type\":\"text\",\"timestamp\":1,\"sessionID\":\"oc-fake\",\"part\":{\"type\":\"text\",\"text\":\"ok\",\"time\":{\"end\":1}}}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KWA_OPENCODE_AUTO_UPDATE", "false")
	t.Setenv("KWA_OPENCODE_INSTALL_DIR", filepath.Join(tmp, "install"))
	prevEnsured, prevBin, prevWS, prevOwner := opencodeEnsured, config.OpencodeBin, config.Workspace, config.OwnerFullName
	opencodeEnsured = false
	config.OpencodeBin, config.Workspace, config.OwnerFullName = bin, tmp, "Alex Owner"
	t.Cleanup(func() {
		opencodeEnsured, config.OpencodeBin, config.Workspace, config.OwnerFullName = prevEnsured, prevBin, prevWS, prevOwner
	})
	return argsPath
}

func waitDone(t *testing.T, s *Stream) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, done := s.Snapshot(); done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("turn did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// invocationWith returns the recorded chat-turn invocation whose arguments
// contain prompt, or "". The planning block marks a chat turn: the titler also
// runs opencode with the user's prompt, but without the chat preamble.
func invocationWith(t *testing.T, argsPath, prompt string) string {
	t.Helper()
	raw, _ := os.ReadFile(argsPath)
	for _, inv := range strings.Split(string(raw), "<<END>>\n") {
		if strings.Contains(inv, prompt) && strings.Contains(inv, "[System — planning]") {
			return inv
		}
	}
	return ""
}

// D25 + D18 end to end: a guest's prompt reaches the model attributed to the
// guest, on both the direct and the queued path, and its message row says so.
func TestGuestPromptIsAttributedToTheGuestOnEveryPath(t *testing.T) {
	argsPath := fakeOpencodeRecordingArgs(t)
	sid := "guest-attr"
	t.Cleanup(func() { Streams.Delete(sid); ClearQueue(sid) })
	cfg := config.SessionConfig{Backend: config.BackendOpencode, Workdir: config.Workspace, Model: "anthropic/claude-sonnet-4-5", Yolo: true}
	if err := db.CreateSession(sid, cfg); err != nil {
		t.Fatal(err)
	}
	sarah := Author{ID: "g-1", Name: "Sarah"}

	assertAttributedToSarah := func(path, prompt string) {
		t.Helper()
		args := invocationWith(t, argsPath, prompt)
		if args == "" {
			t.Fatalf("%s: no opencode run carried the prompt %q", path, prompt)
		}
		if !strings.Contains(args, "written by Sarah") {
			t.Errorf("%s: the model was not told Sarah wrote this", path)
		}
		if strings.Contains(args, "current user: Name: Alex Owner") {
			t.Errorf("%s: the guest's prompt still carries the owner's identity line", path)
		}
	}

	s, queued := SendOrEnqueue(sid, "direct from sarah", cfg, Turn{Author: sarah})
	if queued != nil || s == nil {
		t.Fatal("expected the turn to start")
	}
	if s.Prompt != "direct from sarah" || s.Author != sarah {
		t.Errorf("stream carries prompt=%q author=%+v", s.Prompt, s.Author)
	}
	waitDone(t, s)
	assertAttributedToSarah("direct", "direct from sarah")

	item := EnqueuePrompt(sid, "queued from sarah", Turn{Author: sarah})
	if item.AuthorID != "g-1" || item.AuthorName != "Sarah" {
		t.Fatalf("queued prompt lost its author: %+v", item)
	}
	next := StartNextQueued(sid)
	if next == nil {
		t.Fatal("queued prompt did not start")
	}
	if next.Author != sarah {
		t.Errorf("queued turn's stream has author %+v", next.Author)
	}
	waitDone(t, next)
	assertAttributedToSarah("queued", "queued from sarah")

	var users []db.ChatMessage
	for _, m := range db.GetMessages(sid) {
		if m.Role == "user" {
			users = append(users, m)
		}
	}
	if len(users) != 2 {
		t.Fatalf("got %d user messages", len(users))
	}
	for _, m := range users {
		if m.AuthorID != "g-1" || m.AuthorName != "Sarah" {
			t.Errorf("message %q stored author %q/%q", m.Content, m.AuthorID, m.AuthorName)
		}
	}
}

// The opencode server backend builds its prompt separately from the CLI path;
// it must attribute a guest's prompt the same way.
func TestServerBackendAttributesTheGuestToo(t *testing.T) {
	prev := config.OwnerFullName
	config.OwnerFullName = "Alex Owner"
	t.Cleanup(func() { config.OwnerFullName = prev })

	guest := buildServerTurnRequest("srv-g", "hello", config.SessionConfig{}, nil, Author{ID: "g-1", Name: "Sarah"})
	if !strings.Contains(guest.Prompt, "written by Sarah") || strings.Contains(guest.Prompt, "current user:") {
		t.Errorf("server backend did not attribute the guest: %q", guest.Prompt)
	}
	owner := buildServerTurnRequest("srv-o", "hello", config.SessionConfig{}, nil, Author{})
	if !strings.Contains(owner.Prompt, "current user: Name: Alex Owner") {
		t.Errorf("server backend lost the owner line: %q", owner.Prompt)
	}
}

// The owner's own prompts are untouched: owner identity line, no author.
func TestOwnerPromptKeepsTheOwnerIdentity(t *testing.T) {
	argsPath := fakeOpencodeRecordingArgs(t)
	sid := "owner-attr"
	t.Cleanup(func() { Streams.Delete(sid) })
	cfg := config.SessionConfig{Backend: config.BackendOpencode, Workdir: config.Workspace, Model: "anthropic/claude-sonnet-4-5", Yolo: true}
	if err := db.CreateSession(sid, cfg); err != nil {
		t.Fatal(err)
	}
	s, _ := SendOrEnqueue(sid, "from the owner", cfg)
	waitDone(t, s)
	args := invocationWith(t, argsPath, "from the owner")
	if args == "" {
		t.Fatal("no opencode run carried the owner's prompt")
	}
	if !strings.Contains(args, "current user: Name: Alex Owner") {
		t.Error("the owner's prompt lost the owner identity line")
	}
	if strings.Contains(args, "written by") {
		t.Error("the owner's prompt was attributed to a guest")
	}
	if m := db.GetMessages(sid)[0]; m.AuthorID != "" {
		t.Errorf("owner message stored author %q", m.AuthorID)
	}
}

// A turn's id rides on its first and last frame, so any path that carries the
// turn to a browser also says which turn it is.
func TestTurnIDIsOnTheFirstAndLastFrame(t *testing.T) {
	fakeOpencodeRecordingArgs(t)
	sid := "turn-id"
	t.Cleanup(func() { Streams.Delete(sid) })
	cfg := config.SessionConfig{Backend: config.BackendOpencode, Workdir: config.Workspace, Model: "anthropic/claude-sonnet-4-5", Yolo: true}
	if err := db.CreateSession(sid, cfg); err != nil {
		t.Fatal(err)
	}
	s, _ := SendOrEnqueue(sid, "hello", cfg)
	waitDone(t, s)
	if s.TurnID == "" {
		t.Fatal("turn has no id")
	}
	log, _ := s.Snapshot()
	first, last := log[0], log[len(log)-1]
	if first["type"] != "session_id" || first["turn_id"] != s.TurnID {
		t.Errorf("first frame %v does not carry turn %s", first, s.TurnID)
	}
	if last["type"] != "done" || last["turn_id"] != s.TurnID {
		t.Errorf("last frame %v does not carry turn %s", last, s.TurnID)
	}
	if other := OpenTurn(sid); other.TurnID == s.TurnID || other.TurnID == "" {
		t.Errorf("turn ids are not unique: %q then %q", s.TurnID, other.TurnID)
	}
}

// Both rows a turn writes carry the id its frames carry, on the direct path and
// the queued one, so a viewer can tell a live turn it already shows.
func TestTurnIDIsStoredOnTheRowsTheTurnWrites(t *testing.T) {
	fakeOpencodeRecordingArgs(t)
	sid := "turn-rows"
	t.Cleanup(func() { Streams.Delete(sid); ClearQueue(sid) })
	cfg := config.SessionConfig{Backend: config.BackendOpencode, Workdir: config.Workspace, Model: "anthropic/claude-sonnet-4-5", Yolo: true}
	if err := db.CreateSession(sid, cfg); err != nil {
		t.Fatal(err)
	}
	direct, _ := SendOrEnqueue(sid, "one", cfg)
	waitDone(t, direct)

	hold := OpenTurn(sid) // someone else's turn is running, so the next prompt queues
	if _, q := SendOrEnqueue(sid, "two", cfg, Turn{Author: Author{ID: "g-1", Name: "Sarah"}}); q == nil {
		t.Fatal("prompt did not queue behind a running turn")
	}
	hold.Finish()
	queued := StartNextQueued(sid)
	if queued == nil {
		t.Fatal("queue did not drain")
	}
	waitDone(t, queued)

	msgs := db.GetMessages(sid)
	if len(msgs) != 4 {
		t.Fatalf("got %d messages: %+v", len(msgs), msgs)
	}
	want := []string{direct.TurnID, direct.TurnID, queued.TurnID, queued.TurnID}
	for i, m := range msgs {
		if m.TurnID == "" || m.TurnID != want[i] {
			t.Errorf("message %d (%s %q) has turn %q, want %q", i, m.Role, m.Content, m.TurnID, want[i])
		}
	}
	if direct.TurnID == queued.TurnID {
		t.Error("two turns share an id")
	}
	if msgs[2].AuthorName != "Sarah" {
		t.Errorf("queued prompt lost its author: %+v", msgs[2])
	}
}

// A turn no send path stored a prompt for (the project summary) announces no
// prompt: viewers must not see instructions the transcript does not contain.
func TestAnUnstoredPromptIsNotAnnounced(t *testing.T) {
	fakeOpencodeRecordingArgs(t)
	sid := "turn-internal"
	t.Cleanup(func() { Streams.Delete(sid) })
	cfg := config.SessionConfig{Backend: config.BackendOpencode, Workdir: config.Workspace, Model: "anthropic/claude-sonnet-4-5", Yolo: true}
	if err := db.CreateSession(sid, cfg); err != nil {
		t.Fatal(err)
	}
	internal := StartChatStream(sid, "internal instructions", cfg)
	waitDone(t, internal)
	if internal.Prompt != "" || internal.TurnID == "" {
		t.Errorf("internal turn: prompt %q, id %q", internal.Prompt, internal.TurnID)
	}
	sent, _ := SendOrEnqueue(sid, "hello", cfg)
	waitDone(t, sent)
	if sent.Prompt != "hello" {
		t.Errorf("a stored prompt is not announced: %q", sent.Prompt)
	}
}
