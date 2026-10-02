// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package titler

import (
	"context"
	"os"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

func setupDB(t *testing.T) {
	t.Helper()
	f, err := os.CreateTemp("", "titler-*.db")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	t.Cleanup(func() {
		db.Close()
		os.Remove(path)
	})
	if err := db.Init(path); err != nil {
		t.Fatal(err)
	}
}

// newSession creates a session with a pinned, runnable model so ResolveModel
// returns something without depending on model discovery having run.
func newSession(t *testing.T, id string) config.SessionConfig {
	t.Helper()
	cfg := config.DefaultSessionConfig()
	cfg.Model = "github-copilot/claude-sonnet-4.6"
	config.SetAvailableModels([]string{cfg.Model})
	t.Cleanup(func() { config.SetAvailableModels(nil) })
	if err := db.CreateSession(id, cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestShouldTitle_OnFirstTurn(t *testing.T) {
	setupDB(t)
	cfg := newSession(t, "s1")
	db.AddMessage("s1", "user", "add dark mode toggle")

	model, ok := shouldTitle("s1", "add dark mode toggle", cfg)
	if !ok {
		t.Fatal("the first turn should be titled")
	}
	if model != cfg.Model {
		t.Fatalf("should title with the session's own model, got %q", model)
	}
}

// A title describes what a chat was opened for. Re-titling on later turns
// would keep moving sidebar entries around as a conversation drifts.
func TestShouldTitle_SkipsLaterTurns(t *testing.T) {
	setupDB(t)
	cfg := newSession(t, "s1")
	db.AddMessage("s1", "user", "first")
	db.AddMessage("s1", "assistant", "reply")
	db.AddMessage("s1", "user", "second")

	if _, ok := shouldTitle("s1", "second", cfg); ok {
		t.Fatal("only the first turn should be titled")
	}
}

func TestShouldTitle_SkipsManualLabel(t *testing.T) {
	setupDB(t)
	cfg := newSession(t, "s1")
	cfg.LabelManual = true
	db.AddMessage("s1", "user", "hello")

	if _, ok := shouldTitle("s1", "hello", cfg); ok {
		t.Fatal("a deliberately named session must not be re-titled")
	}
}

func TestShouldTitle_SkipsWhenDisabled(t *testing.T) {
	setupDB(t)
	cfg := newSession(t, "s1")
	db.AddMessage("s1", "user", "hello")
	t.Setenv("KWA_AUTO_TITLE", "off")

	if _, ok := shouldTitle("s1", "hello", cfg); ok {
		t.Fatal("titling should be skipped when disabled")
	}
}

// On a fresh machine, before model discovery has run, there is no model to
// title with. Guessing one is worse than keeping the truncated label.
func TestShouldTitle_SkipsWithNoResolvableModel(t *testing.T) {
	setupDB(t)
	cfg := config.DefaultSessionConfig()
	cfg.Model = ""
	config.SetDefaultModel(nil)
	if err := db.CreateSession("s1", cfg); err != nil {
		t.Fatal(err)
	}
	db.AddMessage("s1", "user", "hello")

	if _, ok := shouldTitle("s1", "hello", cfg); ok {
		t.Fatal("titling should be skipped when no model resolves")
	}
}

func TestRun_StoresTitleAndPublishes(t *testing.T) {
	setupDB(t)
	newSession(t, "s1")
	stubRun(t, `{"type":"text","sessionID":"ses_1","part":{"text":"Dark mode toggle in settings"}}`, nil)

	published := ""
	run("s1", "add a dark mode toggle", "m", func(title string) { published = title })

	got, err := db.GetSessionConfig("s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != "Dark mode toggle in settings" {
		t.Fatalf("label = %q", got.Label)
	}
	if published != "Dark mode toggle in settings" {
		t.Fatalf("published = %q", published)
	}
	// A generated title is not a manual one: a later rename must still win.
	if got.LabelManual {
		t.Fatal("a generated title must not mark the label as manual")
	}
}

// Titling runs concurrently with the turn, so the user can rename the session
// while it is in flight. The rename must win.
func TestRun_DoesNotOverwriteRenameDuringGeneration(t *testing.T) {
	setupDB(t)
	newSession(t, "s1")

	prev := runTitle
	runTitle = func(_ context.Context, _, _, _ string) ([]byte, error) {
		// Simulate the user renaming the session mid-generation.
		cur, _ := db.GetSessionConfig("s1")
		cur.Label = "My own name"
		cur.LabelManual = true
		db.UpdateSessionConfig("s1", cur)
		return []byte(`{"type":"text","part":{"text":"Generated title"}}`), nil
	}
	t.Cleanup(func() { runTitle = prev })

	publishCalls := 0
	run("s1", "some prompt", "m", func(string) { publishCalls++ })

	got, _ := db.GetSessionConfig("s1")
	if got.Label != "My own name" {
		t.Fatalf("rename should win, label = %q", got.Label)
	}
	if publishCalls != 0 {
		t.Fatal("nothing should be published when the title is discarded")
	}
}

// A failed generation must leave the existing label untouched.
func TestRun_KeepsLabelWhenGenerationFails(t *testing.T) {
	setupDB(t)
	newSession(t, "s1")
	before, _ := db.GetSessionConfig("s1")
	stubRun(t, `{"type":"error","error":{"data":{"message":"boom"}}}`, nil)

	run("s1", "some prompt", "m", func(string) { t.Fatal("must not publish on failure") })

	got, _ := db.GetSessionConfig("s1")
	if got.Label != before.Label {
		t.Fatalf("label changed to %q", got.Label)
	}
}

// A title is one line generated from the first message. Producing it with the
// reasoning model the user picked for the conversation costs most of a real
// turn for something nobody asked for, so a workspace that nominates a cheap
// model gets titles on that instead.
func TestShouldTitle_PrefersTheSmallNomination(t *testing.T) {
	setupDB(t)
	cfg := newSession(t, "s1")
	config.SetAvailableModels([]string{cfg.Model, "mistral/small"})
	config.SetPresetModels(map[string]string{config.PresetSmall: "mistral/small"})
	t.Cleanup(func() { config.SetPresetModels(nil) })
	db.AddMessage("s1", "user", "hello")

	model, ok := shouldTitle("s1", "hello", cfg)
	if !ok {
		t.Fatal("the first turn should still be titled")
	}
	if model != "mistral/small" {
		t.Fatalf("model = %q, want the small nomination", model)
	}
}

// A nomination naming a model this workspace cannot run — a typo, or one
// retired since the config was written — must not take titling down with it.
func TestShouldTitle_IgnoresAnUnrunnableSmallNomination(t *testing.T) {
	setupDB(t)
	cfg := newSession(t, "s1")
	config.SetPresetModels(map[string]string{config.PresetSmall: "mistral/gone"})
	t.Cleanup(func() { config.SetPresetModels(nil) })
	db.AddMessage("s1", "user", "hello")

	model, ok := shouldTitle("s1", "hello", cfg)
	if !ok {
		t.Fatal("a stale nomination must not disable titling")
	}
	if model != cfg.Model {
		t.Fatalf("model = %q, want a fallback to the session's model", model)
	}
}
