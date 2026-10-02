// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodeauth"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/providers"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/vertexauth"
)

// Reachability probes. A provider is "authenticated" when we hold a credential
// for it, which says nothing about whether this workspace can actually talk to
// it — an organization's egress policy can refuse a provider whose credentials
// are perfectly valid. Everything here exists to tell those two states apart
// before the user sends a prompt, rather than after.

// probeProvider is the fallback probe, used for any provider without a
// specialised one: run the smallest possible real turn through opencode.
//
// It is deliberately a real turn. Listing a provider's catalogue does not touch
// its inference endpoint, which is exactly how an unusable provider passed for
// usable; only actually asking it something proves the whole path. The `title`
// agent is used because it carries opencode's smallest prompt and no tools, so
// the cost is a few tokens per probe cycle.
func probeProvider(ctx context.Context, providerID string) error {
	model := cheapestModelFor(providerID)
	if model == "" {
		// Nothing to probe with. Reported as an error rather than silently
		// passing: a provider offering no models cannot serve a turn either.
		return fmt.Errorf("no model is available to test this provider")
	}
	cmd := exec.CommandContext(ctx, config.OpencodeBin,
		"run", "--agent", "title", "--model", model, "--format", "json", "hi")
	cmd.Dir = config.Workspace
	out, err := cmd.CombinedOutput()
	if msg := probeErrorFrom(out); msg != "" {
		return fmt.Errorf("%s", msg)
	}
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("timed out after %s", probeTimeoutLabel)
		}
		return fmt.Errorf("could not run a test request: %v", err)
	}
	return nil
}

const probeTimeoutLabel = "45s"

// probeErrorFrom pulls the provider's own message out of opencode's JSON run
// output. Returns "" when the run reported no error.
func probeErrorFrom(out []byte) string {
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var evt struct {
			Type  string `json:"type"`
			Error *struct {
				Name string `json:"name"`
				Data struct {
					Message string `json:"message"`
				} `json:"data"`
			} `json:"error"`
			Name string `json:"name"`
			Data struct {
				Message string `json:"message"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(line), &evt) != nil {
			continue
		}
		if evt.Error != nil && (evt.Error.Data.Message != "" || evt.Error.Name != "") {
			return firstNonEmpty(evt.Error.Data.Message, evt.Error.Name)
		}
		if evt.Name != "" && strings.Contains(strings.ToLower(evt.Name), "error") {
			return firstNonEmpty(evt.Data.Message, evt.Name)
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// cheapestModelFor picks the least expensive discovered model for a provider,
// preferring the small/fast families every vendor names similarly.
func cheapestModelFor(providerID string) string {
	opencodeModelsMu.RLock()
	list := make([]string, len(opencodeModels))
	copy(list, opencodeModels)
	opencodeModelsMu.RUnlock()

	prefix := providerID + "/"
	var owned []string
	for _, m := range list {
		if strings.HasPrefix(m, prefix) {
			owned = append(owned, m)
		}
	}
	if len(owned) == 0 {
		return ""
	}
	for _, cheap := range []string{"haiku", "mini", "flash", "small", "lite"} {
		for _, m := range owned {
			if strings.Contains(strings.ToLower(m), cheap) {
				return m
			}
		}
	}
	return owned[0]
}

// probeVertex tests Google Vertex against its own API instead of through
// opencode, because opencode reports every upstream failure as a generic
// "UnknownError" — which cannot tell a user that their organization's VPC
// Service Controls perimeter is refusing the request, the one thing they need
// to know to act.
func probeVertex(ctx context.Context, _ string) error {
	return vertexauth.CheckReachable(ctx)
}

// installProviderProbes wires the probes and the sign-out actions, and arranges
// for a verdict change to rebuild the model list.
func installProviderProbes() {
	providers.SetDefaultProbe(probeProvider)
	providers.SetProbe(vertexauth.Provider, probeVertex)

	providers.SetSignOut(vertexauth.Provider, vertexauth.SignOut)
	providers.SetSignOut(opencodeauth.Provider, opencodeauth.SignOut)

	// A provider that has just become (un)reachable must stop or resume backing
	// the presets, which only happens on a refresh.
	providers.SetReachChangeHook(refreshOpencodeModels)
}
