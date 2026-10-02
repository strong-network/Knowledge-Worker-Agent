// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package titler

import (
	"context"
	"log"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// Maybe generates a title for the session in the background when this is the
// session's first turn, and stores it as the session label. publish, when
// non-nil, is called with the new title so it can be pushed to a connected
// browser; it is called only after the label has been persisted.
//
// It returns immediately. Every failure path is silent: the session simply
// keeps the label it already had (the client-side truncation of the prompt).
//
// Callers pass the first prompt of the turn. Titling is skipped when:
//   - it has been disabled (KWA_AUTO_TITLE)
//   - the label was set deliberately, by a rename or by a feature that names
//     its own sessions (SessionConfig.LabelManual)
//   - this is not the first turn — a title describes what a chat was opened
//     for, and re-titling later would move entries around the sidebar
//   - no model resolves, e.g. on a fresh machine before model discovery has
//     run. Titling with no model is not possible, and guessing one is worse
//     than leaving the truncated label in place.
func Maybe(sessionID, prompt string, cfg config.SessionConfig, publish func(string)) {
	model, ok := shouldTitle(sessionID, prompt, cfg)
	if !ok {
		return
	}
	go run(sessionID, prompt, model, publish)
}

// shouldTitle reports whether this turn should produce a title, and the model
// to produce it with.
func shouldTitle(sessionID, prompt string, cfg config.SessionConfig) (string, bool) {
	if !Enabled() || cfg.LabelManual || strings.TrimSpace(prompt) == "" {
		return "", false
	}
	// The user's message for this turn is persisted before the turn starts, so
	// the first turn is the one where it is the only message.
	if db.CountMessages(sessionID) > 1 {
		return "", false
	}
	// ResolveModel gives the session's own model when it has one and is
	// runnable, otherwise the default for the provider the user is actually
	// signed in to — so a title is produced by the same backend as the chat.
	model := config.ResolveModel(cfg.Model)
	if model == "" {
		return "", false
	}
	// Prefer the workspace's "small" nomination when its providers make one.
	// A title is one line of text generated from the first message;
	// producing it with whatever reasoning model the user picked for the
	// conversation costs most of a real turn for something nobody asked for.
	// Only honoured when the nominated model is actually runnable, so a stale
	// nomination degrades to the session's model rather than breaking titling.
	if small := config.PresetModel(config.PresetSmall); small != "" && config.ModelRunnable(small) {
		model = small
	}
	return model, true
}

func run(sessionID, prompt, model string, publish func(string)) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	title, err := Generate(ctx, config.OpencodeBin, model, prompt)
	if err != nil {
		log.Printf("[TITLE] session=%s could not generate a title: %v", sessionID, err)
		return
	}

	// Re-read the config rather than reusing the caller's copy: the turn has
	// been running while the title was generated, and the user may have
	// renamed the session in the meantime. A rename always wins.
	cur, cfgErr := db.GetSessionConfig(sessionID)
	if cfgErr != nil {
		return
	}
	if cur.LabelManual {
		log.Printf("[TITLE] session=%s renamed while titling, keeping %q", sessionID, cur.Label)
		return
	}
	cur.Label = title
	if err := db.UpdateSessionConfig(sessionID, cur); err != nil {
		log.Printf("[TITLE] session=%s could not store title: %v", sessionID, err)
		return
	}
	log.Printf("[TITLE] session=%s titled %q", sessionID, title)
	if publish != nil {
		publish(title)
	}
}
