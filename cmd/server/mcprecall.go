// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

// The `mcp-recall` subcommand: the cross-session retrieval MCP server.
//
// It is a subcommand of this same binary rather than a separate executable
// because there is nothing to install -- the binary is already on disk and
// already running. That is what makes recall the simpler half of the Obsidian
// registration model: no package manager, no download, no verification step,
// and no "capability unavailable" branch.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/mcp"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/mcprecall"
)

// runMCPRecall serves the recall MCP server on stdio until the client closes
// its side. Returns a process exit code.
func runMCPRecall(args []string) int {
	fs := flag.NewFlagSet("mcp-recall", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	url := fs.String("url", "", "base URL of the running chat server (default: derived from KWA_PORT)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	base := *url
	if base == "" {
		// Fall back to the configured port rather than a hard-coded one, so a
		// server on a non-default port still works if --url was omitted.
		config.Init()
		base = fmt.Sprintf("http://127.0.0.1:%d", config.Port)
	}

	// stdout is the JSON-RPC transport: nothing else may ever be written to it,
	// or the framing breaks. Diagnostics go to stderr, which opencode captures
	// separately.
	if err := mcprecall.New(base, os.Stdin, os.Stdout).Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "mcp-recall: %v\n", err)
		return 1
	}
	return 0
}

// ensureRecallMCP registers this binary as the "recall" MCP server, enabled.
//
// Enabled is the deviation from every other entry in the catalogue, and it is
// deliberate: recall is not a third party. It serves the user's own chat
// history from the local database over loopback, so there is no vendor to
// consent to and no credential to hand over. Leaving it off would mean the
// agent could not see its own past conversations until the user found a switch
// they had no reason to look for.
//
// EnsureOwnedServer rewrites the command and arguments at every start, since
// this binary's path and port can change; Enabled decides only the first
// write, so if the user turns recall off, it stays off.
func ensureRecallMCP() {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ⚠ MCP recall: cannot resolve own path: %v\n", err)
		return
	}
	mcp.EnsureOwnedServer(os.Stderr, mcp.DefaultServer{
		Name:      "recall",
		Transport: "stdio",
		Command:   self,
		// --url pins the child to the port this server actually bound, rather
		// than letting it re-derive one from an environment it may not inherit.
		Args:    []string{"mcp-recall", "--url", "http://127.0.0.1:" + strconv.Itoa(config.Port)},
		Auth:    mcp.AuthLocal,
		Enabled: true,
		Help:    "Searches your own past chats in this workspace. Runs locally; nothing is sent anywhere.",
	})
}
