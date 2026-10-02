// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import "testing"

// resetPresets clears the nominations so one test cannot leak into the next —
// the store is package-global, mirroring the single per-process registry.
func resetPresets(t *testing.T) {
	t.Helper()
	SetPresetModels(nil)
	t.Cleanup(func() { SetPresetModels(nil) })
}

func TestSetPresetModels(t *testing.T) {
	resetPresets(t)

	SetPresetModels(map[string]string{
		"  Default ": " mistral/small ",
		"thinking":   "mistral/large",
		"":           "ignored",
		"blank":      "   ",
	})

	if got := PresetModel(PresetDefault); got != "mistral/small" {
		t.Errorf("default = %q, want mistral/small", got)
	}
	// Lookups normalise too, so a caller passing "Default" gets the same answer.
	if got := PresetModel("DEFAULT"); got != "mistral/small" {
		t.Errorf("DEFAULT = %q, want mistral/small", got)
	}
	if got := PresetModel(PresetThinking); got != "mistral/large" {
		t.Errorf("thinking = %q, want mistral/large", got)
	}
	if got := PresetModel("blank"); got != "" {
		t.Errorf("blank nomination = %q, want empty", got)
	}
	if got := PresetModel("nonesuch"); got != "" {
		t.Errorf("unknown preset = %q, want empty", got)
	}
}

func TestSetPresetModelsClears(t *testing.T) {
	resetPresets(t)
	SetPresetModels(map[string]string{"default": "mistral/small"})
	// Signing out of the nominating provider must drop the nomination, not
	// leave the composer pinned to a model it can no longer run.
	SetPresetModels(nil)
	if got := PresetModel(PresetDefault); got != "" {
		t.Errorf("default = %q after clearing, want empty", got)
	}
}

func TestPresetModelsReturnsACopy(t *testing.T) {
	resetPresets(t)
	SetPresetModels(map[string]string{"default": "mistral/small"})

	got := PresetModels()
	if got["default"] != "mistral/small" {
		t.Fatalf("got %v", got)
	}
	// Served to the browser on every /api/providers request, so a caller that
	// mutates the result must not corrupt what the next request sees.
	got["default"] = "tampered"
	if PresetModel(PresetDefault) != "mistral/small" {
		t.Error("the published map is aliased to the caller's copy")
	}
}

