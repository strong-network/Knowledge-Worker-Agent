// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package materializer

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// VocabularyDir is where assigned dictation vocabularies land, under configDir.
// OpenCode never reads it; internal/voice does.
const VocabularyDir = "kwa-vocabulary"

// Materialize writes the resolved artifact set for a project into the
// platform-owned OpenCode config dir (configDir, i.e. OPENCODE_CONFIG_DIR).
//
// The materializer OWNS configDir end to end: it is wiped and rewritten on
// every run so that artifacts no longer resolved (reassignments, retired IDs)
// are removed, not just added. The user's Global (~/.config/opencode) is a
// DIFFERENT directory and is never touched here; OpenCode's native per-key
// merge layers this dir above it.
//
// Layout written:
//
//	configDir/opencode.json      (merged mcp + provider fragments + instructions)
//	configDir/agent/<name>.md    (agent artifacts)
//	configDir/skills/<name>/…    (skill artifacts, folders)
//	configDir/context/<name>.md  (context artifacts, referenced via instructions)
//	configDir/kwa-vocabulary/NNN-<name>.md (dictation vocabularies; NOT opencode config)
//	configDir/mcp-default-on.json (connector selection metadata; NOT opencode config)
//	configDir/mcp-meta.json      (MCP auth metadata; NOT opencode config)
//	configDir/provider-meta.json (provider metadata; NOT opencode config)
//
// repoDir is the local checkout root the resolved paths are relative to.
// userGlobalDir, when non-empty, guards against accidentally wiping the user's
// own Global config dir.
//
// The returned warnings describe artifact content that was accepted but partly
// ignored (see takeMCPMeta). They are not errors: materialization succeeded.
func Materialize(configDir, repoDir string, res *Resolved, userGlobalDir string) ([]string, error) {
	if strings.TrimSpace(configDir) == "" {
		return nil, fmt.Errorf("materialize: empty config dir")
	}
	absCfg, err := filepath.Abs(configDir)
	if err != nil {
		return nil, fmt.Errorf("materialize: resolve config dir: %w", err)
	}
	if userGlobalDir != "" {
		if absUser, err := filepath.Abs(userGlobalDir); err == nil && absUser == absCfg {
			return nil, fmt.Errorf("materialize: refusing to write to the user Global config dir %q", absCfg)
		}
	}
	if isRootish(absCfg) {
		return nil, fmt.Errorf("materialize: refusing to operate on unsafe path %q", absCfg)
	}

	// Own the dir: wipe and recreate.
	if err := os.RemoveAll(absCfg); err != nil {
		return nil, fmt.Errorf("materialize: clean config dir: %w", err)
	}
	if err := os.MkdirAll(absCfg, 0o755); err != nil {
		return nil, fmt.Errorf("materialize: create config dir: %w", err)
	}

	// Accumulator for the merged opencode.json (mcp + provider fragments).
	merged := map[string]any{"$schema": "https://opencode.ai/config.json"}
	var instructions []string

	// Agents -> configDir/agent/<basename>.md
	for _, art := range res.ByKind[KindAgents] {
		dst := filepath.Join(absCfg, "agent", filepath.Base(art.Path))
		if err := copyFile(filepath.Join(repoDir, art.Path), dst); err != nil {
			return nil, fmt.Errorf("materialize agent %s: %w", art.ID, err)
		}
	}

	// Skills -> configDir/skills/<name>/… (folders)
	//
	// Plural, matching what internal/defaults installs into the user's Global
	// and what the configuration repository's layout specifies. OpenCode globs `{skill,skills}/**/SKILL.md` in
	// its config dirs, so both names load — but only the plural one is scanned
	// in the `.claude`/`.agents` compat dirs, and it is the name its own docs
	// use. Keeping one name across both writers means a skill folder can be
	// moved between the bundled defaults and the central config repo unchanged.
	for _, art := range res.ByKind[KindSkills] {
		name := filepath.Base(strings.TrimRight(art.Path, "/"))
		dst := filepath.Join(absCfg, "skills", name)
		if err := copyDir(filepath.Join(repoDir, art.Path), dst); err != nil {
			return nil, fmt.Errorf("materialize skill %s: %w", art.ID, err)
		}
	}

	// Context -> configDir/context/<basename>.md, referenced via instructions.
	for _, art := range res.ByKind[KindContext] {
		rel := filepath.Join("context", filepath.Base(art.Path))
		if err := copyFile(filepath.Join(repoDir, art.Path), filepath.Join(absCfg, rel)); err != nil {
			return nil, fmt.Errorf("materialize context %s: %w", art.ID, err)
		}
		instructions = append(instructions, rel)
	}

	// Vocabulary -> configDir/kwa-vocabulary/NNN-<basename>.md. The number keeps
	// assignment order, which is the order dictation merges them in.
	for i, art := range res.ByKind[KindVocabulary] {
		name := fmt.Sprintf("%03d-%s", i+1, filepath.Base(art.Path))
		if err := copyFile(filepath.Join(repoDir, art.Path), filepath.Join(absCfg, VocabularyDir, name)); err != nil {
			return nil, fmt.Errorf("materialize vocabulary %s: %w", art.ID, err)
		}
	}

	// MCP + provider fragments -> merged into opencode.json.
	//
	// Both kinds may additionally carry a "kwa" block (or "sds", its old name) — for a
	// provider, who supplies the key and which models back the Default/Thinking
	// presets; for an MCP server, how it is signed in to. That block is NOT
	// opencode config, so it is stripped before merging and written to a sidecar
	// instead — the same split mcp-default-on.json already uses. Order
	// is preserved: preset conflicts between several assigned providers break on
	// assignment order.
	var providerMeta []ProviderMeta
	var mcpMeta []MCPMeta
	var warnings []string
	for _, kind := range []string{KindMCP, KindProvider} {
		for _, art := range res.ByKind[kind] {
			frag, err := loadFragment(filepath.Join(repoDir, art.Path))
			if err != nil {
				return nil, fmt.Errorf("materialize %s %s: %w", kind, art.ID, err)
			}
			switch kind {
			case KindProvider:
				providerMeta = append(providerMeta, takeProviderMeta(frag)...)
			case KindMCP:
				meta, warn := takeMCPMeta(art.ID, frag)
				mcpMeta = append(mcpMeta, meta...)
				warnings = append(warnings, warn...)
			}
			mergeInto(merged, frag)
		}
	}

	if len(instructions) > 0 {
		// Preserve any instructions a fragment contributed, then append ours.
		if existing, ok := merged["instructions"].([]any); ok {
			for _, e := range existing {
				if s, ok := e.(string); ok {
					instructions = append([]string{s}, instructions...)
				}
			}
		}
		merged["instructions"] = dedupe(instructions)
	}

	if err := writeJSONFile(filepath.Join(absCfg, "opencode.json"), merged); err != nil {
		return nil, fmt.Errorf("materialize opencode.json: %w", err)
	}

	// Selection metadata — carried, NOT materialized into opencode config.
	meta := map[string]any{"mcp_default_on": res.MCPDefaultOn}
	if err := writeJSONFile(filepath.Join(absCfg, "mcp-default-on.json"), meta); err != nil {
		return nil, fmt.Errorf("materialize mcp-default-on.json: %w", err)
	}

	// Provider metadata and MCP metadata — same split: carried for
	// Knowledge Worker Agent, never fed to opencode. Always written (possibly empty) so a
	// reader can distinguish "materialized, nothing assigned" from "never
	// materialized".
	if providerMeta == nil {
		providerMeta = []ProviderMeta{}
	}
	if err := writeJSONFile(filepath.Join(absCfg, ProviderMetaFile), providerMetaFile{Providers: providerMeta}); err != nil {
		return nil, fmt.Errorf("materialize %s: %w", ProviderMetaFile, err)
	}
	if mcpMeta == nil {
		mcpMeta = []MCPMeta{}
	}
	if err := writeJSONFile(filepath.Join(absCfg, MCPMetaFile), mcpMetaFile{Servers: mcpMeta}); err != nil {
		return nil, fmt.Errorf("materialize %s: %w", MCPMetaFile, err)
	}

	return warnings, nil
}

