// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package opencodeembed carries an optional, build-time-supplied copy of the
// OpenCode release tarball so a workspace with no egress to github.com can
// still get a working opencode binary.
//
// The asset is deliberately NOT committed: `asset/` holds only a .gitkeep in
// source control, and the release build drops the real tarball in before
// `go build` (see Dockerfile.release). A plain `go build` therefore still works
// and simply yields a binary with no embedded fallback — Has() reports false and
// the installer stays network-only.
//
// Assets are keyed by the installer's target string (e.g. "linux-x64",
// "linux-arm64", "linux-x64-musl") so an image only ever falls back to a
// tarball that actually matches the platform it is running on.
package opencodeembed

import (
	"embed"
	"fmt"
	"io/fs"
)

// all: is required so the .gitkeep placeholder still satisfies the pattern when
// no real asset has been staged.
//
//go:embed all:asset
var assetFS embed.FS

// Name returns the asset filename the given installer target maps to.
func Name(target string) string {
	return fmt.Sprintf("opencode-%s.tar.gz", target)
}

// Tarball returns the embedded tarball bytes for target, and whether one was
// built into this binary.
func Tarball(target string) ([]byte, bool) {
	if target == "" {
		return nil, false
	}
	b, err := assetFS.ReadFile("asset/" + Name(target))
	if err != nil || len(b) == 0 {
		return nil, false
	}
	return b, true
}

// Has reports whether a fallback tarball for target is embedded.
func Has(target string) bool {
	_, ok := Tarball(target)
	return ok
}

// Targets lists the targets this binary carries a fallback for. Useful for
// startup logging and tests.
func Targets() []string {
	entries, err := fs.ReadDir(assetFS, "asset")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		// Skip the placeholder and anything not shaped like an asset.
		if len(name) < len("opencode-.tar.gz") {
			continue
		}
		if name[:len("opencode-")] != "opencode-" {
			continue
		}
		if name[len(name)-len(".tar.gz"):] != ".tar.gz" {
			continue
		}
		out = append(out, name[len("opencode-"):len(name)-len(".tar.gz")])
	}
	return out
}
