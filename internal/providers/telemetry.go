// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package providers

import (
	"encoding/json"
	"log"
	"path/filepath"
	"sync"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/telemetry"
)

// TelemetryState is a provider's kwa.telemetry, as Accounts and Diagnostics
// show it.
type TelemetryState struct {
	// Endpoint is the collector's address; Content lists what besides usage is
	// sent ("prompts", "replies", "tool details", "tool content").
	Endpoint string   `json:"endpoint,omitempty"`
	Content  []string `json:"content,omitempty"`
	// Error is set when the block is invalid and telemetry is off for it.
	Error    string   `json:"error,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

var (
	telemetryMu    sync.Mutex
	telemetryState = map[string]TelemetryState{}
	// telemetryOff is why telemetry can't run at all (opencode 1.x), or "".
	telemetryOff string
)

// SetTelemetryUnavailable turns telemetry off for every provider, with the
// reason Diagnostics shows. Telemetry reads opencode 2.x's turn events.
func SetTelemetryUnavailable(reason string) {
	telemetryMu.Lock()
	telemetryOff = reason
	telemetryMu.Unlock()
}

// ApplyTelemetry reads every declared provider's kwa.telemetry, validates it,
// and configures the exporters. Run at startup and after the central
// configuration is refreshed.
func ApplyTelemetry() {
	cfgs := map[string]telemetry.Config{}
	states := map[string]TelemetryState{}
	telemetryMu.Lock()
	off := telemetryOff
	telemetryMu.Unlock()

	if dir := configDirFn(); dir != "" {
		declared, err := declaredProviderIDs(filepath.Join(dir, "opencode.json"))
		if err == nil {
			for _, m := range readProviderMeta(filepath.Join(dir, "provider-meta.json")) {
				if !declared[m.ID] || len(m.Telemetry) == 0 {
					continue
				}
				var raw telemetry.RawConfig
				if err := json.Unmarshal(m.Telemetry, &raw); err != nil {
					states[m.ID] = TelemetryState{Error: "kwa.telemetry isn't valid: " + err.Error()}
					continue
				}
				cfg, warnings, err := telemetry.Parse(raw)
				if err != nil {
					log.Printf("[telemetry] %s: kwa.telemetry ignored: %v", m.ID, err)
					states[m.ID] = TelemetryState{Error: err.Error()}
					continue
				}
				for _, w := range warnings {
					log.Printf("[telemetry] %s: %s", m.ID, w)
				}
				if off != "" {
					states[m.ID] = TelemetryState{Error: off}
					continue
				}
				cfgs[m.ID] = cfg
				states[m.ID] = TelemetryState{Endpoint: cfg.Endpoint, Content: contentOf(cfg), Warnings: warnings}
			}
		}
	}
	if off != "" && len(states) > 0 {
		log.Printf("[telemetry] off: %s", off)
	}
	telemetry.Configure(cfgs)
	telemetryMu.Lock()
	telemetryState = states
	telemetryMu.Unlock()
}

func contentOf(cfg telemetry.Config) []string {
	var out []string
	for _, c := range []struct {
		on   bool
		name string
	}{{cfg.LogUserPrompts, "prompts"}, {cfg.LogAssistantResponses, "replies"},
		{cfg.LogToolDetails, "tool details"}, {cfg.LogToolContent, "tool content"}} {
		if c.on {
			out = append(out, c.name)
		}
	}
	return out
}

func telemetryFor(id string) *TelemetryState {
	telemetryMu.Lock()
	defer telemetryMu.Unlock()
	if s, ok := telemetryState[id]; ok {
		return &s
	}
	return nil
}

// TelemetryStates returns every provider's telemetry state, by provider id.
func TelemetryStates() map[string]TelemetryState {
	telemetryMu.Lock()
	defer telemetryMu.Unlock()
	out := make(map[string]TelemetryState, len(telemetryState))
	for id, s := range telemetryState {
		out[id] = s
	}
	return out
}
