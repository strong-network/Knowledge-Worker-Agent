// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package office previews PowerPoint, Word and Excel files as PDF, converted
// by LibreOffice, and installs LibreOffice into the home folder on request.
//
// LibreOffice isn't part of the workspace image. An install into the system
// folders is lost when the workspace restarts, so this package unpacks a
// pinned LibreOffice build under the KWA folder instead, which persists.
// A LibreOffice already on the PATH is used when that install is absent.
package office

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// Extensions lists the file types the preview converts, lower case with the dot.
var Extensions = []string{".pptx", ".ppt", ".docx", ".doc", ".xlsx", ".xls"}

// Previewable reports whether name has one of Extensions.
func Previewable(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, e := range Extensions {
		if ext == e {
			return true
		}
	}
	return false
}

// installDir is where Install unpacks LibreOffice.
func installDir() string { return filepath.Join(layout.System(), "libreoffice") }

// cacheDir holds converted PDFs and LibreOffice's own profile.
func cacheDir() string { return filepath.Join(layout.System(), "office-previews") }

// installedBin is the launcher inside an install made by Install.
func installedBin(dir string) string {
	return filepath.Join(dir, "opt", optDir, "program", "soffice")
}

// Find returns the LibreOffice launcher to run, or "" when there is none:
// the copy Install made, else soffice or libreoffice on the PATH.
func Find() string { return find(installDir()) }

func find(dir string) string {
	if bin := installedBin(dir); isExecutable(bin) {
		return bin
	}
	for _, name := range []string{"soffice", "libreoffice"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// removeStale deletes leftovers of an install or conversion the server was
// stopped in the middle of: folders in dir matching pattern, older than an hour.
func removeStale(dir, pattern string) {
	matches, _ := filepath.Glob(filepath.Join(dir, pattern))
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && info.IsDir() && time.Since(info.ModTime()) > time.Hour {
			os.RemoveAll(m)
		}
	}
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}
