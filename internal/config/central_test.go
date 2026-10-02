// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env/envtest"
)

func TestCentralConfigEnabled(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"true", true},
		{"True", true},
		{"TRUE", true},
		{"1", true},
		{"yes", true},
		{"on", true},
		{" On ", true},
		{"  true  ", true},
		{"", false},
		{"false", false},
		{"0", false},
		{"off", false},
		{"no", false},
		// Anything unrecognised must fall back to the un-managed behaviour
		// rather than guessing. Getting this backwards would, on a typo, strip a
		// workspace of its agents and skills, retire its MCP catalogue and swap
		// its model providers all at once.
		{"maybe", false},
		{"truthy", false},
	}
	envtest.Clear(t, EnvCentralConfig)
	for _, c := range cases {
		t.Run(c.value, func(t *testing.T) {
			t.Setenv(EnvCentralConfig, c.value)
			if got := CentralConfigEnabled(); got != c.want {
				t.Errorf("CentralConfigEnabled() with %s=%q = %v, want %v", EnvCentralConfig, c.value, got, c.want)
			}
		})
	}
}

func TestCentralConfigEnabled_UnsetIsOff(t *testing.T) {
	envtest.Clear(t, EnvCentralConfig)
	if CentralConfigEnabled() {
		t.Errorf("expected central management to be off when %s is unset", EnvCentralConfig)
	}
}

// The three surfaces central management covers must move together. Reading one
// switch is what guarantees that; these are the variables that used to gate the
// model-provider and MCP surfaces separately, and nothing may read them again.
func TestObsoleteCentralSwitchesAreNotRead(t *testing.T) {
	envtest.Clear(t, EnvCentralConfig)
	for _, obsolete := range []string{"SDS_CENTRAL_MODELS", "SDS_CENTRAL_MCP"} {
		t.Run(obsolete, func(t *testing.T) {
			t.Setenv(obsolete, "true")
			if CentralConfigEnabled() {
				t.Errorf("%s must not enable central management on its own", obsolete)
			}
		})
	}
}

// The platform sets the old name until it switches to the new one, which wins
// once both are set.
func TestCentralConfigReadsTheOldName(t *testing.T) {
	envtest.Clear(t, EnvCentralConfig)
	t.Setenv("SDS_CENTRAL_CONFIG", "True")
	if !CentralConfigEnabled() {
		t.Error("SDS_CENTRAL_CONFIG=True alone must enable central management")
	}
	t.Setenv(EnvCentralConfig, "false")
	if CentralConfigEnabled() {
		t.Errorf("%s=false must win over SDS_CENTRAL_CONFIG", EnvCentralConfig)
	}
}
