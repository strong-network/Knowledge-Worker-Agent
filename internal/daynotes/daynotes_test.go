// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package daynotes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// stubRun replaces the opencode call for the duration of a test.
func stubRun(t *testing.T, out []byte, err error) *string {
	t.Helper()
	var seen string
	prev := runNote
	runNote = func(_ context.Context, _, _, prompt string) ([]byte, error) {
		seen = prompt
		return out, err
	}
	t.Cleanup(func() { runNote = prev })
	return &seen
}

// jsonl renders opencode's run output for a successful generation.
func jsonl(text string) []byte {
	var b strings.Builder
	b.WriteString(`{"type":"session","sessionID":"ses_throwaway"}` + "\n")
	line, _ := json.Marshal(map[string]any{
		"type": "text",
		"part": map[string]string{"text": text},
	})
	b.Write(line)
	b.WriteString("\n")
	return []byte(b.String())
}

func msgs(contents ...string) []db.RecallMessage {
	out := make([]db.RecallMessage, 0, len(contents))
	for i, c := range contents {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		out = append(out, db.RecallMessage{Role: role, At: "2026-03-01 10:00:00", Content: c})
	}
	return out
}

// --- the job's visibility -----------------------------------------------

// Start has to say what it is doing.
//
// This is a regression test for a real support cost rather than a style
// preference. `notes` is an empty array both when the job has simply not caught
// up yet and when it is broken, so the API cannot distinguish the two and
// neither can the person reading it: the feature was once deployed inert and
// the empty arrays were reported as a malfunction after twenty minutes of
// waiting for something that was never going to run. Silence was the whole
// defect. One startup line saying the job is running, and how often, is what
// lets an empty array be read correctly.
func TestStartAnnouncesItself(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	Start()

	got := buf.String()
	if got == "" {
		t.Fatal("Start() logged nothing; an operator cannot tell 'not yet' from 'broken'")
	}
	if !strings.Contains(got, "[DAYNOTE]") {
		t.Errorf("startup line is not attributable to day notes: %q", got)
	}
}

// --- the model bound ----------------------------------------------------

// A note pass must never run on a frontier model. Notes are generated in bulk,
// in the background, over history the user is not looking at; billing that at
// Opus rates is not a tradeoff anyone opted into. When nothing small-class
// resolves the pass declines rather than falling back.
//
// The workspace default is deliberately set to an expensive model here. Without
// that, a fallback to config.ResolveModel("") would return "" anyway (no
// default discovered in a unit test) and this test would pass whether or not
// the bound existed -- which is exactly what it did on the first attempt.
func TestNoteModelRefusesTheExpensiveWorkspaceDefault(t *testing.T) {
	config.SetPresetModels(nil)
	config.SetAvailableModels([]string{
		"github-copilot/claude-sonnet-5",
		"github-copilot/claude-opus-5",
	})
	config.SetDefaultModelForProvider([]string{"github-copilot/claude-opus-5"}, "")
	t.Cleanup(func() {
		config.SetAvailableModels(nil)
		config.SetPresetModels(nil)
		config.SetDefaultModelForProvider(nil, "")
	})

	// Guard the guard: if the workspace default were empty, the assertion
	// below would hold for the wrong reason.
	if config.DefaultModel() == "" {
		t.Fatal("setup: expected a workspace default to fall back to")
	}
	if got := noteModel(); got != "" {
		t.Errorf("noteModel() = %q, want \"\" -- notes must not run on the workspace default", got)
	}
}

func TestRunOnceRefusesToRunOnAnExpensiveModel(t *testing.T) {
	config.SetPresetModels(nil)
	config.SetAvailableModels([]string{
		"github-copilot/claude-sonnet-5",
		"github-copilot/claude-opus-5",
	})
	t.Cleanup(func() {
		config.SetAvailableModels(nil)
		config.SetPresetModels(nil)
	})

	called := false
	prev := runNote
	runNote = func(context.Context, string, string, string) ([]byte, error) {
		called = true
		return nil, nil
	}
	t.Cleanup(func() { runNote = prev })

	var buf bytes.Buffer
	prevOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	res := RunOnce(nowFixed())

	if called {
		t.Error("a pass ran a generation with only expensive models available")
	}
	if res.Considered != 0 || res.Written != 0 {
		t.Errorf("pass did work with no small model: %+v", res)
	}
	// Same lesson as the startup line: skipping quietly is indistinguishable
	// from being broken.
	if !strings.Contains(buf.String(), "small") {
		t.Errorf("skip was not explained in terms of the small model: %q", buf.String())
	}
}

// The mirror image: when a small-class model is available the pass proceeds to
// look at the backlog. Without this, the test above would still pass if notes
// were broken outright.
func TestRunOnceProceedsWhenASmallModelResolves(t *testing.T) {
	config.SetPresetModels(nil)
	config.SetAvailableModels([]string{
		"github-copilot/claude-sonnet-5",
		"github-copilot/claude-haiku-4.5",
	})
	t.Cleanup(func() {
		config.SetAvailableModels(nil)
		config.SetPresetModels(nil)
	})

	if got := noteModel(); got != "github-copilot/claude-haiku-4.5" {
		t.Fatalf("noteModel() = %q, want the small-class model", got)
	}
}

