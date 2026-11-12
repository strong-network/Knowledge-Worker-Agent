// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/mcp"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/mcppkbrain"
)

func runMCPPkBrain(args []string) int {
	flags := flag.NewFlagSet("mcp-pkbrain", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	baseURL := flags.String("url", os.Getenv("PKBRAIN_API_URL"), "PKBrain local Compose service URL")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	server, err := mcppkbrain.New(*baseURL, os.Stdin, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mcp-pkbrain: PKBrain service URL is missing or invalid")
		return 2
	}
	if err := server.Run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "mcp-pkbrain: MCP input failed")
		return 1
	}
	return 0
}

func ensurePKBrainMCP() {
	configuredURL := os.Getenv("PKBRAIN_API_URL")
	if configuredURL == "" {
		return
	}
	baseURL, err := mcppkbrain.ValidateBaseURL(configuredURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "  ⚠ MCP PKBrain history: local service URL is invalid")
		return
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ⚠ MCP PKBrain history: cannot resolve own path: %v\n", err)
		return
	}
	mcp.EnsureOwnedServer(os.Stderr, mcp.DefaultServer{
		Name:      "pkbrain-history",
		Transport: "stdio",
		Command:   self,
		Args:      []string{"mcp-pkbrain", "--url", baseURL},
		Auth:      mcp.AuthLocal,
		Enabled:   true,
		Help:      "On-demand search of imported ChatGPT and other-agent history in PKBrain. KWA Recall searches chats created in KWA.",
	})
}
