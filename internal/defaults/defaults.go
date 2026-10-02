// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package defaults installs the content Knowledge Worker Agent ships with its binary
// into the user's OpenCode config dir (~/.config/opencode).
//
// What ships is deliberately minimal. Agents, context and product-specific
// skills are the administrator's to choose, and they come from the
// central config repo; shipping a second copy in the binary meant every
// workspace silently got Citrix-specific content on top of whatever was
// centrally assigned, which is precisely what stopped an administrator from
// deciding what a user sees. That bundle is retired — see RetireLegacyDefaults,
// which removes it from workspaces that already have it.
//
// Two things still ship, because they are platform capability rather than
// content an administrator curates:
//
//   - plugin/ — OpenCode plugins that keep chat working (see InstallPlugins).
//   - skills/ — the document-handling skills (pdf, docx, pptx, xlsx). Reading
//     an attached document is a baseline expectation of the product, not a
//     Citrix opinion, and pdf-guard.js actively directs the model to the `pdf`
//     skill when it drops an attachment the provider cannot accept. Leaving
//     that skill to the config repo would mean a workspace whose guard
//     recommends a capability it does not have.
//
// Both install in every workspace, centrally managed or not.
package defaults

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

//go:embed all:skills
var skillsFS embed.FS

//go:embed all:plugin
var pluginFS embed.FS

// Bundle roots. Each is both the directory inside the embedded FS and the
// directory it is written to under the OpenCode config dir, which is what lets
// copyTree take a single root argument.
const (
	skillsRoot = "skills"
	pluginRoot = "plugin"
)

const manifestName = ".defaults-manifest.json"

// manifest records the SHA-256 of each file the retired content bundle wrote at
// install time. Nothing writes it any more: it survives only so
// RetireLegacyDefaults can tell a file it installed (safe to remove) from one
// the user has since edited (theirs to keep).
type manifest struct {
	Version int               `json:"version"`
	Files   map[string]string `json:"files"` // relative path → sha256 hex
}

func loadManifest(targetDir string) *manifest {
	m := &manifest{Version: 1, Files: map[string]string{}}
	data, err := os.ReadFile(filepath.Join(targetDir, manifestName))
	if err != nil {
		return m
	}
	_ = json.Unmarshal(data, m)
	if m.Files == nil {
		m.Files = map[string]string{}
	}
	return m
}

func (m *manifest) save(targetDir string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(targetDir, manifestName), data, 0o644)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// copyTree writes the embedded tree rooted at root into targetDir/root.
//
// Every file is platform-owned: one whose bytes differ from the embedded
// version is overwritten, and one that already matches is left alone so an
// unchanged bundle does not churn mtimes on every boot. Nothing is recorded in
// the manifest, which is what keeps these trees out of RetireLegacyDefaults'
// reach.
//
// Overwriting rather than preserving edits is the point: these are not content
// a user is invited to fork. A local edit is a shipped fix not being applied.
func copyTree(fsys fs.FS, root, targetDir string) (int, error) {
	var written int
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dest := filepath.Join(targetDir, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", p, err)
		}
		if existing, err := os.ReadFile(dest); err == nil && string(existing) == string(data) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", dest, err)
		}
		written++
		return nil
	})
	return written, err
}

// treePaths returns every file path in an embedded tree, in the slash form the
// manifest uses for paths relative to the OpenCode config dir.
func treePaths(fsys fs.FS, root string) map[string]bool {
	out := map[string]bool{}
	_ = fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		out[p] = true
		return nil
	})
	return out
}

// InstallSkills writes the bundled document-handling skills into
// targetDir/skills.
//
// These are the floor: the workspace's ability to read a PDF, Word, PowerPoint
// or Excel file the user attached. Unlike the retired content bundle they
// install in every workspace, including a centrally managed one, for the same
// reason InstallPlugins does — a workspace on central config runs the same
// OpenCode against the same providers and needs the same baseline to be
// useful. An administrator who ships their own version of one of these simply
// overrides it from the platform config dir, which OpenCode resolves above the
// user's Global.
func InstallSkills(w io.Writer, targetDir string) error {
	if strings.TrimSpace(targetDir) == "" {
		return fmt.Errorf("install opencode skills: empty target dir")
	}
	written, err := copyTree(skillsFS, skillsRoot, targetDir)
	if err != nil {
		return fmt.Errorf("install opencode skills: %w", err)
	}
	if written > 0 {
		fmt.Fprintf(w, "  ✓ Installed %d document-skill file(s) to %s\n", written, filepath.Join(targetDir, skillsRoot))
	}
	return nil
}

// InstallPlugins writes the platform's OpenCode plugins into targetDir/plugin.
//
// These are not content: they are guards that keep the chat backend working
// (see plugin/pdf-guard.js, which stops an unreadable attachment from
// permanently breaking a session). They install in every workspace, centrally
// managed or not, and are kept out of the defaults manifest so
// RetireLegacyDefaults can never remove them.
func InstallPlugins(w io.Writer, targetDir string) error {
	if strings.TrimSpace(targetDir) == "" {
		return fmt.Errorf("install opencode plugins: empty target dir")
	}
	written, err := copyTree(pluginFS, pluginRoot, targetDir)
	if err != nil {
		return fmt.Errorf("install opencode plugins: %w", err)
	}
	if written > 0 {
		fmt.Fprintf(w, "  ✓ Installed %d opencode plugin(s) to %s\n", written, filepath.Join(targetDir, pluginRoot))
	}
	return nil
}