// --- generation ---------------------------------------------------------

func TestGenerateReturnsTheNote(t *testing.T) {
	stubRun(t, jsonl("Refactored the recall caps and added offset paging."), nil)

	got, err := Generate(context.Background(), "opencode", "some/model",
		msgs("please fix recall", "done"))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if got != "Refactored the recall caps and added offset paging." {
		t.Errorf("note = %q", got)
	}
}

// opencode prints this warning and then silently runs the DEFAULT agent -- a
// full, tool-enabled turn. Its output is not a note and must never be stored.
func TestGenerateRefusesWhenTheAgentDidNotResolve(t *testing.T) {
	out := append([]byte("Falling back to default agent\n"), jsonl("looks like a note")...)
	stubRun(t, out, nil)

	if _, err := Generate(context.Background(), "opencode", "some/model", msgs("hi", "there")); err == nil {
		t.Error("expected a refusal when the title agent did not resolve")
	}
}

// A failed run exits 0 and reports only an error event, so the exit code cannot
// be relied on to detect it.
func TestGenerateReportsARunErrorDespiteACleanExit(t *testing.T) {
	line, _ := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]any{
			"name": "ProviderAuthError",
			"data": map[string]string{"message": "no credentials"},
		},
	})
	stubRun(t, append(line, '\n'), nil)

	_, err := Generate(context.Background(), "opencode", "some/model", msgs("hi", "there"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "no credentials") {
		t.Errorf("error should carry the reason, got %v", err)
	}
}

func TestGenerateNeedsAResolvedModel(t *testing.T) {
	stubRun(t, jsonl("note"), nil)
	if _, err := Generate(context.Background(), "opencode", "  ", msgs("hi", "there")); err == nil {
		t.Error("expected an error with no model: opencode has no default provider")
	}
}

func TestGenerateRefusesAnEmptyDay(t *testing.T) {
	stubRun(t, jsonl("note"), nil)
	if _, err := Generate(context.Background(), "opencode", "m", msgs("   ", "")); err == nil {
		t.Error("expected an error when there is nothing to summarise")
	}
}

func TestGenerateRejectsAnEmptyResponse(t *testing.T) {
	stubRun(t, jsonl("   "), nil)
	if _, err := Generate(context.Background(), "opencode", "m", msgs("hi", "there")); err == nil {
		t.Error("expected an error when the model produced no text")
	}
}

// The instruction has to reach the model; without it the run summarises nothing
// in particular and the transcript arrives unlabelled.
func TestGenerateSendsTheInstructionAndTheTranscript(t *testing.T) {
	seen := stubRun(t, jsonl("ok"), nil)

	if _, err := Generate(context.Background(), "opencode", "m",
		msgs("add offset to search", "added it")); err != nil {
		t.Fatalf("generate: %v", err)
	}
	for _, want := range []string{"at most 120 words", "--- TRANSCRIPT ---", "USER: add offset to search", "ASSISTANT: added it"} {
		if !strings.Contains(*seen, want) {
			t.Errorf("prompt is missing %q\n--- prompt ---\n%s", want, *seen)
		}
	}
}

func TestCleanNoteStripsThinkingAndQuotes(t *testing.T) {
	stubRun(t, jsonl("<think>hmm, what happened</think>\n\"Fixed the parser.\""), nil)

	got, err := Generate(context.Background(), "opencode", "m", msgs("hi", "there"))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if got != "Fixed the parser." {
		t.Errorf("note = %q, want the reasoning and quotes removed", got)
	}
}

func TestCleanNoteCapsARunawayResponse(t *testing.T) {
	stubRun(t, jsonl(strings.Repeat("x", maxNoteChars*3)), nil)

	got, err := Generate(context.Background(), "opencode", "m", msgs("hi", "there"))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len([]rune(got)) > maxNoteChars {
		t.Errorf("note is %d runes, want <= %d", len([]rune(got)), maxNoteChars)
	}
}

// --- transcript ---------------------------------------------------------

func TestBuildTranscriptLabelsRolesAndSkipsEmptyMessages(t *testing.T) {
	got := BuildTranscript([]db.RecallMessage{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "   "},
		{Role: "assistant", Content: "second"},
	})
	if strings.Count(got, "\n\n") != 1 {
		t.Errorf("empty message was not skipped:\n%s", got)
	}
	if !strings.Contains(got, "USER: first") || !strings.Contains(got, "ASSISTANT: second") {
		t.Errorf("roles not labelled:\n%s", got)
	}
}

// One enormous paste must not crowd out the rest of the day.
func TestBuildTranscriptCapsASingleHugeMessage(t *testing.T) {
	got := BuildTranscript([]db.RecallMessage{
		{Role: "user", Content: strings.Repeat("p", maxMessageChars*4)},
		{Role: "assistant", Content: "and here is the reply that must survive"},
	})
	if !strings.Contains(got, "and here is the reply that must survive") {
		t.Error("the huge paste swallowed the rest of the day")
	}
	if !strings.Contains(got, "[…]") {
		t.Error("truncation of the huge message was not marked")
	}
}

