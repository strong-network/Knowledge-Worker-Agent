// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package providers

import (
	"slices"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/telemetry"
)

func TestApplyTelemetry(t *testing.T) {
	t.Cleanup(func() {
		SetTelemetryUnavailable("")
		telemetry.Shutdown()
	})
	centralDir(t, `{"provider":{"gw":{},"broken":{}}}`, `{"providers":[
		{"id":"gw","telemetry":{"endpoint":"https://otel.example.com","logUserPrompts":true,"logToolDetails":true}},
		{"id":"broken","telemetry":{"endpoint":"http://otel.example.com"}},
		{"id":"gone","telemetry":{"endpoint":"https://otel.example.com"}}]}`)

	ApplyTelemetry()

	gw := telemetryFor("gw")
	if gw == nil || gw.Endpoint != "https://otel.example.com" || gw.Error != "" ||
		!slices.Equal(gw.Content, []string{"prompts", "tool details"}) {
		t.Errorf("gw = %+v", gw)
	}
	if telemetry.For("gw") == nil {
		t.Error("gw has no exporter")
	}
	if b := telemetryFor("broken"); b == nil || b.Error == "" || telemetry.For("broken") != nil {
		t.Errorf("an invalid block is not reported as one: %+v", b)
	}
	if telemetryFor("gone") != nil || telemetry.For("gone") != nil {
		t.Error("a provider the configuration doesn't declare has telemetry")
	}

	SetTelemetryUnavailable("usage reporting needs opencode 2.x")
	ApplyTelemetry()
	if gw := telemetryFor("gw"); gw == nil || gw.Error == "" || telemetry.For("gw") != nil {
		t.Errorf("telemetry still on without opencode 2.x: %+v", gw)
	}
}
