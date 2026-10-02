// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package obsidianinstaller auto-installs the obsidian-mcp Node package into
// ~/.local/bin (or OBSIDIAN_INSTALL_DIR) on server startup, and the server
// then registers it as an MCP server in opencode.json.
//
// obsidian-mcp is published on npm and we don't vendor a release archive —
// installation requires npm to be available on the host. If npm is missing the
// installer logs a warning and returns; startup proceeds normally and the
// Obsidian MCP server simply won't be wired up.
package obsidianinstaller

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// DefaultInstallDir returns the parent of bin/. obsidian-mcp ends up at
// <DefaultInstallDir>/bin/obsidian-mcp via `npm install --prefix`.
func DefaultInstallDir() string { return layout.ObsidianInstall() }

// DefaultVaultDir returns the Obsidian vault directory we hand to obsidian-mcp
// on startup. Created if it doesn't exist.
func DefaultVaultDir() string { return layout.ObsidianVault() }

// PackageName is the npm package we install.
const PackageName = "obsidian-mcp"

// PinnedVersion is the only obsidian-mcp version Ensure installs. npm checks the
// package against the registry's integrity hash.
const PinnedVersion = "2.0.1"

// minNodeMajor is the oldest Node.js major version PinnedVersion supports.
const minNodeMajor = 22

// Result describes what Ensure decided to do.
type Result struct {
	Bin        string
	VaultDir   string
	Installed  bool
	Skipped    bool
	SkipReason string
}

// Options configures Ensure.
type Options struct {
	InstallDir string
	VaultDir   string
}

// BinPath returns the expected location of obsidian-mcp under the install dir.
func BinPath(installDir string) string {
	if installDir == "" {
		installDir = DefaultInstallDir()
	}
	return filepath.Join(installDir, "bin", "obsidian-mcp")
}

// VerifyBin returns true if obsidian-mcp at the given path is invocable.
// obsidian-mcp prints its usage when run without args, so we treat any output
// as proof the binary launched.
func VerifyBin(bin string) bool {
	if bin == "" {
		return false
	}
	if _, err := os.Stat(bin); err != nil {
		return false
	}
	out, _ := exec.Command(bin).CombinedOutput()
	return len(out) > 0
}

// Ensure verifies obsidian-mcp is installed at <InstallDir>/bin/obsidian-mcp
// and installs it via npm if not. The default vault directory is created if
// missing. Errors are non-fatal — the caller should treat a Skipped result as
// "obsidian integration unavailable" and continue.
func Ensure(out io.Writer, opts Options) (Result, error) {
	if out == nil {
		out = os.Stderr
	}
	installDir := opts.InstallDir
	if installDir == "" {
		installDir = DefaultInstallDir()
	}
	vaultDir := opts.VaultDir
	if vaultDir == "" {
		vaultDir = DefaultVaultDir()
	}
	res := Result{Bin: BinPath(installDir), VaultDir: vaultDir}

	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		fmt.Fprintf(out, "  ⚠ obsidian: cannot create vault dir %s: %v\n", vaultDir, err)
	} else {
		ensureWelcomeNote(vaultDir)
	}

	// A working install of another version is upgraded, but kept if the upgrade
	// can't run. An install whose version can't be read is left alone.
	working := VerifyBin(res.Bin)
	if working {
		v := installedVersion(installDir)
		if v == "" || v == PinnedVersion {
			fmt.Fprintf(out, "  ✓ Obsidian MCP: %s\n", res.Bin)
			return res, nil
		}
		fmt.Fprintf(out, "  ↻ Obsidian MCP: upgrading %s %s to the pinned %s\n", PackageName, v, PinnedVersion)
	}
	keepOrSkip := func(reason string) (Result, error) {
		if working {
			fmt.Fprintf(out, "  ⚠ Obsidian MCP: keeping the installed version (%s)\n", reason)
			return res, nil
		}
		res.Skipped = true
		res.SkipReason = reason
		res.Bin = ""
		fmt.Fprintf(out, "  ⚠ Obsidian MCP: skipped (%s)\n", reason)
		return res, nil
	}

	npm, err := exec.LookPath("npm")
	if err != nil {
		return keepOrSkip("npm not found in PATH; install Node.js and npm to enable")
	}
	if major := nodeMajor(); major < minNodeMajor {
		return keepOrSkip(fmt.Sprintf("%s %s needs Node.js %d or later", PackageName, PinnedVersion, minNodeMajor))
	}

	fmt.Fprintf(out, "  ⤓ Installing %s@%s into %s ...\n", PackageName, PinnedVersion, installDir)
	cmd := exec.Command(npm, "install", "--prefix", installDir, "-g", PackageName+"@"+PinnedVersion)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		if working {
			return keepOrSkip(fmt.Sprintf("npm install failed: %v", err))
		}
		res.Skipped = true
		res.SkipReason = fmt.Sprintf("npm install failed: %v", err)
		res.Bin = ""
		fmt.Fprintf(out, "  ⚠ Obsidian MCP install failed: %v\n", err)
		return res, err
	}

	if !VerifyBin(res.Bin) {
		res.Skipped = true
		res.SkipReason = "binary missing after install"
		res.Bin = ""
		fmt.Fprintf(out, "  ⚠ Obsidian MCP: install completed but %s not found\n", BinPath(installDir))
		return res, nil
	}

	res.Installed = true
	fmt.Fprintf(out, "  ✓ Obsidian MCP installed: %s\n", res.Bin)
	return res, nil
}

// installedVersion reads the version npm recorded for the package under
// installDir, or "" if it can't be read.
func installedVersion(installDir string) string {
	b, err := os.ReadFile(filepath.Join(installDir, "lib", "node_modules", PackageName, "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(b, &pkg) != nil {
		return ""
	}
	return pkg.Version
}

// nodeMajor returns the major version of the node on PATH, or 0 if unknown.
func nodeMajor() int {
	out, err := exec.Command("node", "--version").Output()
	if err != nil {
		return 0
	}
	major, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(string(out)), "v"), ".")
	n, _ := strconv.Atoi(major)
	return n
}

const welcomeNote = `# Welcome to your Obsidian Vault

This vault is auto-managed by Knowledge Worker Agent.

The vault is wired into the assistant through the
[obsidian-mcp](https://github.com/StevenStavrakis/obsidian-mcp) MCP server,
so you can ask the assistant to:

- "List the notes in my vault"
- "Create a new note titled 'Project ideas' with these bullet points: ..."
- "Search my vault for 'meeting'"

Files placed in this directory will be visible to the assistant through the
MCP tools.
`

func ensureWelcomeNote(vaultDir string) {
	// obsidian-mcp validates the vault by requiring .obsidian/app.json to
	// exist; without it every tool call fails with "Not a valid Obsidian
	// vault". Initialize the minimum config Obsidian itself would create on
	// first launch.
	obsidianDir := filepath.Join(vaultDir, ".obsidian")
	_ = os.MkdirAll(obsidianDir, 0o755)
	appJSON := filepath.Join(obsidianDir, "app.json")
	if _, err := os.Stat(appJSON); err != nil {
		_ = os.WriteFile(appJSON, []byte("{}\n"), 0o644)
	}
	workspaceJSON := filepath.Join(obsidianDir, "workspace.json")
	if _, err := os.Stat(workspaceJSON); err != nil {
		_ = os.WriteFile(workspaceJSON, []byte("{}\n"), 0o644)
	}

	p := filepath.Join(vaultDir, "Welcome.md")
	if _, err := os.Stat(p); err == nil {
		return
	}
	_ = os.WriteFile(p, []byte(welcomeNote), 0o644)
}
