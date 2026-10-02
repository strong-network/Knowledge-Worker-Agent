// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package agents

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/agentbuilder"
)

// AssistFn runs a single, synchronous text-in/text-out model turn: it takes a
// fully-composed prompt and returns the model's reply text (no tools, no
// persistence). It is injected so the agents package stays testable without the
// chat backend; production wires it to chat.RunStream in main.go.
type AssistFn func(ctx context.Context, prompt string) (string, error)

// assistRequest is the body for both assist endpoints. Prompt-polish reads
// `prompt`; description-improve reads `description` (and may use `name`/`prompt`
// as context). Unused fields are simply ignored.
type assistRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
}

// polishPrompt — POST /api/agent-builder/assist/prompt. Rewrites the agent's
// system prompt to be clearer and more effective, returning only the new text.
func (h *Handlers) polishPrompt(w http.ResponseWriter, r *http.Request) {
	if h.AssistFn == nil {
		writeError(w, http.StatusServiceUnavailable, "The writing assistant isn't available right now.")
		return
	}
	var req assistRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "The request body wasn't valid.")
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeError(w, http.StatusUnprocessableEntity, "Write a prompt first, then polish it.")
		return
	}
	out, err := h.AssistFn(r.Context(), buildPolishPromptInstruction(req.Name, req.Description, req.Prompt))
	if err != nil {
		writeError(w, http.StatusBadGateway, "The writing assistant couldn't finish. Try again.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": cleanAssistOutput(out)})
}

// optimizePrompt — POST /api/chat/assist/prompt. The chat composer's "Optimize
// my prompt" assist: rewrites the user's *draft prompt* to be clearer,
// more specific, and better structured, returning only the improved text. It
// reuses the same AssistFn + output cleanup as the Agent Builder polisher, but
// with a user-prompt instruction (not an agent system-prompt one). The returned
// text replaces the composer draft; nothing is sent.
func (h *Handlers) optimizePrompt(w http.ResponseWriter, r *http.Request) {
	if h.AssistFn == nil {
		writeError(w, http.StatusServiceUnavailable, "The writing assistant isn't available right now.")
		return
	}
	var req assistRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "The request body wasn't valid.")
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeError(w, http.StatusUnprocessableEntity, "Write a prompt first, then optimize it.")
		return
	}
	out, err := h.AssistFn(r.Context(), buildOptimizeUserPromptInstruction(req.Prompt))
	if err != nil {
		writeError(w, http.StatusBadGateway, "Couldn't optimize just now — try again.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": cleanAssistOutput(out)})
}

// improveDescription — POST /api/agent-builder/assist/description. Sharpens the
// routing description ("when to use this agent"), preserving any author-declared
// `Requires:` dependency line so improving the text never breaks import
// surfacing.
func (h *Handlers) improveDescription(w http.ResponseWriter, r *http.Request) {
	if h.AssistFn == nil {
		writeError(w, http.StatusServiceUnavailable, "The writing assistant isn't available right now.")
		return
	}
	var req assistRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "The request body wasn't valid.")
		return
	}
	if strings.TrimSpace(req.Description) == "" {
		writeError(w, http.StatusUnprocessableEntity, "Write a description first, then improve it.")
		return
	}
	out, err := h.AssistFn(r.Context(), buildImproveDescriptionInstruction(req.Name, req.Description, req.Prompt))
	if err != nil {
		writeError(w, http.StatusBadGateway, "The writing assistant couldn't finish. Try again.")
		return
	}
	result := preserveRequires(req.Description, cleanAssistOutput(out))
	writeJSON(w, http.StatusOK, map[string]any{"text": result})
}

// buildPolishPromptInstruction composes the instruction for the prompt polisher.
func buildPolishPromptInstruction(name, description, prompt string) string {
	var b strings.Builder
	b.WriteString("You are improving the system prompt for a custom AI agent. ")
	b.WriteString("Rewrite the prompt below so it is clearer, better structured, and more effective at guiding the agent's behavior. ")
	b.WriteString("Keep the author's intent and any specific rules; make instructions concrete and unambiguous; remove filler. ")
	b.WriteString("Do not invent capabilities or tools the prompt doesn't mention.\n\n")
	b.WriteString("Return ONLY the rewritten prompt as plain text — no preamble, no explanation, no surrounding quotes or code fences.\n\n")
	if strings.TrimSpace(name) != "" {
		b.WriteString("Agent name: " + strings.TrimSpace(name) + "\n")
	}
	if strings.TrimSpace(description) != "" {
		b.WriteString("Agent purpose: " + strings.TrimSpace(description) + "\n")
	}
	b.WriteString("\nCurrent prompt:\n")
	b.WriteString(strings.TrimSpace(prompt))
	return b.String()
}

