// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"encoding/json"
	"fmt"
	"strings"
)

// `opencode run` cancels the question tool's questions, having no one to ask, so
// the turn stops at them and the user's answers go out as the next message.

// questionTool is opencode's tool for asking the user.
const questionTool = "question"

// AskedQuestion is one question of a question tool call.
type AskedQuestion struct {
	Header   string        `json:"header,omitempty"`
	Question string        `json:"question"`
	Options  []AskedOption `json:"options,omitempty"`
	Multiple bool          `json:"multiple,omitempty"`
}

// AskedOption is an answer the agent offers.
type AskedOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// askedQuestions returns the questions of a question tool call; nil for any other call.
func askedQuestions(tool string, args map[string]any) []AskedQuestion {
	if tool != questionTool {
		return nil
	}
	raw, err := json.Marshal(args["questions"])
	if err != nil {
		return nil
	}
	var all []AskedQuestion
	if json.Unmarshal(raw, &all) != nil {
		return nil
	}
	var qs []AskedQuestion
	for _, q := range all {
		if strings.TrimSpace(q.Question) == "" {
			continue
		}
		var opts []AskedOption
		for _, o := range q.Options {
			if strings.TrimSpace(o.Label) != "" {
				opts = append(opts, o)
			}
		}
		q.Options = opts
		qs = append(qs, q)
	}
	return qs
}

// questionsMarkdown is the questions as the reply shows them, so the transcript keeps them.
func questionsMarkdown(qs []AskedQuestion) string {
	var b strings.Builder
	for i, q := range qs {
		indent := ""
		if len(qs) > 1 {
			fmt.Fprintf(&b, "%d. ", i+1)
			indent = "   "
		}
		if h := headerLabel(q.Header); h != "" {
			b.WriteString("**" + h + "** ")
		}
		b.WriteString(oneLine(q.Question))
		if q.Multiple {
			b.WriteString(" (choose any)")
		}
		b.WriteString("\n")
		for _, o := range q.Options {
			b.WriteString(indent + "- **" + oneLine(o.Label) + "**")
			if d := oneLine(o.Description); d != "" {
				b.WriteString(": " + d)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// headerLabel ends a header with a colon, unless it ends in punctuation already.
func headerLabel(header string) string {
	h := strings.TrimSuffix(oneLine(header), ":")
	if h == "" || strings.ContainsAny(h[len(h)-1:], "?!.") {
		return h
	}
	return h + ":"
}