// loadFragment reads and parses an opencode.json fragment (which may contain
// JSONC comments).
func loadFragment(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	clean := stripTrailingCommas(stripJSONComments(data))
	var m map[string]any
	if err := json.Unmarshal(clean, &m); err != nil {
		return nil, fmt.Errorf("parse fragment %s: %w", filepath.Base(path), err)
	}
	return m, nil
}

// mergeInto deep-merges src into dst. Nested JSON objects (map[string]any) merge
// per key (so mcp/provider server maps accumulate); every other value type
// (including arrays and scalars) is overwritten by src. $schema is left as the
// canonical value already present in dst.
func mergeInto(dst, src map[string]any) {
	for k, v := range src {
		if k == "$schema" {
			continue
		}
		if sv, ok := v.(map[string]any); ok {
			if dv, ok := dst[k].(map[string]any); ok {
				mergeInto(dv, sv)
				continue
			}
		}
		dst[k] = v
	}
}

// copyFile copies a single file, creating parent dirs.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

// copyDir recursively copies a directory tree.
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

// writeJSONFile writes v as indented JSON with a trailing newline. Map keys are
// emitted in a stable (sorted) order by encoding/json, keeping output
// deterministic across runs.
func writeJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

// isRootish rejects obviously dangerous targets (root, home dir itself).
func isRootish(abs string) bool {
	clean := filepath.Clean(abs)
	if clean == "/" || clean == "." {
		return true
	}
	if home, err := os.UserHomeDir(); err == nil {
		if clean == filepath.Clean(home) {
			return true
		}
	}
	// A path with no separator beyond the leading one is suspicious.
	return len(strings.Split(strings.Trim(clean, string(filepath.Separator)), string(filepath.Separator))) < 1
}
