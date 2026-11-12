// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodeauth"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodev2"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/providers"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/telemetry"
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
		// The model list holds only providers that already passed this check,
		// so a provider just signed in to has none there yet. Ask opencode for
		// its models directly; otherwise it could never pass, and so never get
		// models.
		model = pickCheapestModel(providerID, probeModelDiscovery(providerID))
	}
	if model == "" {
		// Nothing to probe with. Reported as an error rather than silently
		// passing: a provider offering no models cannot serve a turn either.
		return fmt.Errorf("no model is available to test this provider")
	}
	cmd := opencodev2.Cmd(ctx, config.OpencodeBin,
		"run", "--agent", "title", "--model", model, "--format", "json", "hi")
	cmd.Dir = config.Workspace
	started := time.Now()
	out, err := cmd.CombinedOutput()
	if opencodev2.Enabled() {
		telemetry.RecordRun(telemetry.CallInfo{Model: model, QuerySource: "provider_check", Started: started}, out)
	}
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
			Type string `json:"type"`
			// 1.x: error.name and error.data.message. 2.x: error.type and
			// error.message.
			Error *struct {
				Name    string `json:"name"`
				Type    string `json:"type"`
				Message string `json:"message"`
				Data    struct {
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
		if evt.Error != nil {
			if msg := firstNonEmpty(evt.Error.Data.Message, evt.Error.Message, evt.Error.Name, evt.Error.Type); msg != "" {
				return msg
			}
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
	return pickCheapestModel(providerID, list)
}

// probeModelDiscovery lists a provider's models straight from opencode, as
// model discovery does. Indirected for tests.
var probeModelDiscovery = func(providerID string) []string {
	fetch := providers.ModelDiscovery(providerID)
	if fetch == nil {
		fetch = func() ([]string, error) { return opencodeauth.FetchModelsFor(providerID) }
	}
	list, err := fetch()
	if err != nil {
		return nil
	}
	return list
}

func pickCheapestModel(providerID string, list []string) string {
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
	// ChatGPT's subscription catalogue can list models that are not enabled
	// for every plan. Prefer the small fast family that the web chat uses, so a
	// single unavailable catalogue entry does not mark a working account as
	// unreachable.
	if providerID == opencodeauth.ChatGPTProvider {
		for _, family := range []string{"gpt-6-luna-fast", "luna-fast", "fast", "luna"} {
			for _, m := range owned {
				if strings.Contains(strings.ToLower(m), family) {
					return m
				}
			}
		}
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
// to know to act. It calls the address and headers opencode is configured with,
// which an AI gateway in front of Vertex replaces.
func probeVertex(ctx context.Context, id string) error {
	base, headers := providers.RequestConfig(id)
	model := strings.TrimPrefix(cheapestModelFor(id), id+"/")
	if !strings.HasPrefix(model, "claude-") {
		model = ""
	}
	return vertexauth.CheckReachable(ctx, vertexauth.Target{BaseURL: base, Headers: headers, Model: model})
}

// installProviderProbes wires the probes and the sign-out actions, and arranges
// for a verdict change to rebuild the model list.
func installProviderProbes() {
	providers.SetDefaultProbe(probeProvider)
	providers.SetProbe(vertexauth.Provider, probeVertex)

	providers.SetSignOut(vertexauth.Provider, vertexauth.SignOut)
	providers.SetSignOut(opencodeauth.Provider, opencodeauth.SignOut)
	providers.SetSignOut(opencodeauth.ChatGPTProvider, opencodeauth.SignOutChatGPT)

	// A provider that has just become (un)reachable must stop or resume backing
	// the presets, which only happens on a refresh.
	providers.SetReachChangeHook(refreshOpencodeModels)
}
