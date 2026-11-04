// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeauth

import (
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/vertexauth"
)

// 2.2.5's /api/model, one Vertex model per client package plus a Copilot one
// (trimmed: no variants, costs or dates).
const modelsV2 = `{"location": {"directory": "/w"}, "data": [
{"id": "gemini-4-argon", "providerID": "google-vertex", "type": "chat", "package": "@opencode/ai/providers/google-vertex/gemini", "settings": {"project": "demo-project", "location": "us-east5"}, "capabilities": {"tools": true, "input": ["text", "image", "video", "audio", "pdf"], "output": ["text"]}, "status": "active", "enabled": true},
{"id": "claude-haiku-5-5@default", "providerID": "google-vertex", "type": "chat", "package": "@opencode/ai/providers/google-vertex/messages", "settings": {"project": "demo-project", "location": "us-east5"}, "capabilities": {"tools": true, "input": ["text", "image", "pdf"], "output": ["text"]}, "status": "active", "enabled": true},
{"id": "xai/grok-4.7", "providerID": "google-vertex", "type": "chat", "package": "@opencode/ai/providers/google-vertex/chat", "settings": {"project": "demo-project", "location": "us-east5", "baseURL": "https://us-east5-aiplatform.googleapis.com/v1/projects/demo-project/locations/us-east5/endpoints/openapi"}, "capabilities": {"tools": true, "input": ["text", "image"], "output": ["text"]}, "status": "beta", "enabled": true},
{"id": "codestral-2", "providerID": "google-vertex", "type": "chat", "package": "@opencode/ai/providers/google-vertex/mistral", "settings": {"project": "demo-project", "location": "us-east5", "baseURL": "https://us-east5-aiplatform.googleapis.com/v1/projects/demo-project/locations/us-east5/publishers/mistralai/models"}, "capabilities": {"tools": true, "input": ["text"], "output": ["text"]}, "status": "active", "enabled": true},
{"id": "imagen-9", "providerID": "google-vertex", "type": "chat", "package": "@opencode/ai/providers/google-vertex/gemini", "settings": {}, "capabilities": {"tools": false, "output": ["image"]}, "status": "active", "enabled": true},
{"id": "gpt-6.1-terra", "providerID": "github-copilot", "type": "chat", "package": "aisdk:@ai-sdk/github-copilot", "settings": {"baseURL": "https://api.githubcopilot.com"}, "capabilities": {"tools": true, "input": ["text", "image", "pdf"], "output": ["text"]}, "status": "active", "enabled": true}
]}`

func TestModelDetailsV2(t *testing.T) {
	got := modelDetailsV2([]byte(modelsV2), "google-vertex")
	if len(got) != 5 {
		t.Fatalf("got %d Vertex models, want 5: %+v", len(got), got)
	}
	byModel := map[string]ModelDetail{}
	for _, d := range got {
		byModel[d.Model] = d
	}
	// The API each is checked through, as 1.x's packages named it.
	for model, want := range map[string]vertexauth.API{
		"gemini-4-argon":           vertexauth.APIGemini,
		"claude-haiku-5-5@default": vertexauth.APIAnthropic,
		"xai/grok-4.7":             vertexauth.APIOpenAI,
		"codestral-2":              vertexauth.APIUnknown,
	} {
		if api := vertexauth.APIFor(byModel[model].NPM); api != want {
			t.Errorf("%s: API %v from %q, want %v", model, api, byModel[model].NPM, want)
		}
	}
	grok := byModel["xai/grok-4.7"]
	if grok.ID != "google-vertex/xai/grok-4.7" || grok.Status != "beta" || !grok.ToolCall || !grok.OutputText ||
		grok.URL != "https://us-east5-aiplatform.googleapis.com/v1/projects/demo-project/locations/us-east5/endpoints/openapi" {
		t.Errorf("grok = %+v", grok)
	}
	if img := byModel["imagen-9"]; img.ToolCall || img.OutputText || !img.OutputImage {
		t.Errorf("imagen = %+v", img)
	}
	if c := modelDetailsV2([]byte(modelsV2), "github-copilot"); len(c) != 1 || c[0].NPM != "@ai-sdk/github-copilot" {
		t.Errorf("copilot = %+v", c)
	}
	if modelDetailsV2([]byte("not json"), "google-vertex") != nil {
		t.Error("unreadable list gave models")
	}
}
