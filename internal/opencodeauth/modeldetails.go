// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeauth

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodev2"
)

// ModelDetail is what opencode's model list says about one model.
type ModelDetail struct {
	// ID is "provider/model", as FetchModelsFor returns it; Model is the part
	// after the provider.
	ID    string
	Model string
	// NPM is the client package opencode talks to the model with, which says
	// which API it speaks; URL is the package's address template, when the
	// catalogue sets one.
	NPM    string
	URL    string
	Status string
	// ToolCall and the Output fields are the model's capabilities.
	ToolCall    bool
	OutputText  bool
	OutputImage bool
	OutputAudio bool
}

// FetchModelDetails lists a provider's models with their catalogue metadata,
// from the server's model list.
func FetchModelDetails(provider string) ([]ModelDetail, error) {
	ctx, cancel := context.WithTimeout(context.Background(), modelsTimeout)
	defer cancel()
	var raw json.RawMessage
	if err := opencodev2.Get(ctx, "/api/model", &raw); err != nil {
		return nil, err
	}
	return modelDetailsV2(raw, provider), nil
}

// v2Packages names opencode's model clients by the AI SDK package that speaks
// the same API, which is what NPM is read for.
var v2Packages = map[string]string{
	"@opencode/ai/providers/google-vertex/gemini":   "@ai-sdk/google-vertex",
	"@opencode/ai/providers/google-vertex/messages": "@ai-sdk/google-vertex/anthropic",
	"@opencode/ai/providers/google-vertex/chat":     "@ai-sdk/openai-compatible",
}

// modelDetailsV2 reads a provider's models out of 2.x's model list. URL is
// the model's address with the project and location already filled in.
func modelDetailsV2(raw []byte, provider string) []ModelDetail {
	var resp struct {
		Data []struct {
			ID         string `json:"id"`
			ProviderID string `json:"providerID"`
			Package    string `json:"package"`
			Status     string `json:"status"`
			Settings   struct {
				BaseURL string `json:"baseURL"`
			} `json:"settings"`
			Capabilities struct {
				Tools  bool     `json:"tools"`
				Output []string `json:"output"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &resp) != nil {
		return nil
	}
	var out []ModelDetail
	for _, m := range resp.Data {
		if m.ProviderID != provider || m.ID == "" {
			continue
		}
		npm := strings.TrimPrefix(m.Package, "aisdk:")
		if v1, ok := v2Packages[npm]; ok {
			npm = v1
		}
		d := ModelDetail{
			ID: m.ProviderID + "/" + m.ID, Model: m.ID, NPM: npm, URL: m.Settings.BaseURL, Status: m.Status,
			ToolCall: m.Capabilities.Tools,
		}
		for _, o := range m.Capabilities.Output {
			switch o {
			case "text":
				d.OutputText = true
			case "image":
				d.OutputImage = true
			case "audio":
				d.OutputAudio = true
			}
		}
		out = append(out, d)
	}
	return out
}
