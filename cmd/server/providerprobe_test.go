// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"strings"
	"testing"
)

func TestProbeErrorFromExtractsProviderMessage(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{
			name: "opencode error event",
			out: `{"type":"start"}
{"type":"error","error":{"name":"UnknownError","data":{"message":"Unexpected server error."}}}`,
			want: "Unexpected server error.",
		},
		{
			// The message is the actionable half; a bare name is better than nothing.
			name: "name only",
			out:  `{"type":"error","error":{"name":"ProviderAuthError","data":{}}}`,
			want: "ProviderAuthError",
		},
		{
			name: "successful run reports nothing",
			out: `{"type":"start"}
{"type":"text","text":"hi"}`,
			want: "",
		},
		{
			// opencode prints human-readable warnings on the same stream.
			name: "non-json lines are ignored",
			out: `warning: something
{"type":"text","text":"hi"}`,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := probeErrorFrom([]byte(tc.out)); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCheapestModelForPrefersSmallFamilies(t *testing.T) {
	opencodeModelsMu.Lock()
	prev := opencodeModels
	opencodeModels = []string{
		"github-copilot/claude-opus-5",
		"github-copilot/claude-haiku-4.5",
		"google-vertex/claude-opus-5@default",
	}
	opencodeModelsMu.Unlock()
	t.Cleanup(func() {
		opencodeModelsMu.Lock()
		opencodeModels = prev
		opencodeModelsMu.Unlock()
	})

	if got := cheapestModelFor("github-copilot"); got != "github-copilot/claude-haiku-4.5" {
		t.Fatalf("expected the haiku model, got %q", got)
	}
	// No small family available: any model of that provider still beats none.
	if got := cheapestModelFor("google-vertex"); got != "google-vertex/claude-opus-5@default" {
		t.Fatalf("expected the only vertex model, got %q", got)
	}
	if got := cheapestModelFor("nobody"); got != "" {
		t.Fatalf("expected no model for an unknown provider, got %q", got)
	}
}

// A provider with no discovered models cannot serve a turn, so the probe must
// fail rather than pass vacuously.
func TestProbeProviderFailsWithNoModels(t *testing.T) {
	opencodeModelsMu.Lock()
	prev := opencodeModels
	opencodeModels = []string{"github-copilot/claude-opus-5"}
	opencodeModelsMu.Unlock()
	t.Cleanup(func() {
		opencodeModelsMu.Lock()
		opencodeModels = prev
		opencodeModelsMu.Unlock()
	})

	err := probeProvider(context.Background(), "some-provider")
	if err == nil || !strings.Contains(err.Error(), "no model") {
		t.Fatalf("expected a no-model error, got %v", err)
	}
}
