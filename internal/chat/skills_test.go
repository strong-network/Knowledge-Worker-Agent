// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/env/envtest"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodeserver"
)

// stubSkills points the skill lookup at a fixed list for the duration of a
// test, so nothing here needs a running opencode server. It also clears the
// skill cache: changing what the enumeration returns invalidates anything an
// earlier test cached, and a stale entry would otherwise be served instead of
// the stub.
func stubSkills(t *testing.T, skills []opencodeserver.Skill, err error) {
	t.Helper()
	prev := listSkillsFn
	listSkillsFn = func(context.Context, string) ([]opencodeserver.Skill, error) {
		return skills, err
	}
	InvalidateSkillCache()
	t.Cleanup(func() {
		listSkillsFn = prev
		InvalidateSkillCache()
	})
}

func TestListSkillsOmitsUndescribedAndSorts(t *testing.T) {
	stubSkills(t, []opencodeserver.Skill{
		{Name: "ux-writing", Description: "Citrix UX writing guidelines", Content: "body"},
		{Name: "internal-only", Description: "  ", Content: "body"},
		{Name: "pdf", Description: "Work with PDFs", Content: "body"},
	}, nil)

	list, available := ListSkills(context.Background(), "/tmp")
	if !available {
		t.Fatal("expected the list to be reported as available")
	}
	if len(list) != 2 {
		t.Fatalf("expected the undescribed skill to be dropped, got %d: %+v", len(list), list)
	}
	if list[0].Name != "pdf" || list[1].Name != "ux-writing" {
		t.Fatalf("expected skills sorted by name, got %+v", list)
	}
	if list[0].ID != "pdf" {
		t.Errorf("expected id to be the skill name, got %q", list[0].ID)
	}
}

// A workspace with no skills and a workspace we could not ask must not look the
// same to the browser: the picker shows different copy for each.
func TestListSkillsDistinguishesEmptyFromUnavailable(t *testing.T) {
	stubSkills(t, []opencodeserver.Skill{}, nil)
	list, available := ListSkills(context.Background(), "/tmp")
	if !available {
		t.Fatal("an empty workspace is still an answer; expected available=true")
	}
	if list == nil {
		t.Fatal("expected an empty slice, not nil (it must encode as [] not null)")
	}

	stubSkills(t, nil, errors.New("connection refused"))
	list, available = ListSkills(context.Background(), "/tmp")
	if available {
		t.Fatal("expected available=false when the enumeration failed")
	}
	if len(list) != 0 {
		t.Fatalf("expected no skills when unavailable, got %+v", list)
	}
}

func TestResolveForcedSkillFindsBodyAndLocation(t *testing.T) {
	stubSkills(t, []opencodeserver.Skill{
		{Name: "pdf", Description: "PDFs", Content: "pdf body", Location: "/skills/pdf/SKILL.md"},
		{Name: "ux-writing", Description: "UX", Content: "ux body", Location: "/skills/ux-writing/SKILL.md"},
	}, nil)

	skill, ok := resolveForcedSkill(context.Background(), "/tmp", "ux-writing")
	if !ok {
		t.Fatal("expected a forced skill")
	}
	if skill.Content != "ux body" {
		t.Errorf("expected the armed skill's body, got %q", skill.Content)
	}
	if skill.Location != "/skills/ux-writing/SKILL.md" {
		t.Errorf("expected the skill's location to be carried through, got %q", skill.Location)
	}
}

func TestResolveForcedSkillEmptyNameForcesNothing(t *testing.T) {
	stubSkills(t, []opencodeserver.Skill{{Name: "pdf", Description: "PDFs", Content: "body"}}, nil)
	if _, ok := resolveForcedSkill(context.Background(), "/tmp", "  "); ok {
		t.Fatal("an unarmed turn must not force anything")
	}
}

// Losing a skill between arming and sending must not cost the user their
// message: the turn still goes out, carrying an explanation instead.
func TestResolveForcedSkillMissingStillSends(t *testing.T) {
	stubSkills(t, []opencodeserver.Skill{{Name: "pdf", Description: "PDFs", Content: "body"}}, nil)

	skill, ok := resolveForcedSkill(context.Background(), "/tmp", "deleted-skill")
	if !ok {
		t.Fatal("expected the turn to proceed with a notice, not to be dropped")
	}
	if skill.Content != "" {
		t.Errorf("expected no body for a missing skill, got %q", skill.Content)
	}
	rendered := config.ForcedSkillInstruction(skill)
	if !strings.Contains(rendered, "could not be found") {
		t.Errorf("expected a missing-skill notice, got %q", rendered)
	}
}