func TestModelRunnable(t *testing.T) {
	SetAvailableModels([]string{"mistral/small", "Github-Copilot/Claude-Sonnet-5"})
	t.Cleanup(func() { SetAvailableModels(nil) })

	cases := map[string]bool{
		"mistral/small":                  true,
		"MISTRAL/SMALL":                  true, // matched case-insensitively
		"github-copilot/claude-sonnet-5": true,
		"claude-sonnet-5":                true, // legacy bare id resolves via suffix
		"mistral/gone":                   false,
		"":                               true, // no pin: the server default is used
	}
	for model, want := range cases {
		if got := ModelRunnable(model); got != want {
			t.Errorf("ModelRunnable(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestModelRunnableWithUnknownModelList(t *testing.T) {
	// An empty available list means "discovery has not run", not "nothing is
	// available". Reporting false here would fail every scheduled task in the
	// workspace on a transient discovery error.
	SetAvailableModels(nil)
	if !ModelRunnable("anything/at-all") {
		t.Error("ModelRunnable = false while the model list was unknown")
	}
}

func TestPickDefaultModelHonoursNomination(t *testing.T) {
	resetPresets(t)
	models := []string{"github-copilot/claude-sonnet-5", "mistral/small", "mistral/large"}

	// Without a nomination the Claude heuristic decides, exactly as before.
	if got := pickDefaultModel(models); got != "github-copilot/claude-sonnet-5" {
		t.Fatalf("got %q, want the heuristic's Sonnet pin", got)
	}

	SetPresetModels(map[string]string{PresetDefault: "mistral/large"})
	if got := pickDefaultModel(models); got != "mistral/large" {
		t.Errorf("got %q, want the admin nomination to win", got)
	}
}

func TestPickDefaultModelIgnoresUndiscoveredNomination(t *testing.T) {
	// A nomination naming a model that is not in the discovered list is a typo
	// or a retired model. Pinning it would launch every session against a dead
	// id; falling through to the heuristic keeps the workspace working.
	resetPresets(t)
	models := []string{"github-copilot/claude-sonnet-5"}
	SetPresetModels(map[string]string{PresetDefault: "mistral/gone"})

	if got := pickDefaultModel(models); got != "github-copilot/claude-sonnet-5" {
		t.Errorf("got %q, want the heuristic fallback", got)
	}
}

func TestPickDefaultModelNominationIsCaseInsensitive(t *testing.T) {
	resetPresets(t)
	models := []string{"Mistral/Small"}
	SetPresetModels(map[string]string{PresetDefault: "mistral/small"})

	// The discovered spelling is returned, not the nominated one, so the id
	// handed to opencode matches its catalog exactly.
	if got := pickDefaultModel(models); got != "Mistral/Small" {
		t.Errorf("got %q, want the discovered spelling", got)
	}
}

func TestPickDefaultModelEmptyList(t *testing.T) {
	resetPresets(t)
	SetPresetModels(map[string]string{PresetDefault: "mistral/small"})
	if got := pickDefaultModel(nil); got != "" {
		t.Errorf("got %q, want empty — a nomination cannot conjure a model", got)
	}
}

// --- SmallModel: the bound on background generation ---------------------

// The nomination wins when the workspace makes one. An admin who names a model
// for the `small` slot has chosen what background work runs on.
func TestSmallModelPrefersTheNomination(t *testing.T) {
	resetPresets(t)
	SetAvailableModels([]string{"vendor/cheap-1", "vendor/expensive-5"})
	t.Cleanup(func() { SetAvailableModels(nil) })
	SetPresetModels(map[string]string{PresetSmall: "vendor/cheap-1"})

	if got := SmallModel(); got != "vendor/cheap-1" {
		t.Errorf("SmallModel() = %q, want the nominated small model", got)
	}
}

// Built-in providers ship no nominations at all, so without a fallback notes
// would never run outside central-config workspaces. Haiku is right there.
func TestSmallModelFallsBackToHaiku(t *testing.T) {
	resetPresets(t)
	SetAvailableModels([]string{
		"github-copilot/claude-sonnet-5",
		"github-copilot/claude-haiku-4.5",
		"github-copilot/claude-opus-5",
	})
	t.Cleanup(func() { SetAvailableModels(nil) })

	if got := SmallModel(); got != "github-copilot/claude-haiku-4.5" {
		t.Errorf("SmallModel() = %q, want the haiku in the discovered list", got)
	}
}

// The point of the whole function: when nothing cheap resolves it returns ""
// so the caller can decline to run. Falling through to the workspace default
// would mean an unrequested background sweep billing frontier rates.
func TestSmallModelRefusesToNameAnExpensiveModel(t *testing.T) {
	resetPresets(t)
	SetAvailableModels([]string{
		"github-copilot/claude-sonnet-5",
		"github-copilot/claude-opus-5",
	})
	t.Cleanup(func() { SetAvailableModels(nil) })
	SetDefaultModelForProvider([]string{"github-copilot/claude-opus-5"}, "")

	if got := SmallModel(); got != "" {
		t.Errorf("SmallModel() = %q, want \"\" rather than an expensive fallback", got)
	}
}

// A nomination naming a model the workspace cannot actually run is stale. It
// must not be returned, but it also must not suppress the haiku fallback.
func TestSmallModelIgnoresAnUnrunnableNomination(t *testing.T) {
	resetPresets(t)
	SetAvailableModels([]string{"github-copilot/claude-haiku-4.5"})
	t.Cleanup(func() { SetAvailableModels(nil) })
	SetPresetModels(map[string]string{PresetSmall: "retired/model-9"})

	if got := SmallModel(); got != "github-copilot/claude-haiku-4.5" {
		t.Errorf("SmallModel() = %q, want the stale nomination skipped for the haiku", got)
	}
}

// Knowledge Worker Agent offers several model providers and Anthropic is only one of them. A
// workspace with no Claude at all still has a cheap tier; before this it
// resolved nothing and every caller declined to run.
func TestSmallModelFindsEachVendorsCheapTier(t *testing.T) {
	cases := []struct {
		name   string
		models []string
		want   string
	}{
		{
			name:   "openai nano",
			models: []string{"github-copilot/gpt-5.4", "github-copilot/gpt-5.4-nano"},
			want:   "github-copilot/gpt-5.4-nano",
		},
		{
			name:   "openai mini when there is no nano",
			models: []string{"github-copilot/gpt-5.4", "github-copilot/gpt-5.4-mini"},
			want:   "github-copilot/gpt-5.4-mini",
		},
		{
			name:   "google flash-lite before plain flash",
			models: []string{"google-vertex/gemini-3.8-flash", "google-vertex/gemini-3.1-flash-lite"},
			want:   "google-vertex/gemini-3.1-flash-lite",
		},
		{
			name:   "google flash when there is no lite",
			models: []string{"google-vertex/gemini-2.5-pro", "google-vertex/gemini-3.8-flash"},
			want:   "google-vertex/gemini-3.8-flash",
		},
		{
			name:   "mistral small",
			models: []string{"mistral/mistral-large-3", "mistral/mistral-small-3.2"},
			want:   "mistral/mistral-small-3.2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetPresets(t)
			SetAvailableModels(tc.models)
			t.Cleanup(func() { SetAvailableModels(nil) })

			if got := SmallModel(); got != tc.want {
				t.Errorf("SmallModel() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The whole reason family matching is on words rather than substrings:
// "gemini" contains "mini". Under a substring test every Gemini model joins the
// mini tier, and versionScore then ranks gemini-3.1-pro at the top of it — so
// the guard against expensive background sweeps would hand one a Pro model.
func TestSmallModelDoesNotMistakeGeminiProForAMiniModel(t *testing.T) {
	resetPresets(t)
	SetAvailableModels([]string{
		"google-vertex/gemini-3.1-pro-preview",
		"google-vertex/gemini-2.5-pro",
	})
	t.Cleanup(func() { SetAvailableModels(nil) })

	if got := SmallModel(); got != "" {
		t.Errorf("SmallModel() = %q, want \"\" — a Pro model is not a small model", got)
	}
}

// Guards the guard above: the same list plus a genuine cheap tier must still
// resolve, so the test passes for the right reason rather than because nothing
// ever matches.
func TestSmallModelStillResolvesAlongsideGeminiPro(t *testing.T) {
	resetPresets(t)
	SetAvailableModels([]string{
		"google-vertex/gemini-3.1-pro-preview",
		"google-vertex/gemini-3.1-flash-lite",
	})
	t.Cleanup(func() { SetAvailableModels(nil) })

	if got := SmallModel(); got != "google-vertex/gemini-3.1-flash-lite" {
		t.Errorf("SmallModel() = %q, want the flash-lite", got)
	}
}

// Ordering is a promise, not an accident: a workspace resolving haiku today
// keeps resolving haiku after this change rather than silently switching tier.
func TestSmallModelKeepsPreferringHaikuWhenSeveralTiersExist(t *testing.T) {
	resetPresets(t)
	SetAvailableModels([]string{
		"github-copilot/gpt-5.4-nano",
		"github-copilot/gemini-3.8-flash",
		"github-copilot/claude-haiku-4.5",
	})
	t.Cleanup(func() { SetAvailableModels(nil) })

	if got := SmallModel(); got != "github-copilot/claude-haiku-4.5" {
		t.Errorf("SmallModel() = %q, want the haiku to stay first", got)
	}
}

// Within a tier the newest wins, so a retired model does not pin the choice.
func TestSmallModelPicksTheNewestInItsTier(t *testing.T) {
	resetPresets(t)
	SetAvailableModels([]string{
		"github-copilot/claude-haiku-3.5",
		"github-copilot/claude-haiku-4.5",
	})
	t.Cleanup(func() { SetAvailableModels(nil) })

	if got := SmallModel(); got != "github-copilot/claude-haiku-4.5" {
		t.Errorf("SmallModel() = %q, want the newest haiku", got)
	}
}