// buildOptimizeUserPromptInstruction composes the instruction for the chat
// composer's "Optimize my prompt" assist. Unlike
// buildPolishPromptInstruction — which improves an agent's *system prompt* —
// this improves a *user's own task or question* to an assistant: it rewrites
// the draft to be clearer, more specific, and better structured while
// preserving the user's intent, without answering it or inventing details.
func buildOptimizeUserPromptInstruction(draft string) string {
	var b strings.Builder
	b.WriteString("You are improving a prompt that a person is about to send to an AI assistant. ")
	b.WriteString("Rewrite the prompt below so it is clearer, more specific, and better structured, so the assistant can respond well on the first try.\n\n")
	b.WriteString("Follow these rules:\n")
	b.WriteString("- Preserve the person's intent and constraints exactly. Keep every requirement, preference, and detail they stated. Do not drop anything they asked for.\n")
	b.WriteString("- Do not invent details. Do not add facts, context, names, numbers, or requirements the person did not provide. If something important is genuinely missing, phrase the ask so the assistant knows what to ask for rather than fabricating it.\n")
	b.WriteString("- Do not answer the prompt. Your job is only to rewrite the request, not to fulfill it.\n")
	b.WriteString("- Stay proportional. Improve wording and structure; do not pad a short, simple request into something long and bureaucratic. A clear one-line prompt can stay one line.\n")
	b.WriteString("- Keep the original language (for example, a German prompt stays in German). Do not translate.\n")
	b.WriteString("- Make it self-contained and unambiguous: state the task, any needed context the person already gave, and — where the person implied one — the desired form of the answer.\n\n")
	b.WriteString("Return ONLY the rewritten prompt as plain text — no preamble, no explanation, no surrounding quotes or code fences.\n\n")
	b.WriteString("Current prompt:\n")
	b.WriteString(strings.TrimSpace(draft))
	return b.String()
}

// buildImproveDescriptionInstruction composes the instruction for the
// description assist.
func buildImproveDescriptionInstruction(name, description, prompt string) string {
	var b strings.Builder
	b.WriteString("You are improving the description of a custom AI agent. ")
	b.WriteString("The description is a routing signal: it tells an orchestrator WHEN to use this agent. ")
	b.WriteString("Rewrite the description below to be concise and specific about the tasks and situations this agent is for. ")
	b.WriteString("Keep it to a few sentences. ")
	b.WriteString("If the description contains a line beginning with \"Requires:\", keep that line exactly as-is on its own line.\n\n")
	b.WriteString("Return ONLY the rewritten description as plain text — no preamble, no explanation, no surrounding quotes or code fences.\n\n")
	if strings.TrimSpace(name) != "" {
		b.WriteString("Agent name: " + strings.TrimSpace(name) + "\n")
	}
	if strings.TrimSpace(prompt) != "" {
		b.WriteString("For context, the agent's prompt is:\n" + strings.TrimSpace(prompt) + "\n")
	}
	b.WriteString("\nCurrent description:\n")
	b.WriteString(strings.TrimSpace(description))
	return b.String()
}

// cleanAssistOutput strips the wrappers models tend to add despite instructions:
// leading/trailing whitespace and an enclosing fenced code block.
func cleanAssistOutput(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if nl := strings.IndexByte(s, '\n'); nl >= 0 {
			s = s[nl+1:] // drop the opening ``` (and any language tag) line
		}
		if idx := strings.LastIndex(s, "```"); idx >= 0 {
			s = s[:idx]
		}
	}
	return strings.TrimSpace(s)
}

// preserveRequires guarantees the author's dependency convention survives the
// description assist: if the original had a "Requires:" line but the improved
// text dropped it, the line is re-appended.
func preserveRequires(original, improved string) string {
	req := agentbuilder.RequiresLine(original)
	if req == "" {
		return improved
	}
	if agentbuilder.RequiresLine(improved) != "" {
		return improved // the model kept it (possibly reworded) — leave as-is
	}
	improved = strings.TrimRight(improved, "\n")
	return improved + "\nRequires: " + req
}