// Backend 1 of 2 (CLI, `opencode run` per turn): the forced skill must reach
// the spawned process's prompt argument.
func TestRunStreamCLIBackendForcesSkill(t *testing.T) {
	tmp := t.TempDir()
	if err := db.Init(filepath.Join(tmp, "test.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	argsPath := filepath.Join(tmp, "args.txt")
	bin := filepath.Join(tmp, "opencode")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsPath + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	envtest.Clear(t, "KWA_OPENCODE_USE_SERVER")
	t.Setenv("KWA_OPENCODE_AUTO_UPDATE", "false")
	t.Setenv("KWA_OPENCODE_INSTALL_DIR", filepath.Join(tmp, "install"))
	prevEnsured := opencodeEnsured
	opencodeEnsured = false
	t.Cleanup(func() { opencodeEnsured = prevEnsured })

	prevWorkspace, prevBin := config.Workspace, config.OpencodeBin
	config.Workspace, config.OpencodeBin = tmp, bin
	t.Cleanup(func() { config.Workspace, config.OpencodeBin = prevWorkspace, prevBin })

	stubSkills(t, []opencodeserver.Skill{{
		Name:        "ux-writing",
		Description: "Citrix UX writing guidelines",
		Content:     "Always use sentence case.",
		Location:    filepath.Join(tmp, "skills", "ux-writing", "SKILL.md"),
	}}, nil)

	cfg := config.SessionConfig{Backend: config.BackendOpencode, Workdir: tmp, Yolo: true}
	if err := db.CreateSession("cli-skill", cfg); err != nil {
		t.Fatal(err)
	}

	events, proc := RunStream("cli-skill", "write an error message", cfg, Turn{Skill: "ux-writing"})
	if proc == nil {
		t.Fatal("expected an active process")
	}
	drain(t, events)

	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(args)
	for _, want := range []string{
		"[System — forced skill]",
		`<skill_content name="ux-writing">`,
		"Always use sentence case.",
		"write an error message",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("CLI backend prompt missing %q; got:\n%s", want, got)
		}
	}
}

// An unarmed turn on the CLI backend must carry no skill block at all — the
// default has to stay "nothing is forced".
func TestRunStreamCLIBackendWithoutSkillIsUnchanged(t *testing.T) {
	tmp := t.TempDir()
	if err := db.Init(filepath.Join(tmp, "test.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	argsPath := filepath.Join(tmp, "args.txt")
	bin := filepath.Join(tmp, "opencode")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+argsPath+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	envtest.Clear(t, "KWA_OPENCODE_USE_SERVER")
	t.Setenv("KWA_OPENCODE_AUTO_UPDATE", "false")
	t.Setenv("KWA_OPENCODE_INSTALL_DIR", filepath.Join(tmp, "install"))
	prevEnsured := opencodeEnsured
	opencodeEnsured = false
	t.Cleanup(func() { opencodeEnsured = prevEnsured })

	prevWorkspace, prevBin := config.Workspace, config.OpencodeBin
	config.Workspace, config.OpencodeBin = tmp, bin
	t.Cleanup(func() { config.Workspace, config.OpencodeBin = prevWorkspace, prevBin })

	cfg := config.SessionConfig{Backend: config.BackendOpencode, Workdir: tmp, Yolo: true}
	if err := db.CreateSession("cli-plain", cfg); err != nil {
		t.Fatal(err)
	}

	events, _ := RunStream("cli-plain", "just a question", cfg)
	drain(t, events)

	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(args), "forced skill") {
		t.Fatalf("an unarmed turn must not carry a skill block; got:\n%s", args)
	}
}

// Backend 2 of 2 (`opencode serve`): the request handed to the server carries
// the same forced-skill block. Asserted on the request builder because a real
// server process is not available under test.
func TestServerBackendTurnRequestForcesSkill(t *testing.T) {
	tmp := t.TempDir()
	if err := db.Init(filepath.Join(tmp, "test.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	prevWorkspace := config.Workspace
	config.Workspace = tmp
	t.Cleanup(func() { config.Workspace = prevWorkspace })

	cfg := config.SessionConfig{Backend: config.BackendOpencode, Workdir: tmp}
	forced := []config.ForcedSkill{{
		Name:     "ux-writing",
		Content:  "Always use sentence case.",
		Location: filepath.Join(tmp, "skills", "ux-writing", "SKILL.md"),
	}}

	req := buildServerTurnRequest("srv-skill", "write an error message", cfg, forced, Author{})
	for _, want := range []string{
		"[System — forced skill]",
		`<skill_content name="ux-writing">`,
		"Always use sentence case.",
		"write an error message",
	} {
		if !strings.Contains(req.Prompt, want) {
			t.Fatalf("server backend prompt missing %q; got:\n%s", want, req.Prompt)
		}
	}

	plain := buildServerTurnRequest("srv-plain", "just a question", cfg, nil, Author{})
	if strings.Contains(plain.Prompt, "forced skill") {
		t.Fatalf("an unarmed turn must not carry a skill block; got:\n%s", plain.Prompt)
	}
}

// A skill armed when a message is queued belongs to that message, not to
// whatever the user types next.
func TestQueuedPromptCarriesItsOwnSkill(t *testing.T) {
	resetQueuesForTest()
	t.Cleanup(resetQueuesForTest)

	withSkill := EnqueuePrompt("sess", "make a deck", Turn{Skill: "citrix-slide-system"})
	plain := EnqueuePrompt("sess", "and then summarise it")

	if withSkill.Skill != "citrix-slide-system" {
		t.Errorf("expected the queued item to carry its skill, got %q", withSkill.Skill)
	}
	if plain.Skill != "" {
		t.Errorf("expected the next queued item to be unarmed, got %q", plain.Skill)
	}

	items := SnapshotQueue("sess")
	if len(items) != 2 || items[0].Skill != "citrix-slide-system" || items[1].Skill != "" {
		t.Fatalf("skill must not leak between queued prompts: %+v", items)
	}
}

// drain reads a stream to completion so the child process finishes writing
// before the test inspects what it was given.
func drain(t *testing.T, events <-chan *Event) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("timed out waiting for the stream to finish")
		}
	}
}

// A skill armed in the composer is injected into the preamble and never travels
// through opencode's tool channel, so without a stand-in the transcript shows no
// sign it was applied. These assert the stand-in the UI reads.
func TestForcedSkillEventsReportAResolvedSkill(t *testing.T) {
	events := forcedSkillEvents([]config.ForcedSkill{
		{Name: "ux-writing", Content: "body", Location: "/skills/ux-writing/SKILL.md"},
	})

	if len(events) != 2 {
		t.Fatalf("expected a tool_call/tool_done pair, got %d: %+v", len(events), events)
	}
	call, done := events[0], events[1]
	if call.Kind != "tool_call" || done.Kind != "tool_done" {
		t.Fatalf("expected a call then a done, got %q then %q", call.Kind, done.Kind)
	}
	// The UI groups skills by tool name; a different name would land the armed
	// skill back in the ordinary tool list.
	if call.Tool != skillToolName || done.Tool != skillToolName {
		t.Errorf("expected both events under the %q tool, got %q and %q", skillToolName, call.Tool, done.Tool)
	}
	if call.CallID == "" || call.CallID != done.CallID {
		t.Errorf("expected one shared call id, got %q and %q", call.CallID, done.CallID)
	}
	if got := call.Args["name"]; got != "ux-writing" {
		t.Errorf("expected the skill name in the args, got %v", got)
	}
	if got := call.Args["forced"]; got != true {
		t.Errorf("expected the args to mark the skill as user-armed, got %v", got)
	}
	if !done.Success {
		t.Error("a skill that resolved should not be reported as failed")
	}
}

// A skill that vanished between arming and sending is the one case the user must
// be told about: the preamble already tells the model, and the transcript has to
// agree.
func TestForcedSkillEventsReportAnUnresolvedSkillAsFailed(t *testing.T) {
	events := forcedSkillEvents([]config.ForcedSkill{{Name: "gone"}})

	if len(events) != 2 {
		t.Fatalf("expected a pair even for a missing skill, got %d", len(events))
	}
	done := events[1]
	if done.Success {
		t.Error("expected a skill with no body to be reported as failed")
	}
	if !strings.Contains(strings.ToLower(done.Output), "no longer exists") {
		t.Errorf("expected the failure to explain itself, got %q", done.Output)
	}
	if got := events[0].Args["name"]; got != "gone" {
		t.Errorf("expected the missing skill still to be named, got %v", got)
	}
}

// The count is meant to report work the model did. A preamble injection costs no
// round trip, so counting it would overstate the turn.
func TestForcedSkillEventsAreMarkedSynthetic(t *testing.T) {
	for _, e := range forcedSkillEvents([]config.ForcedSkill{{Name: "ux-writing", Content: "body"}}) {
		if !e.Synthetic {
			t.Errorf("expected %s to be marked synthetic so it is not counted as a tool call", e.Kind)
		}
	}
}

func TestForcedSkillEventsIgnoreAnEmptyName(t *testing.T) {
	if events := forcedSkillEvents([]config.ForcedSkill{{Name: "  "}}); len(events) != 0 {
		t.Fatalf("expected a nameless skill to produce nothing, got %+v", events)
	}
	if events := forcedSkillEvents(nil); len(events) != 0 {
		t.Fatalf("expected no forced skills to produce nothing, got %+v", events)
	}
}

// The stand-in has to arrive before the model's own events, or the transcript
// claims the agent worked before it had the skill it was told to use.
func TestPrependEventsPutsTheHeadFirstAndRelaysTheRest(t *testing.T) {
	src := make(chan *Event, 2)
	src <- &Event{Kind: "chunk", Text: "answer"}
	src <- &Event{Kind: "result"}
	close(src)

	out := prependEvents(src, []*Event{{Kind: "tool_call", Tool: skillToolName}})

	var kinds []string
	for e := range out {
		kinds = append(kinds, e.Kind)
	}
	if strings.Join(kinds, ",") != "tool_call,chunk,result" {
		t.Fatalf("expected the head first then the stream, got %v", kinds)
	}
}

// Nothing to prepend must cost nothing: the original channel is handed back so
// the common path gains no goroutine or copy.
func TestPrependEventsReturnsTheSourceWhenThereIsNoHead(t *testing.T) {
	src := make(chan *Event)
	if out := prependEvents(src, nil); out != (<-chan *Event)(src) {
		t.Fatal("expected the source channel to be returned unchanged")
	}
}