// RetireLegacyDefaults removes the content bundle earlier builds installed: the
// Citrix agents, the product context files, AGENTS.md, and the
// product-specific skills.
//
// Not installing them any more is not enough on its own. Any workspace that has
// started once already has them on disk, and stale content does not sit there
// inertly — it keeps acting. The retired AGENTS.md is the global rules file
// injected into every turn, and it names context files to read, subagents to
// delegate to and skills to load that no longer exist; the retired agents stay
// in the picker and get passed to --agent; the retired skills stay in
// OpenCode's available-skills list, armable and paid for on every turn. Left
// behind they also shadow whatever the config repo assigns, which is the "two
// copies of the same guidance disagreeing" problem this change exists to end.
//
// Only files we own are removed, decided by the manifest the old installer
// wrote: a file goes if its on-disk content still matches the hash recorded
// when we wrote it. Anything the user has since edited is left in place and
// keeps its manifest entry, so this can never delete someone's work. Files that
// are already gone simply drop out of the manifest.
//
// Paths belonging to a bundle we still ship are skipped. Today that is the
// document skills, which the old bundle also installed and therefore recorded:
// without this guard, retirement would delete the very floor InstallSkills had
// just written, and the order of the two calls would silently decide whether a
// workspace could read a PDF.
//
// Directories emptied by the removal are pruned, and the manifest itself is
// deleted once nothing of ours is left, so a retired workspace looks untouched
// rather than littered with empty folders. Once that happens this is a no-op
// forever, which is what makes it safe to run on every boot.
func RetireLegacyDefaults(w io.Writer, targetDir string) error {
	if strings.TrimSpace(targetDir) == "" {
		return fmt.Errorf("retire opencode defaults: empty target dir")
	}
	m := loadManifest(targetDir)
	if len(m.Files) == 0 {
		return nil
	}

	keep := treePaths(skillsFS, skillsRoot)
	remaining := map[string]string{}
	prune := map[string]bool{}
	var removed, kept int

	for rel, recorded := range m.Files {
		if keep[filepath.ToSlash(rel)] {
			// Still shipped, just no longer manifest-tracked. Drop the entry
			// without touching the file.
			continue
		}
		dest := filepath.Join(targetDir, filepath.FromSlash(rel))
		data, err := os.ReadFile(dest)
		switch {
		case os.IsNotExist(err):
			// Already gone — drop the entry rather than resurrecting it.
			continue
		case err != nil:
			// Can't read it, so can't prove we own it. Leave it alone.
			remaining[rel] = recorded
			continue
		}
		if sha256Hex(data) != recorded {
			// Edited since we wrote it: the user's, not ours.
			remaining[rel] = recorded
			kept++
			continue
		}
		if err := os.Remove(dest); err != nil {
			remaining[rel] = recorded
			continue
		}
		removed++
		prune[filepath.Dir(dest)] = true
	}

	m.Files = remaining
	if len(m.Files) == 0 {
		if err := os.Remove(filepath.Join(targetDir, manifestName)); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(w, "  ! Failed to remove opencode defaults manifest: %v\n", err)
		}
	} else if err := m.save(targetDir); err != nil {
		fmt.Fprintf(w, "  ! Failed to write opencode defaults manifest: %v\n", err)
	}

	pruneEmptyDirs(targetDir, prune)

	if removed > 0 {
		fmt.Fprintf(w, "  ✓ Retired %d bundled default file(s) from %s\n", removed, targetDir)
	}
	if kept > 0 {
		fmt.Fprintf(w, "  • Preserved %d user-modified default file(s) in %s\n", kept, targetDir)
	}
	return nil
}

// pruneEmptyDirs removes directories left empty by a retirement, walking up
// from each one towards root but never past (or including) root itself.
// os.Remove refuses to delete a non-empty directory, which is exactly the test
// we want: the walk stops at the first directory that still holds something.
func pruneEmptyDirs(root string, dirs map[string]bool) {
	for dir := range dirs {
		for dir != root && strings.HasPrefix(dir, root+string(os.PathSeparator)) {
			if err := os.Remove(dir); err != nil {
				break
			}
			dir = filepath.Dir(dir)
		}
	}
}

// ShippedAgentFilenames returns the set of agent *.md filenames (base names,
// e.g. "product-manager.md") that an earlier build's content bundle wrote into
// targetDir's "agent/" directory, as recorded in the SHA-256 manifest. The
// Agent Builder uses this to keep shipped agents out of the user's
// editable list.
//
// Knowledge Worker Agent no longer ships agents, so on a retired workspace this is
// empty and every agent in the user's Global is genuinely theirs. It still
// matters on the boot before RetireLegacyDefaults has run, where the old files
// are on disk and must not be presented as the user's own work.
//
// The returned map is always non-nil.
func ShippedAgentFilenames(targetDir string) map[string]bool {
	shipped := map[string]bool{}
	m := loadManifest(targetDir)
	for rel := range m.Files {
		// Manifest keys use forward slashes ("agent/<name>.md"); normalize for
		// safety on any platform.
		rel = filepath.ToSlash(rel)
		dir, file := path.Split(rel)
		if dir == "agent/" && strings.HasSuffix(file, ".md") {
			shipped[file] = true
		}
	}
	return shipped
}
