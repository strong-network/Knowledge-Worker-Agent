// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"regexp"
	"strconv"
	"strings"
)

// opencode 2.x "Code Mode".
//
// On 2.x the model does not call connector (MCP) tools one by one. It writes a
// short program and runs it with a single `execute` tool, and the program
// calls the connectors. The JSON stream reports that as one `execute` call
// whose input is the program; the calls the program made are listed in the
// tool's metadata, in the order they started:
//
//	state.metadata.metadata.toolCalls: [
//	  {"tool": "microsoft-learn.microsoft_docs_search", "status": "completed", "input": {...}},
//	  {"tool": "search", "status": "completed", "input": {"query": "..."}},
//	]
//
// A row reading "execute" over a program tells the user nothing, and the
// connectors bar would stay dark, because it attributes calls by the
// `<server>_<tool>` names 1.x gave connector tools. So each listed call
// becomes a row of its own, under the name 1.x would have used. opencode does
// not report a result per call, only the program's, so a row carries that
// result only when it is the program's one call, or when it failed (the
// program's output then holds the error).
//
// A program that calls nothing is plain computation and stays one row.

// codeModeTool is the 2.x tool that runs a Code Mode program.
const codeModeTool = "execute"

// codeModeCalls returns the rows standing in for one finished `execute` call:
// one per call its program made, or nil when it made none and the call should
// be shown as itself.
func codeModeCalls(callID, sid string, state map[string]any, output string) []*Event {
	calls := codeModeToolCalls(state)
	if len(calls) == 0 {
		return nil
	}
	rows := make([]*Event, 0, len(calls))
	for i, c := range calls {
		name := strings.TrimSpace(strOr(c, "tool"))
		if name == "" {
			continue
		}
		args, _ := c["input"].(map[string]any)
		ok := strOr(c, "status") == "completed"
		row := &Event{
			Kind:      "tool_done",
			CallID:    callID + "/" + strconv.Itoa(i),
			Tool:      codeModeToolName(name),
			Args:      args,
			Success:   ok,
			SessionID: sid,
		}
		if !ok || len(calls) == 1 {
			row.Output = output
		}
		if !ok && isPermissionDenied(output) {
			row.Kind = "tool_blocked"
		}
		rows = append(rows, row)
	}
	return rows
}

// codeModeToolCalls reads the calls a program made out of the tool state. The
// list sits one level down, under the tool's own metadata.
func codeModeToolCalls(state map[string]any) []map[string]any {
	meta, _ := state["metadata"].(map[string]any)
	if inner, ok := meta["metadata"].(map[string]any); ok {
		meta = inner
	}
	list, _ := meta["toolCalls"].([]any)
	calls := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if c, ok := item.(map[string]any); ok {
			calls = append(calls, c)
		}
	}
	return calls
}

// codeModeFailed reports whether a program failed. opencode marks the call
// completed either way and flags a failed program in the metadata.
func codeModeFailed(state map[string]any) bool {
	meta, _ := state["metadata"].(map[string]any)
	if inner, ok := meta["metadata"].(map[string]any); ok {
		meta = inner
	}
	failed, _ := meta["error"].(bool)
	return failed
}

// codeModeToolName names a call the way 1.x named the same tool. A program
// reaches a connector's tool as `<server>.<tool>`, which 1.x called
// `<server>_<tool>`; opencode's own tools sit under `opencode.` and had no
// prefix. Code Mode's own functions (`search`, `fetch`) keep their names.
func codeModeToolName(name string) string {
	ns, tool, ok := strings.Cut(name, ".")
	if !ok || ns == "" || tool == "" {
		return name
	}
	if ns == "opencode" {
		return tool
	}
	return ns + "_" + tool
}

// systemReminder matches a block of opencode's own notes to the model. 2.x
// sends a change to the model's instructions or tool catalog as a message
// wrapped in these tags, and the first turn of a conversation started on 1.x
// receives one. A small model sometimes copies the block into its reply; it is
// never meant for the user.
var systemReminder = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>\s*`)

// stripSystemReminders removes copied notes from a reply.
func stripSystemReminders(text string) string {
	if !strings.Contains(text, "<system-reminder>") {
		return text
	}
	return systemReminder.ReplaceAllString(text, "")
}
