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

var twoQuestions = map[string]any{"questions": []any{
	map[string]any{"header": "Scope", "question": "What should I create\nin Jira?", "options": []any{
		map[string]any{"label": "Epic + bug (Recommended)", "description": "The epic, and the bug linked to it"},
		map[string]any{"label": "Epic only"},
		map[string]any{"label": " "},
	}},
	map[string]any{"question": "Which labels?", "multiple": true, "options": []any{map[string]any{"label": "ui"}, map[string]any{"label": "api"}}},
	map[string]any{"question": "  "},
}}

func TestAskedQuestions(t *testing.T) {
	qs := askedQuestions("question", twoQuestions)
	if len(qs) != 2 || qs[0].Header != "Scope" || len(qs[0].Options) != 2 || qs[0].Options[0].Description != "The epic, and the bug linked to it" || !qs[1].Multiple {
		t.Fatalf("questions: %+v", qs)
	}
	if qs := askedQuestions("bash", twoQuestions); qs != nil {
		t.Errorf("another tool's input: %+v", qs)
	}
	if qs := askedQuestions("question", map[string]any{"questions": "not a list"}); qs != nil {
		t.Errorf("bad input: %+v", qs)
	}
}

func TestQuestionsMarkdown(t *testing.T) {
	want := "1. **Scope:** What should I create in Jira?\n" +
		"   - **Epic + bug (Recommended)**: The epic, and the bug linked to it\n" +
		"   - **Epic only**\n" +
		"2. Which labels? (choose any)\n" +
		"   - **ui**\n" +
		"   - **api**\n"
	if got := questionsMarkdown(askedQuestions("question", twoQuestions)); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	one := []AskedQuestion{{Question: "Go ahead?", Options: []AskedOption{{Label: "Yes"}}}}
	if got := questionsMarkdown(one); got != "Go ahead?\n- **Yes**\n" {
		t.Errorf("one question: %q", got)
	}
	asking := []AskedQuestion{{Header: "Which draft?", Question: "Which draft should I delete?"}}
	if got := questionsMarkdown(asking); got != "**Which draft?** Which draft should I delete?\n" {
		t.Errorf("a header that asks: %q", got)
	}
}

// The reply keeps the questions, so a reload shows what was asked.
func TestAQuestionIsSavedWithTheReply(t *testing.T) {
	qs := askedQuestions("question", twoQuestions)
	saved, frames := runTurn(t, []*Event{{Kind: "chunk", Text: "Nearly there."}, {Kind: "question", Question: qs[0].Question, Questions: qs}}, nil)
	if !strings.HasPrefix(saved, "Nearly there.\n\n1. **Scope:**") {
		t.Errorf("saved %q", saved)
	}
	var q map[string]any
	for _, f := range frames {
		if f["type"] == "question" {
			q = f
		}
	}
	if got, _ := q["questions"].([]AskedQuestion); len(got) != 2 {
		t.Errorf("question frame: %+v", q)
	}
}

// `opencode run` cancels a question and tells the agent to assume an answer:
// the turn stops there instead, with the questions for the user.
func TestRunStreamStopsAtTheAgentsQuestion(t *testing.T) {
	cfg := fakeAskingOpencode(t, "exec sleep 30\n")

	start := time.Now()
	events, _ := RunStream("q-test", "make the tickets", cfg)
	var kinds []string
	var question *Event
	timeout := time.After(20 * time.Second)
	for done := false; !done; {
		select {
		case ev, ok := <-events:
			if !ok {
				done = true
				break
			}
			kinds = append(kinds, ev.Kind)
			if ev.Kind == "question" {
				question = ev
			}
			if ev.Kind == "chunk" {
				t.Errorf("the agent went on after its question: %q", ev.Text)
			}
			if ev.Kind == "tool_done" && (!ev.Success || ev.Output != "Waiting for your answer.") {
				t.Errorf("question row: %+v", ev)
			}
		case <-timeout:
			t.Fatal("the turn didn't stop at the question")
		}
	}
	if question == nil || len(question.Questions) != 1 || question.Questions[0].Header != "Scope" {
		t.Fatalf("question event: %+v", question)
	}
	if strings.Join(kinds, ",") != "tool_call,tool_done,question,result" {
		t.Errorf("events: %v", kinds)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("stopping took %s", time.Since(start))
	}
}

// fakeAskingOpencode is an opencode run that asks a question, which comes back
// cancelled, then goes on with an assumption and runs tail.
func fakeAskingOpencode(t *testing.T, tail string) config.SessionConfig {
	t.Helper()
	tmp := t.TempDir()
	if err := db.Init(filepath.Join(tmp, "test.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	bin := filepath.Join(tmp, "opencode")
	script := "#!/bin/sh\n" +
		"case \"$1\" in --version) echo 'opencode 1.19.4'; exit 0;; esac\n" +
		`printf '%s\n' '{"type":"tool_use","timestamp":1,"sessionID":"oc-q","part":{"id":"call_q","tool":"question","state":{"status":"error","input":{"questions":[{"header":"Scope","question":"What should I create?","options":[{"label":"Epic only"}]}]},"error":"This non-interactive run cannot ask the user questions, so the question was cancelled. Continue without an answer; make reasonable assumptions and state them."}}}'` + "\n" +
		`printf '%s\n' '{"type":"text","timestamp":2,"sessionID":"oc-q","part":{"type":"text","text":"I will assume Epic only."}}'` + "\n" +
		tail
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KWA_OPENCODE_AUTO_UPDATE", "false")
	t.Setenv("KWA_OPENCODE_INSTALL_DIR", filepath.Join(tmp, "install"))
	prevEnsured, prevBin, prevWS := opencodeEnsured, config.OpencodeBin, config.Workspace
	opencodeEnsured = false
	t.Cleanup(func() { opencodeEnsured, config.OpencodeBin, config.Workspace = prevEnsured, prevBin, prevWS })
	config.Workspace, config.OpencodeBin = tmp, bin
	cfg := config.SessionConfig{Backend: config.BackendOpencode, Workdir: tmp, Yolo: true}
	if err := db.CreateSession("q-test", cfg); err != nil {
		t.Fatal(err)
	}
	return cfg

}

// No one answers a scheduled run, so it goes on with an assumption, as before.
func TestAnUnattendedRunDoesNotStopAtAQuestion(t *testing.T) {
	cfg := fakeAskingOpencode(t, "")
	cfg.Unattended = true
	events, _ := RunStream("q-test", "make the tickets", cfg)
	var kinds []string
	timeout := time.After(20 * time.Second)
	for done := false; !done; {
		select {
		case ev, ok := <-events:
			if !ok {
				done = true
				break
			}
			kinds = append(kinds, ev.Kind)
			if ev.Kind == "tool_done" && (ev.Success || !strings.Contains(ev.Output, "cancelled")) {
				t.Errorf("question row: %+v", ev)
			}
		case <-timeout:
			t.Fatal("the run didn't end")
		}
	}
	if strings.Join(kinds, ",") != "tool_call,tool_done,chunk,result" {
		t.Errorf("events: %v", kinds)
	}
}
