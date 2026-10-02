// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"fmt"
	"io"
	"maps"
	"os/exec"
	"slices"
	"strings"
	"sync"
)

// Owned servers are ones Knowledge Worker Agent runs itself: recall is this
// binary, obsidian is a package it installs. Their definitions hold paths only
// this process knows, and those paths move when the binary or an install
// directory moves. An add-if-absent write would keep the first path for good,
// so every start rewrites the definition instead. Only the user's on/off choice
// carries over.
var (
	ownedMu sync.Mutex
	owned   = map[string]bool{}
)

// IsOwned reports whether Knowledge Worker Agent rewrites this server's
// definition at every start. Such a server may be switched on and off, but an
// edit or a removal would undo itself at the next start, so both are refused.
func IsOwned(name string) bool {
	ownedMu.Lock()
	defer ownedMu.Unlock()
	return owned[strings.ToLower(strings.TrimSpace(name))]
}

func ownedRefusal(name string) string {
	return name + " is set up by Knowledge Worker Agent, so it can't be changed here. " +
		"You can still turn it on or off — that choice is yours and it survives a restart."
}

// EnsureOwnedServer registers s, or brings an existing entry's definition up to
// date. s.Enabled decides only the first write; after that the user's on/off
// choice stays.
func EnsureOwnedServer(logw io.Writer, s DefaultServer) {
	if logw == nil {
		logw = io.Discard
	}
	ownedMu.Lock()
	owned[strings.ToLower(s.Name)] = true
	ownedMu.Unlock()

	existing, ok, err := getServer(s.Name)
	if err != nil {
		fmt.Fprintf(logw, "  ⚠ MCP defaults: cannot read opencode config: %v\n", err)
		return
	}
	if !ok {
		EnsureServer(logw, s)
		return
	}
	want := serverView{
		Name:        existing.Name,
		Transport:   s.Transport,
		Command:     s.Command,
		Args:        s.Args,
		URL:         s.URL,
		Environment: s.Env,
		Headers:     s.Headers,
		Enabled:     existing.Enabled,
		Timeout:     existing.Timeout,
	}
	if want.Transport == "" {
		want.Transport = "stdio"
		if want.URL != "" {
			want.Transport = "http"
		}
	}
	if sameDefinition(existing, want) {
		return
	}
	if _, err := upsertServer(want); err != nil {
		fmt.Fprintf(logw, "  ⚠ MCP defaults: failed to update %q: %v\n", s.Name, err)
		return
	}
	fmt.Fprintf(logw, "  ✓ MCP defaults: updated %q (was %s)\n", s.Name, describeTarget(existing))
}

func sameDefinition(a, b serverView) bool {
	local := func(t string) bool { return t == "stdio" || t == "local" }
	return local(strings.ToLower(a.Transport)) == local(strings.ToLower(b.Transport)) &&
		a.Command == b.Command && slices.Equal(a.Args, b.Args) && a.URL == b.URL &&
		maps.Equal(a.Environment, b.Environment) && maps.Equal(a.Headers, b.Headers)
}

func describeTarget(v serverView) string {
	if v.URL != "" {
		return v.URL
	}
	return strings.TrimSpace(v.Command + " " + strings.Join(v.Args, " "))
}

// WarnMissingCommands logs every enabled local server whose program can't be
// found. opencode fails to start such a server on every turn, and nothing else
// says so.
func WarnMissingCommands(logw io.Writer) {
	if logw == nil {
		logw = io.Discard
	}
	views, err := listServers()
	if err != nil {
		return
	}
	for _, v := range views {
		if !v.Enabled || v.Command == "" || !strings.EqualFold(v.Transport, "stdio") {
			continue
		}
		if _, err := exec.LookPath(v.Command); err != nil {
			fmt.Fprintf(logw, "  ⚠ MCP: %q is on, but its program %s isn't found, so it can't start\n", v.Name, v.Command)
		}
	}
}
