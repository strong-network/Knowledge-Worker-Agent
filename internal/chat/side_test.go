// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// A side chat's first turn forks its parent's opencode session, so the agent
// knows the parent's conversation and adds nothing to it. Its next turn
// continues the side chat's own session.
func TestSideChatForksItsParentOnce(t *testing.T) {
	argsPath := fakeOpencodeRecordingArgs(t)
	parent, side := "side-parent", "side-child"
	t.Cleanup(func() { Streams.Delete(side); ClearQueue(side) })
	cfg := config.SessionConfig{Backend: config.BackendOpencode, Workdir: config.Workspace, Model: "anthropic/claude-sonnet-4-5", Yolo: true}
	if err := db.CreateSession(parent, cfg); err != nil {
		t.Fatal(err)
	}
	db.SetOpencodeSession(parent, "ses_parent")
	sideCfg := cfg
	sideCfg.ParentID, sideCfg.Fork, sideCfg.Temporary = parent, true, true
	if err := db.CreateSession(side, sideCfg); err != nil {
		t.Fatal(err)
	}

	s, _ := SendOrEnqueue(side, "first side question", sideCfg, Turn{})
	waitDone(t, s)
	args := invocationWith(t, argsPath, "first side question")
	if !strings.Contains(args, "--session\nses_parent\n") || !strings.Contains(args, "\n--fork\n") {
		t.Fatalf("the first turn should fork the parent's session:\n%s", args)
	}
	if got := db.GetOpencodeSession(side); got != "oc-fake" {
		t.Errorf("side chat's opencode session = %q, want the fork's", got)
	}
	if got := db.GetOpencodeSession(parent); got != "ses_parent" {
		t.Errorf("parent's opencode session = %q, want it unchanged", got)
	}
	stored, _ := db.GetSessionConfig(side)
	if stored.Fork || stored.ParentID != parent {
		t.Errorf("after its first turn the side chat has fork %v parent %q", stored.Fork, stored.ParentID)
	}

	s, _ = SendOrEnqueue(side, "second side question", stored, Turn{})
	waitDone(t, s)
	args = invocationWith(t, argsPath, "second side question")
	if !strings.Contains(args, "--session\noc-fake\n") || strings.Contains(args, "--fork") {
		t.Errorf("the second turn should continue the side chat's own session:\n%s", args)
	}
}

// Which opencode session a turn continues, and whether it forks it.
func TestResumeOrFork(t *testing.T) {
	fakeOpencodeRecordingArgs(t)
	db.CreateSession("p", config.SessionConfig{})
	db.SetOpencodeSession("p", "ses_p")
	db.CreateSession("p-new", config.SessionConfig{})
	db.CreateSession("own", config.SessionConfig{})
	db.SetOpencodeSession("own", "ses_own")

	cases := []struct {
		name, sid string
		cfg       config.SessionConfig
		want      string
		fork      bool
	}{
		{"plain chat", "own", config.SessionConfig{}, "ses_own", false},
		{"side chat, first turn", "s1", config.SessionConfig{ParentID: "p", Fork: true}, "ses_p", true},
		{"side chat with its own session", "own", config.SessionConfig{ParentID: "p", Fork: true}, "ses_own", false},
		{"parent has no conversation yet", "s2", config.SessionConfig{ParentID: "p-new", Fork: true}, "", false},
		{"parent deleted", "s3", config.SessionConfig{ParentID: "gone", Fork: true}, "", false},
		{"fork already done", "s4", config.SessionConfig{ParentID: "p"}, "", false},
	}
	for _, c := range cases {
		got, fork := resumeOrFork(c.sid, c.cfg)
		if got != c.want || fork != c.fork {
			t.Errorf("%s: resumeOrFork = %q %v, want %q %v", c.name, got, fork, c.want, c.fork)
		}
	}

	req := buildServerTurnRequest("s1", "hi", config.SessionConfig{ParentID: "p", Fork: true}, nil, Author{})
	if req.OpencodeSession != "ses_p" || !req.Fork {
		t.Errorf("server turn = session %q fork %v, want ses_p true", req.OpencodeSession, req.Fork)
	}
}
