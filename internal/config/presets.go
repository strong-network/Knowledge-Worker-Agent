// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"sync"
)

// Preset names. The composer offers two one-click choices — "Default" for
// everyday work and "Thinking" for harder problems — and each resolves to a
// concrete model id.
//
// PresetSmall is not offered in the composer. It names the model used for the
// short, mechanical, server-initiated generations the user never asked for —
// today, chat titles. Those run on the session's own model, so titling a chat
// costs a call to whatever the user picked for the conversation itself, which
// on a reasoning model is most of the price of a real turn for one line of
// text. It mirrors opencode's own `small_model` config key.
const (
	PresetDefault  = "default"
	PresetThinking = "thinking"
	PresetSmall    = "small"
)

// presetModels holds the model each preset resolves to, as nominated by the
// providers the workspace is offering.
//
// Before this, both presets were string-matched against Claude family names
// ("sonnet", "opus"). That works only because every provider shipped so far is
// a Claude vendor: on a provider with no Sonnet and no Opus the match fails and
// BOTH presets resolve to nothing, leaving the composer with no model to send.
// A provider artifact nominates its own models instead, and the heuristic stays
// as the fallback for the built-in providers.
var (
	presetModelsMu sync.RWMutex
	presetModels   map[string]string
)

// SetPresetModels records the preset nominations for the active provider set.
// An empty or nil map means "no nominations" — resolution falls back to the
// Claude heuristic, which is what always happens in built-in mode. Safe for
// concurrent use.
func SetPresetModels(m map[string]string) {
	cleaned := make(map[string]string, len(m))
	for name, model := range m {
		name = strings.ToLower(strings.TrimSpace(name))
		model = strings.TrimSpace(model)
		if name != "" && model != "" {
			cleaned[name] = model
		}
	}
	presetModelsMu.Lock()
	presetModels = cleaned
	presetModelsMu.Unlock()
}

// PresetModel returns the model nominated for a preset, or "" when none is.
func PresetModel(name string) string {
	presetModelsMu.RLock()
	defer presetModelsMu.RUnlock()
	return presetModels[strings.ToLower(strings.TrimSpace(name))]
}

// PresetModels returns a copy of every nomination currently published.
//
// The browser needs these because the composer decides what "Default" means for
// the chip label and for the model it arms. Resolving that a second time in
// TypeScript would put two implementations of the first-wins/skip-unavailable
// rules in the codebase, and the moment they disagree the composer shows one
// model and the turn runs another. Serving the resolved answer keeps the rules
// in one place; the frontend only keeps the Claude heuristic, which it needs
// anyway for built-in mode.
func PresetModels() map[string]string {
	presetModelsMu.RLock()
	defer presetModelsMu.RUnlock()
	out := make(map[string]string, len(presetModels))
	for k, v := range presetModels {
		out[k] = v
	}
	return out
}
