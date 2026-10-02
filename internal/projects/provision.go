// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package projects

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/git"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// projectsRoot returns the base directory that holds all project workspaces.
// Each project gets its own subdirectory here.
func projectsRoot() string { return layout.Projects() }

// slugify turns a project name into a filesystem-safe folder name.
func slugify(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	prevDash := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == '-' || r == '_' || r == ' ' || r == '.':
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		default:
			// drop other characters
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		s = "project"
	}
	return s
}

// uniqueDir returns base if it doesn't exist, else base-2, base-3, …
func uniqueDir(base string) string {
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return base
	}
	for i := 2; i < 1000; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
	}
	return fmt.Sprintf("%s-%d", base, os.Getpid())
}

// ProvisionResult reports where a project's workspace was created.
type ProvisionResult struct {
	WorkspacePath string
	RepoURL       string
	CloneOutput   string
}

// ProvisionWorkspace creates the shared workspace directory for a project.
//
//   - If repoURL is empty, it makes a fresh empty directory under
//     ~/Projects/<slug-of-name>.
//   - If repoURL is set, it shallow-clones that repo into ~/Projects (folder
//     named from the project, falling back to the repo name) and uses the
//     clone directory as the workspace.
//
// The returned WorkspacePath is the resolved absolute directory.
func ProvisionWorkspace(name, repoURL string) (ProvisionResult, error) {
	root := projectsRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return ProvisionResult{}, fmt.Errorf("create projects root: %w", err)
	}

	repoURL = strings.TrimSpace(repoURL)
	if repoURL != "" {
		if !git.IsSafeRepoURL(repoURL) {
			return ProvisionResult{}, fmt.Errorf("invalid repository URL")
		}
		dest, out, err := git.Clone(repoURL, name, root)
		if err != nil {
			return ProvisionResult{CloneOutput: out}, fmt.Errorf("clone failed: %w", err)
		}
		return ProvisionResult{WorkspacePath: dest, RepoURL: repoURL, CloneOutput: out}, nil
	}

	dest := uniqueDir(filepath.Join(root, slugify(name)))
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return ProvisionResult{}, fmt.Errorf("create project workspace: %w", err)
	}
	return ProvisionResult{WorkspacePath: dest}, nil
}
