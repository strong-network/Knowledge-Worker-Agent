// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeauth

import "testing"

const verboseListing = `google-vertex/claude-haiku-4-5@20251001
{
  "id": "claude-haiku-4-5@20251001",
  "providerID": "google-vertex",
  "api": {
    "id": "claude-haiku-4-5@20251001",
    "url": "",
    "npm": "@ai-sdk/google-vertex/anthropic"
  },
  "status": "active",
  "capabilities": {
    "toolcall": true,
    "output": { "text": true, "audio": false, "image": false }
  },
  "variants": {
    "low": {
      "effort": "low"
    }
  }
}
` + "\x1b[2mwarning: a stray log line\x1b[0m" + `
google-vertex/openai/gpt-oss-20b-maas
{
  "id": "openai/gpt-oss-20b-maas",
  "providerID": "google-vertex",
  "api": {
    "url": "https://${GOOGLE_VERTEX_ENDPOINT}/v1/projects/${GOOGLE_VERTEX_PROJECT}/locations/${GOOGLE_VERTEX_LOCATION}/endpoints/openapi",
    "npm": "@ai-sdk/openai-compatible"
  },
  "status": "deprecated",
  "capabilities": {
    "toolcall": true,
    "output": { "text": true }
  }
}
google-vertex/broken
{
  "id": 
}
google-vertex/gemini-3-pro-image
{
  "id": "gemini-3-pro-image",
  "providerID": "google-vertex",
  "api": { "npm": "@ai-sdk/google-vertex" },
  "capabilities": { "toolcall": false, "output": { "text": true, "image": true } }
}
`

func TestParseModelDetails(t *testing.T) {
	got := parseModelDetails(verboseListing)
	if len(got) != 3 {
		t.Fatalf("got %d models, want 3: %+v", len(got), got)
	}
	h := got[0]
	if h.ID != "google-vertex/claude-haiku-4-5@20251001" || h.Model != "claude-haiku-4-5@20251001" ||
		h.NPM != "@ai-sdk/google-vertex/anthropic" || h.Status != "active" || !h.ToolCall || !h.OutputText || h.OutputImage {
		t.Errorf("haiku = %+v", h)
	}
	o := got[1]
	if o.ID != "google-vertex/openai/gpt-oss-20b-maas" || o.Status != "deprecated" ||
		o.URL != "https://${GOOGLE_VERTEX_ENDPOINT}/v1/projects/${GOOGLE_VERTEX_PROJECT}/locations/${GOOGLE_VERTEX_LOCATION}/endpoints/openapi" {
		t.Errorf("gpt-oss = %+v", o)
	}
	if g := got[2]; g.Model != "gemini-3-pro-image" || g.ToolCall || !g.OutputImage {
		t.Errorf("image model = %+v", g)
	}
	if parseModelDetails("") != nil {
		t.Error("empty listing produced models")
	}
}
