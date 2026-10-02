// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"fmt"
	"testing"
)

func TestSavedPrompts_SeedOnce(t *testing.T) {
	setupTestDB(t)

	// First seed inserts the built-in defaults.
	if err := SeedDefaultSavedPromptsOnce(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got := ListSavedPrompts()
	if len(got) != len(defaultSavedPrompts) {
		t.Fatalf("expected %d seeded prompts, got %d", len(defaultSavedPrompts), len(got))
	}
	// "Research a company" leads the row and runs on Thinking.
	if got[0].Name != researchCompanyName {
		t.Errorf("first chip = %q, want %q", got[0].Name, researchCompanyName)
	}
	if got[0].AgentKind != "thinking" {
		t.Errorf("first chip agent kind = %q, want thinking", got[0].AgentKind)
	}
	if got[0].Prompt != researchCompanyPrompt {
		t.Errorf("first chip prompt = %q, want %q", got[0].Prompt, researchCompanyPrompt)
	}
	if got[1].AgentKind != "default" {
		t.Errorf("second chip agent kind = %q, want default", got[1].AgentKind)
	}

	// Deleting all and seeding again must NOT re-seed (empty stays empty).
	for _, p := range got {
		if err := DeleteSavedPrompt(p.ID); err != nil {
			t.Fatalf("delete: %v", err)
		}
	}
	if err := SeedDefaultSavedPromptsOnce(); err != nil {
		t.Fatalf("reseed: %v", err)
	}
	if n := CountSavedPrompts(); n != 0 {
		t.Errorf("expected empty row after delete-all (no re-seed), got %d", n)
	}
}

// seedLegacyRow reproduces an install seeded before the "Research a company"
// chip changed: the old label-as-prompt chips, and the seeded flag already set
// so SeedDefaultSavedPromptsOnce is a no-op. This is the state that made the
// upgrade necessary — without it the new chip reaches new users only.
func seedLegacyRow(t *testing.T) {
	t.Helper()
	legacy := []struct{ id, name, prompt string }{
		{"seed-0", "Create a slide for me", "Create a slide for me"},
		{"seed-1", "Summarize a document", "Summarize a document"},
		{"seed-2", "Draft an email", "Draft an email"},
		{"seed-3", researchCompanyName, researchCompanyLegacyPrompt},
		{"seed-4", "Turn notes into a brief", "Turn notes into a brief"},
	}
	for i, p := range legacy {
		if _, err := DB.Exec(
			`INSERT INTO saved_prompts (id, name, prompt, agent_kind, agent_id, position, created_at, updated_at)
			 VALUES (?, ?, ?, 'default', '', ?, '', '')`,
			p.id, p.name, p.prompt, i,
		); err != nil {
			t.Fatalf("seed legacy: %v", err)
		}
	}
	if err := metaSet(savedPromptsSeededKey, "1"); err != nil {
		t.Fatalf("mark seeded: %v", err)
	}
}

func TestResearchCompanyUpgrade_RewritesUntouchedChipInPlace(t *testing.T) {
	setupTestDB(t)
	seedLegacyRow(t)

	// Guard the premise: seeding alone can't deliver the chip to this install.
	if err := SeedDefaultSavedPromptsOnce(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if p := GetSavedPrompt("seed-3"); p == nil || p.Prompt != researchCompanyLegacyPrompt {
		t.Fatalf("precondition: seeding unexpectedly changed the chip: %+v", p)
	}

	if err := UpgradeResearchCompanyPromptOnce(); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	list := ListSavedPrompts()
	if n := len(list); n != 5 {
		t.Fatalf("expected 5 chips (rewrite, not insert), got %d", n)
	}
	if list[0].ID != "seed-3" {
		t.Errorf("expected the chip moved to the front, got %q", list[0].ID)
	}
	if list[0].Prompt != researchCompanyPrompt {
		t.Errorf("prompt = %q, want %q", list[0].Prompt, researchCompanyPrompt)
	}
	if list[0].AgentKind != "thinking" {
		t.Errorf("agent kind = %q, want thinking", list[0].AgentKind)
	}
	// Exactly one chip may carry the label, or the row shows a duplicate.
	count := 0
	for _, p := range list {
		if p.Name == researchCompanyName {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 %q chip, got %d", researchCompanyName, count)
	}
}

func TestResearchCompanyUpgrade_LeavesAnEditedChipAlone(t *testing.T) {
	setupTestDB(t)
	seedLegacyRow(t)
	const mine = "Research a company the way I like it"
	if _, err := UpdateSavedPrompt("seed-3", researchCompanyName, mine, "default", ""); err != nil {
		t.Fatalf("edit: %v", err)
	}

	if err := UpgradeResearchCompanyPromptOnce(); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	got := GetSavedPrompt("seed-3")
	if got == nil {
		t.Fatal("the user's chip was deleted by the upgrade")
	}
	if got.Prompt != mine {
		t.Errorf("user's edit was overwritten: %q", got.Prompt)
	}
	if n := CountSavedPrompts(); n != 5 {
		t.Errorf("expected no chip added alongside the edited one, got %d", n)
	}
}

func TestResearchCompanyUpgrade_InsertsWhenChipIsGone(t *testing.T) {
	setupTestDB(t)
	seedLegacyRow(t)
	if err := DeleteSavedPrompt("seed-3"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if err := UpgradeResearchCompanyPromptOnce(); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	list := ListSavedPrompts()
	if len(list) != 5 {
		t.Fatalf("expected the chip to be inserted, got %d chips", len(list))
	}
	if list[0].Name != researchCompanyName || list[0].AgentKind != "thinking" {
		t.Errorf("front chip = %+v, want the Thinking research chip", list[0])
	}
}

func TestResearchCompanyUpgrade_RunsOnlyOnce(t *testing.T) {
	setupTestDB(t)
	seedLegacyRow(t)
	if err := UpgradeResearchCompanyPromptOnce(); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	// A user who deletes the upgraded chip must not have it come back.
	if err := DeleteSavedPrompt("seed-3"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := UpgradeResearchCompanyPromptOnce(); err != nil {
		t.Fatalf("second upgrade: %v", err)
	}
	for _, p := range ListSavedPrompts() {
		if p.Name == researchCompanyName {
			t.Fatalf("deleted chip was resurrected by a second run")
		}
	}
}

func TestResearchCompanyUpgrade_RespectsChipCap(t *testing.T) {
	setupTestDB(t)
	for i := 0; i < MaxSavedPrompts; i++ {
		if _, err := CreateSavedPrompt(
			fmt.Sprintf("mine-%d", i), fmt.Sprintf("Mine %d", i), "prompt", "default", "",
		); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	if err := metaSet(savedPromptsSeededKey, "1"); err != nil {
		t.Fatalf("mark seeded: %v", err)
	}

	if err := UpgradeResearchCompanyPromptOnce(); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if n := CountSavedPrompts(); n != MaxSavedPrompts {
		t.Errorf("cap exceeded: %d chips, max is %d", n, MaxSavedPrompts)
	}
}

func TestSavedPrompts_CreatePrependsAndReorder(t *testing.T) {
	setupTestDB(t)
	if err := SeedDefaultSavedPromptsOnce(); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A newly created prompt appears first (position below the current min).
	created, err := CreateSavedPrompt("mine", "Account briefing", "Research {account} and draft a briefing", "custom", "demo-creator")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created == nil || created.AgentKind != "custom" || created.AgentID != "demo-creator" {
		t.Fatalf("unexpected created record: %+v", created)
	}
	list := ListSavedPrompts()
	if list[0].ID != "mine" {
		t.Errorf("expected new prompt first, got %q", list[0].ID)
	}

	// Reorder puts it last; the order must persist.
	ids := make([]string, 0, len(list))
	for i := 1; i < len(list); i++ {
		ids = append(ids, list[i].ID)
	}
	ids = append(ids, "mine")
	if err := ReorderSavedPrompts(ids); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	after := ListSavedPrompts()
	if after[len(after)-1].ID != "mine" {
		t.Errorf("expected reordered prompt last, got %q", after[len(after)-1].ID)
	}
}

func TestSavedPrompts_Update(t *testing.T) {
	setupTestDB(t)
	created, err := CreateSavedPrompt("p1", "Old", "old text", "default", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = created
	upd, err := UpdateSavedPrompt("p1", "New", "new text", "thinking", "")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if upd.Name != "New" || upd.Prompt != "new text" || upd.AgentKind != "thinking" {
		t.Errorf("update did not persist: %+v", upd)
	}
}
