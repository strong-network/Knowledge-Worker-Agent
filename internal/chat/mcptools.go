// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"log"
	"sort"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/mcp"
)

// Attributing a tool call to the MCP server that provides it.
//
// opencode namespaces every tool an MCP server contributes as
// "<serverName>_<toolName>" — e.g. `atlassian_getJiraIssue`,
// `github_gh__create_pull_request`, `Jira-Citrix_getJiraIssue`. Nothing else in
// the event says where a tool came from: the tool part carries only `tool`,
// `callID`, `state` and `metadata`, so the prefix is the only signal available.
//
// Splitting on the first underscore would be wrong. Built-in tools contain
// underscores too (`list_mcp_resources`, `todo_write`), and an MCP server's own
// tool names contain them freely (`gh__create_pull_request`). So a name only
// counts as an MCP call when its prefix matches a server that is actually
// configured — which is what keeps `list_mcp_resources` an ordinary tool unless
// someone genuinely runs a server called "list".

// mcpServerNames is the seam tests substitute for; production reads the
// configured servers out of the opencode config.
var mcpServerNames = func() ([]string, error) {
	servers, err := mcp.GlobalServers()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(servers))
	for _, s := range servers {
		names = append(names, s.Name)
	}
	return names, nil
}

// mcpAttributor resolves tool names to MCP servers for the span of one turn.
//
// The list is read once, lazily, on the first tool event: a turn with no tool
// calls never touches the config, and a turn with fifty does not re-read it
// fifty times. Per-turn rather than global because a turn's tool set is fixed
// when opencode starts it anyway — so this needs no TTL, no lock, and no
// invalidation when the user adds a server.
type mcpAttributor struct {
	servers []string
	loaded  bool
}

// load reads the configured server names, longest first so that a server named
// "github" cannot claim a call belonging to one named "github-enterprise".
func (a *mcpAttributor) load() {
	a.loaded = true
	names, err := mcpServerNames()
	if err != nil {
		// A config we cannot read means we cannot attribute calls, so every
		// tool renders as an ordinary one — exactly today's behaviour, and
		// strictly better than dropping the events.
		log.Printf("[MCP] cannot list servers to attribute tool calls: %v", err)
		return
	}
	sort.SliceStable(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	a.servers = names
}

// annotate fills in an event's MCP attribution, leaving ordinary tools untouched.
func (a *mcpAttributor) annotate(e *Event) {
	if e == nil || e.Tool == "" || e.McpServer != "" {
		return
	}
	if !a.loaded {
		a.load()
	}
	if server, _ := splitMcpTool(e.Tool, a.servers); server != "" {
		e.McpServer = server
	}
}

// splitMcpTool attributes a tool name to a configured MCP server. It returns the
// server's configured name — not the casing used in the tool name, so the UI
// agrees with the connector picker — and the tool name with that prefix removed.
//
// An empty server means an ordinary built-in tool.
func splitMcpTool(tool string, servers []string) (server, bare string) {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		return "", ""
	}
	lower := strings.ToLower(tool)
	for _, s := range servers {
		if s == "" {
			continue
		}
		prefix := strings.ToLower(s) + "_"
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		rest := tool[len(prefix):]
		if rest == "" {
			// "atlassian_" with nothing after it is not a call from that
			// server; treat it as an ordinary name rather than inventing an
			// empty row under a connector.
			continue
		}
		return s, rest
	}
	return "", tool
}
