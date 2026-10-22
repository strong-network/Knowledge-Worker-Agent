// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeauth

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
)

// ModelDetail is what `opencode models --verbose` reports about one model.
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
// by running `opencode models <provider> --verbose`.
func FetchModelDetails(provider string) ([]ModelDetail, error) {
	ctx, cancel := context.WithTimeout(context.Background(), modelsTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, resolveBin(), "models", provider, "--verbose").Output()
	if err != nil {
		return nil, describeFailure(ctx, err, string(out))
	}
	return parseModelDetails(string(out)), nil
}

// parseModelDetails reads the verbose listing: each model is its
// "provider/model" line followed by an indented JSON object whose closing
// brace starts a line.
func parseModelDetails(raw string) []ModelDetail {
	var out []ModelDetail
	id := ""
	var block []string
	for _, line := range strings.Split(ansiRe.ReplaceAllString(raw, ""), "\n") {
		switch {
		case block == nil && line == "{":
			block = []string{line}
		case block != nil:
			block = append(block, line)
			if line != "}" {
				continue
			}
			if d, ok := decodeModelDetail(id, strings.Join(block, "\n")); ok {
				out = append(out, d)
			}
			block, id = nil, ""
		default:
			if s := strings.TrimSpace(line); s != "" && !strings.ContainsAny(s, " \t") && strings.Contains(s, "/") {
				id = s
			}
		}
	}
	return out
}

func decodeModelDetail(id, text string) (ModelDetail, bool) {
	var v struct {
		ID         string `json:"id"`
		ProviderID string `json:"providerID"`
		Status     string `json:"status"`
		API        struct {
			URL string `json:"url"`
			NPM string `json:"npm"`
		} `json:"api"`
		Capabilities struct {
			ToolCall bool `json:"toolcall"`
			Output   struct {
				Text  bool `json:"text"`
				Image bool `json:"image"`
				Audio bool `json:"audio"`
			} `json:"output"`
		} `json:"capabilities"`
	}
	if json.Unmarshal([]byte(text), &v) != nil || v.ID == "" {
		return ModelDetail{}, false
	}
	if id == "" {
		id = v.ProviderID + "/" + v.ID
	}
	return ModelDetail{
		ID: id, Model: v.ID, NPM: v.API.NPM, URL: v.API.URL, Status: v.Status,
		ToolCall:    v.Capabilities.ToolCall,
		OutputText:  v.Capabilities.Output.Text,
		OutputImage: v.Capabilities.Output.Image,
		OutputAudio: v.Capabilities.Output.Audio,
	}, true
}
