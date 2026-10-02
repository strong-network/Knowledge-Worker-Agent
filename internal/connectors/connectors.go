// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package connectors implements MCP connector selection: resolving which
// of the workspace's globally-configured MCP servers are "on" for a given chat
// context (a standalone chat or a Knowledge Worker Agent project), and reconciling that
// selection live against the running opencode process for the context's working
// directory. It is the single home for this logic so the per-chat (sessions)
// and per-project (projects) surfaces stay consistent.
package connectors

import (
	"context"
	"sort"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/mcp"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodeserver"
)

// Connector is the per-context view of one MCP server returned to the UI.
// Selected is the persisted selection for the context; Status is the live
// connection status from the running opencode server, or "" when no live
// instance is serving the context's working directory yet.
type Connector struct {
	Name     string `json:"name"`
	Selected bool   `json:"selected"`
	Status   string `json:"status"`
}

// EffectiveSelection resolves a context's selection: every globally-enabled
// server defaults to on, and any explicit per-context override wins. A nil
// override yields the global defaults (the seed). Servers disabled globally
// are omitted entirely — opencode never connects them, so they cannot be
// selected into a chat. The result is keyed by server name and never nil.
func EffectiveSelection(override map[string]bool, servers []mcp.GlobalServer) map[string]bool {
	sel := make(map[string]bool, len(servers))
	for _, s := range servers {
		if !s.Enabled {
			continue
		}
		if v, ok := override[s.Name]; ok {
			sel[s.Name] = v
		} else {
			sel[s.Name] = true
		}
	}
	return sel
}

// enabledOnly filters a global server list down to the enabled entries.
func enabledOnly(servers []mcp.GlobalServer) []mcp.GlobalServer {
	out := make([]mcp.GlobalServer, 0, len(servers))
	for _, s := range servers {
		if s.Enabled {
			out = append(out, s)
		}
	}
	return out
}

// Build combines the global server list, the context's selection override, and
// (best effort) the live status for workdir into the sorted connector list for
// the UI. Globally-disabled servers are excluded: they are configured but not
// usable (typically not signed in), so they are managed in the MCP Servers
// modal rather than offered per chat.
func Build(workdir string, override map[string]bool) ([]Connector, error) {
	all, err := mcp.GlobalServers()
	if err != nil {
		return nil, err
	}
	servers := enabledOnly(all)
	sel := EffectiveSelection(override, servers)
	status := liveStatus(workdir)
	out := make([]Connector, 0, len(servers))
	for _, s := range servers {
		out = append(out, Connector{
			Name:     s.Name,
			Selected: sel[s.Name],
			Status:   status[s.Name],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Merge applies an incoming partial selection over the context's resolved
// current selection, ignoring unknown server names, and returns the full
// name→on map to persist. A partial body only changes the named servers.
func Merge(override, incoming map[string]bool) (map[string]bool, error) {
	all, err := mcp.GlobalServers()
	if err != nil {
		return nil, err
	}
	servers := enabledOnly(all)
	known := make(map[string]bool, len(servers))
	for _, s := range servers {
		known[s.Name] = true
	}
	sel := EffectiveSelection(override, servers)
	for name, on := range incoming {
		if known[name] {
			sel[name] = on
		}
	}
	return sel, nil
}

// Apply reconciles the desired selection against the running opencode instance
// for workdir. Best effort: when no live instance exists (no turn has run yet)
// it is a no-op, and the selection takes effect on the next turn's fresh
// process via the seeded config.
func Apply(workdir string, sel map[string]bool) {
	client, ok := liveClient(workdir)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	current, err := client.McpStatus(ctx)
	if err != nil {
		return
	}
	for name, want := range sel {
		connected := IsConnected(current[name])
		switch {
		case want && !connected:
			_ = client.McpConnect(ctx, name)
		case !want && connected:
			_ = client.McpDisconnect(ctx, name)
		}
	}
}

// IsConnected reports whether a live MCP status string represents an active
// connection (connected or in the process of connecting).
func IsConnected(status string) bool {
	switch status {
	case "connected", "connecting", "pending":
		return true
	default:
		return false
	}
}

// liveStatus returns the running opencode server's MCP status map for workdir,
// or nil when no live instance exists (no turn has run yet).
func liveStatus(workdir string) map[string]string {
	client, ok := liveClient(workdir)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, err := client.McpStatus(ctx)
	if err != nil {
		return nil
	}
	return status
}

// liveClient returns the client for an already-running opencode instance bound
// to workdir, without starting one. ok is false when none is live.
func liveClient(workdir string) (*opencodeserver.Client, bool) {
	sup := chat.Supervisor()
	if sup == nil {
		return nil, false
	}
	return sup.Client(workdir, config.Workspace)
}