// A day that does not fit must lose its MIDDLE, not its tail. What was
// concluded and what was left open is the part a note most needs; a plain
// prefix cut would discard exactly that while still reading confidently.
func TestBuildTranscriptKeepsBothEndsOfAnOversizedDay(t *testing.T) {
	var day []db.RecallMessage
	day = append(day, db.RecallMessage{Role: "user", Content: "OPENING REQUEST"})
	for i := 0; i < 60; i++ {
		day = append(day, db.RecallMessage{
			Role:    "assistant",
			Content: fmt.Sprintf("filler %d ", i) + strings.Repeat("z", maxMessageChars),
		})
	}
	day = append(day, db.RecallMessage{Role: "assistant", Content: "CLOSING DECISION"})

	got := BuildTranscript(day)

	if len(got) > maxTranscriptChars+maxMessageChars {
		t.Errorf("transcript is %d chars, well over the %d budget", len(got), maxTranscriptChars)
	}
	if !strings.Contains(got, "OPENING REQUEST") {
		t.Error("the start of the day was dropped")
	}
	if !strings.Contains(got, "CLOSING DECISION") {
		t.Error("the END of the day was dropped -- that is the part a note most needs")
	}
	if !strings.Contains(got, "messages omitted from the middle") {
		t.Error("messages were dropped without saying so; the model cannot tell it is seeing a partial day")
	}
}

// A day that fits must arrive whole, with no omission marker inviting the model
// to hedge about material it can actually see.
func TestBuildTranscriptLeavesASmallDayIntact(t *testing.T) {
	got := BuildTranscript(msgs("one", "two", "three", "four"))
	if strings.Contains(got, "omitted") {
		t.Errorf("a day well inside the budget was trimmed:\n%s", got)
	}
	for _, want := range []string{"one", "two", "three", "four"} {
		if !strings.Contains(got, want) {
			t.Errorf("message %q missing from an untrimmed day", want)
		}
	}
}

func TestBuildTranscriptOfNothingIsEmpty(t *testing.T) {
	if got := BuildTranscript(nil); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := BuildTranscript(msgs("", "  ")); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// A full batch means there is more waiting. Sleeping off the rest of a six-hour
// cycle between batches is what made the first backfill take days of wall clock
// for minutes of work.
func TestNextDelayKeepsGoingWhileTheBacklogIsFull(t *testing.T) {
	res := PassResult{Considered: perPassLimit, Written: perPassLimit}
	if got := res.nextDelay(); got != drainInterval {
		t.Errorf("nextDelay() = %s, want %s while draining", got, drainInterval)
	}
}

// Anything short of a full batch means the backlog is exhausted, so the job
// settles to the slow cadence rather than waking every minute for nothing.
func TestNextDelaySettlesOnceCaughtUp(t *testing.T) {
	res := PassResult{Considered: perPassLimit - 1, Written: perPassLimit - 1}
	if got := res.nextDelay(); got != passInterval {
		t.Errorf("nextDelay() = %s, want %s once caught up", got, passInterval)
	}
}

// The hazard the Written > 0 condition exists for. Failures stay in the backlog
// by design, so a batch that fails wholesale is still a *full* batch: keyed on
// size alone the job would re-offer the same doomed days every minute for ever,
// spending a model call on each. Progress, not backlog size, is what earns the
// fast cadence.
func TestNextDelayBacksOffWhenAFullBatchWroteNothing(t *testing.T) {
	res := PassResult{Considered: perPassLimit, Written: 0, Failed: perPassLimit}
	if got := res.nextDelay(); got != passInterval {
		t.Errorf("nextDelay() = %s, want %s so a failing batch cannot spin", got, passInterval)
	}
}

// Partial failure is still progress: the batch shrinks the backlog even though
// one day could not be summarised, so draining should continue.
func TestNextDelayKeepsDrainingThroughPartialFailure(t *testing.T) {
	res := PassResult{Considered: perPassLimit, Written: perPassLimit - 1, Failed: 1}
	if got := res.nextDelay(); got != drainInterval {
		t.Errorf("nextDelay() = %s, want %s when the batch made progress", got, drainInterval)
	}
}

// An empty pass costs no model calls; it must not schedule itself tightly.
func TestNextDelaySettlesOnAnEmptyPass(t *testing.T) {
	if got := (PassResult{}).nextDelay(); got != passInterval {
		t.Errorf("nextDelay() = %s, want %s for an empty pass", got, passInterval)
	}
}

// Draining must be meaningfully faster than the steady-state cadence, or the
// constant is decorative.
func TestDrainIntervalIsShorterThanTheSteadyStateInterval(t *testing.T) {
	if drainInterval >= passInterval {
		t.Fatalf("drainInterval %s must be shorter than passInterval %s", drainInterval, passInterval)
	}
}
