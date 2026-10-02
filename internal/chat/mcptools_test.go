// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"errors"
	"testing"
)

// stubMcpServers points the server lookup at a fixed list for one test.
func stubMcpServers(t *testing.T, names []string, err error) {
	t.Helper()
	prev := mcpServerNames
	mcpServerNames = func() ([]string, error) { return names, err }
	t.Cleanup(func() { mcpServerNames = prev })
}

// The real names below are taken from recorded opencode sessions, so this is
// the actual shape the classifier has to cope with rather than an invented one.
func TestSplitMcpToolAttributesRealToolNames(t *testing.T) {
	servers := []string{
		"microsoft-foundry", "microsoft-learn", "azure-devops", "Jira-Citrix",
		"gainsight", "pagerduty", "context7", "deepwiki", "atlassian", "clickup",
		"obsidian", "github", "sentry", "figma", "zephyr", "slack", "Otter",
		"Pendo", "aws",
	}
	// Longest first, as resolveMcpServers orders them.
	sortByLengthDesc(servers)

	cases := []struct {
		tool       string
		wantServer string
		wantBare   string
	}{
		{"atlassian_getJiraIssue", "atlassian", "getJiraIssue"},
		{"atlassian_searchJiraIssuesUsingJql", "atlassian", "searchJiraIssuesUsingJql"},
		// The server namespaces its own tools with "gh__"; only our prefix comes off.
		{"github_gh__create_pull_request", "github", "gh__create_pull_request"},
		{"github_github-copilot-chat-standard__search_repositories", "github", "github-copilot-chat-standard__search_repositories"},
		// Configured with capitals; the configured spelling is what comes back.
		{"Jira-Citrix_getJiraIssue", "Jira-Citrix", "getJiraIssue"},
		{"Otter_otter_search", "Otter", "otter_search"},
		{"Pendo_list_all_applications", "Pendo", "list_all_applications"},
		{"context7_resolve-library-id", "context7", "resolve-library-id"},
		{"clickup_clickup_search", "clickup", "clickup_search"},

		// Built-ins must stay built-in. list_mcp_resources is the trap: it is
		// about MCP and contains an underscore, but no server is called "list".
		{"list_mcp_resources", "", "list_mcp_resources"},
		{"list_mcp_resource_templates", "", "list_mcp_resource_templates"},
		{"todowrite", "", "todowrite"},
		{"webfetch", "", "webfetch"},
		{"bash", "", "bash"},
		{"read", "", "read"},
		{"skill", "", "skill"},
		{"task", "", "task"},
	}

	for _, c := range cases {
		server, bare := splitMcpTool(c.tool, servers)
		if server != c.wantServer || bare != c.wantBare {
			t.Errorf("splitMcpTool(%q) = (%q, %q), want (%q, %q)",
				c.tool, server, bare, c.wantServer, c.wantBare)
		}
	}
}

// A shorter server name must not claim a call belonging to a longer one that
// starts with the same text.
func TestSplitMcpToolPrefersTheLongerServerName(t *testing.T) {
	servers := []string{"github-enterprise", "github"}
	sortByLengthDesc(servers)

	server, bare := splitMcpTool("github-enterprise_search_code", servers)
	if server != "github-enterprise" || bare != "search_code" {
		t.Fatalf("expected the longer server to win, got (%q, %q)", server, bare)
	}
	if server, _ := splitMcpTool("github_search_code", servers); server != "github" {
		t.Errorf("expected the shorter server to still match its own calls, got %q", server)
	}
}

// A bare server name with nothing after the separator is not a tool call.
func TestSplitMcpToolIgnoresAPrefixWithNoToolName(t *testing.T) {
	if server, _ := splitMcpTool("atlassian_", []string{"atlassian"}); server != "" {
		t.Errorf("expected no attribution for a bare prefix, got %q", server)
	}
	if server, _ := splitMcpTool("", []string{"atlassian"}); server != "" {
		t.Errorf("expected no attribution for an empty name, got %q", server)
	}
	if server, _ := splitMcpTool("atlassian", []string{"atlassian"}); server != "" {
		t.Errorf("expected the server name alone not to be a call, got %q", server)
	}
}

func TestAnnotateSetsTheServerOnMcpToolsOnly(t *testing.T) {
	stubMcpServers(t, []string{"atlassian", "github"}, nil)
	var a mcpAttributor

	mcpCall := &Event{Kind: "tool_call", Tool: "atlassian_getJiraIssue"}
	a.annotate(mcpCall)
	if mcpCall.McpServer != "atlassian" {
		t.Errorf("expected the connector to be attributed, got %q", mcpCall.McpServer)
	}
	// The name is left whole: the browser needs it to match tool_done to
	// tool_call, and the UI strips the prefix for display itself.
	if mcpCall.Tool != "atlassian_getJiraIssue" {
		t.Errorf("expected the tool name to be left intact, got %q", mcpCall.Tool)
	}

	builtin := &Event{Kind: "tool_call", Tool: "read"}
	a.annotate(builtin)
	if builtin.McpServer != "" {
		t.Errorf("expected an ordinary tool to stay unattributed, got %q", builtin.McpServer)
	}
}

// The server list is read once per turn, however many tools the turn uses.
func TestAnnotateReadsTheServerListOnce(t *testing.T) {
	calls := 0
	prev := mcpServerNames
	mcpServerNames = func() ([]string, error) {
		calls++
		return []string{"atlassian"}, nil
	}
	t.Cleanup(func() { mcpServerNames = prev })

	var a mcpAttributor
	for i := 0; i < 5; i++ {
		a.annotate(&Event{Kind: "tool_call", Tool: "atlassian_getJiraIssue"})
	}
	if calls != 1 {
		t.Fatalf("expected the server list to be read once per turn, read %d times", calls)
	}
}

// A turn that never calls a tool must not read the config at all.
func TestAnnotateDoesNotReadTheServerListWithoutToolCalls(t *testing.T) {
	calls := 0
	prev := mcpServerNames
	mcpServerNames = func() ([]string, error) {
		calls++
		return []string{"atlassian"}, nil
	}
	t.Cleanup(func() { mcpServerNames = prev })

	var a mcpAttributor
	a.annotate(&Event{Kind: "chunk", Text: "hello"})
	if calls != 0 {
		t.Fatalf("expected no config read for a turn with no tools, read %d times", calls)
	}
}

// An unreadable config must degrade to "everything is an ordinary tool", never
// to a dropped or mislabelled event.
func TestAnnotateFallsBackToOrdinaryToolsWhenTheConfigCannotBeRead(t *testing.T) {
	stubMcpServers(t, nil, errors.New("config unreadable"))
	var a mcpAttributor

	e := &Event{Kind: "tool_call", Tool: "atlassian_getJiraIssue"}
	a.annotate(e)
	if e.McpServer != "" {
		t.Errorf("expected no attribution when the server list is unavailable, got %q", e.McpServer)
	}
	if e.Tool != "atlassian_getJiraIssue" {
		t.Errorf("expected the event to survive intact, got %q", e.Tool)
	}
}

// sortByLengthDesc mirrors the ordering mcpAttributor.load applies.
func sortByLengthDesc(names []string) {
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && len(names[j]) > len(names[j-1]); j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
}
