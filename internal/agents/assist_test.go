// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCleanAssistOutput(t *testing.T) {
	cases := map[string]string{
		"  hello  ":                       "hello",
		"```\ncode here\n```":             "code here",
		"```markdown\n# Title\nbody\n```": "# Title\nbody",
		"no fences":                       "no fences",
	}
	for in, want := range cases {
		if got := cleanAssistOutput(in); got != want {
			t.Errorf("cleanAssistOutput(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPreserveRequires(t *testing.T) {
	// Model dropped the Requires line → it is re-appended.
	orig := "Reviews PRs.\nRequires: the gh MCP server"
	improved := "Reviews pull requests thoroughly and suggests fixes."
	got := preserveRequires(orig, improved)
	if !strings.Contains(got, "Requires: the gh MCP server") {
		t.Errorf("Requires line not preserved: %q", got)
	}

	// Model kept it → not duplicated.
	improved2 := "Reviews PRs well.\nRequires: the gh MCP server"
	got2 := preserveRequires(orig, improved2)
	if strings.Count(got2, "Requires:") != 1 {
		t.Errorf("Requires line duplicated: %q", got2)
	}

	// No Requires originally → nothing added.
	got3 := preserveRequires("Just a description.", "A better description.")
	if strings.Contains(got3, "Requires:") {
		t.Errorf("unexpected Requires line: %q", got3)
	}
}

func newAssistHandler(fn AssistFn) *http.ServeMux {
	h := &Handlers{Store: &Store{Dir: ""}, AssistFn: fn}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux
}

func postJSON(mux *http.ServeMux, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b)))
	return rr
}

func TestAssistPromptHandler(t *testing.T) {
	var seen string
	mux := newAssistHandler(func(_ context.Context, prompt string) (string, error) {
		seen = prompt
		return "```\nPolished prompt.\n```", nil
	})
	rr := postJSON(mux, "/api/agent-builder/assist/prompt", map[string]string{
		"name": "Reviewer", "prompt": "review stuff",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var out map[string]string
	json.Unmarshal(rr.Body.Bytes(), &out)
	if out["text"] != "Polished prompt." {
		t.Errorf("text = %q (fences should be stripped)", out["text"])
	}
	if !strings.Contains(seen, "review stuff") {
		t.Errorf("instruction didn't include the current prompt: %q", seen)
	}
}

func TestAssistDescriptionPreservesRequires(t *testing.T) {
	mux := newAssistHandler(func(_ context.Context, _ string) (string, error) {
		return "A sharper description without the dependency line.", nil
	})
	rr := postJSON(mux, "/api/agent-builder/assist/description", map[string]string{
		"description": "Old desc.\nRequires: the jira MCP server",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var out map[string]string
	json.Unmarshal(rr.Body.Bytes(), &out)
	if !strings.Contains(out["text"], "Requires: the jira MCP server") {
		t.Errorf("Requires line not preserved through the handler: %q", out["text"])
	}
}

func TestAssistEmptyInputRejected(t *testing.T) {
	mux := newAssistHandler(func(_ context.Context, _ string) (string, error) { return "x", nil })
	rr := postJSON(mux, "/api/agent-builder/assist/prompt", map[string]string{"prompt": "   "})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("empty prompt status = %d, want 422", rr.Code)
	}
}

func TestAssistUnavailableWhenNil(t *testing.T) {
	mux := newAssistHandler(nil)
	rr := postJSON(mux, "/api/agent-builder/assist/prompt", map[string]string{"prompt": "hi"})
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("nil AssistFn status = %d, want 503", rr.Code)
	}
}

// TestOptimizePromptHandler covers the chat composer assist: the draft is
// sent through AssistFn with the user-prompt instruction, wrapper fences are
// stripped, and the improved text is returned. The instruction must ask for a
// rewrite (not an answer) and must include the user's draft.
func TestOptimizePromptHandler(t *testing.T) {
	var seen string
	mux := newAssistHandler(func(_ context.Context, prompt string) (string, error) {
		seen = prompt
		return "```\nRewritten, clearer prompt.\n```", nil
	})
	rr := postJSON(mux, "/api/chat/assist/prompt", map[string]string{
		"prompt": "help me fix the bug",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var out map[string]string
	json.Unmarshal(rr.Body.Bytes(), &out)
	if out["text"] != "Rewritten, clearer prompt." {
		t.Errorf("text = %q (fences should be stripped)", out["text"])
	}
	if !strings.Contains(seen, "help me fix the bug") {
		t.Errorf("instruction didn't include the user draft: %q", seen)
	}
	if !strings.Contains(seen, "Do not answer the prompt") {
		t.Errorf("instruction missing the do-not-answer guard: %q", seen)
	}
}

// TestOptimizePromptEmptyRejected ensures a whitespace-only draft is rejected
// with the friendly 422 guard rather than calling the model.
func TestOptimizePromptEmptyRejected(t *testing.T) {
	called := false
	mux := newAssistHandler(func(_ context.Context, _ string) (string, error) {
		called = true
		return "x", nil
	})
	rr := postJSON(mux, "/api/chat/assist/prompt", map[string]string{"prompt": "   "})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("empty draft status = %d, want 422", rr.Code)
	}
	if called {
		t.Error("AssistFn should not be called for an empty draft")
	}
}

// TestOptimizePromptUnavailableWhenNil ensures the route returns 503 when no
// assist backend is wired.
func TestOptimizePromptUnavailableWhenNil(t *testing.T) {
	mux := newAssistHandler(nil)
	rr := postJSON(mux, "/api/chat/assist/prompt", map[string]string{"prompt": "hi"})
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("nil AssistFn status = %d, want 503", rr.Code)
	}
}
