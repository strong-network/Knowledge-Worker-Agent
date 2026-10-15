// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package diagnostics

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Log sources the diagnostics can show.
const (
	LogKWA      = "kwa"
	LogOpencode = "opencode"
)

// LogTail is the end of a log, redacted.
type LogTail struct {
	Source string   `json:"source"`
	Paths  []string `json:"paths"`
	Lines  []string `json:"lines"`
	// Truncated is set when older lines exist that were not returned.
	Truncated bool `json:"truncated"`
}

// logFiles returns a source's files, oldest first.
func logFiles(source string) []string {
	switch source {
	case LogKWA:
		f := strings.TrimSpace(src.LogFile)
		if f == "" || f == "off" || f == "none" || f == "-" {
			return nil
		}
		return existing(f+".1", f)
	case LogOpencode:
		// opencode writes one file per run on some versions and one growing
		// file on others; the newest is the one in use.
		matches, _ := filepath.Glob(filepath.Join(opencodeDataDir(), "log", "*.log"))
		sort.Slice(matches, func(i, j int) bool { return modTime(matches[i]) < modTime(matches[j]) })
		if len(matches) == 0 {
			return nil
		}
		return matches[len(matches)-1:]
	}
	return nil
}

func existing(paths ...string) []string {
	var out []string
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

func modTime(p string) int64 {
	if info, err := os.Stat(p); err == nil {
		return info.ModTime().UnixNano()
	}
	return 0
}

// Tail returns up to maxLines of the source's newest lines, reading at most
// maxBytes from the end of its files.
func Tail(source string, maxLines int, maxBytes int64) LogTail {
	t := LogTail{Source: source, Paths: []string{}, Lines: []string{}}
	files := logFiles(source)
	t.Paths = append(t.Paths, files...)
	var lines []string
	budget := maxBytes
	for i := len(files) - 1; i >= 0 && len(lines) < maxLines && budget > 0; i-- {
		chunk, complete := readEnd(files[i], budget)
		budget -= int64(len(chunk))
		got := splitLines(chunk, complete)
		lines = append(got, lines...)
		if !complete {
			t.Truncated = true
			break
		}
		if i > 0 && budget <= 0 {
			t.Truncated = true
		}
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
		t.Truncated = true
	}
	for _, l := range lines {
		t.Lines = append(t.Lines, Redact(l))
	}
	return t
}

// readEnd returns up to n bytes from the end of path, and whether that is the
// whole file.
func readEnd(path string, n int64) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, true
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, true
	}
	size := info.Size()
	start := int64(0)
	if size > n {
		start = size - n
	}
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return nil, true
	}
	return buf, start == 0
}

// splitLines splits b into lines, dropping the first one when b starts
// mid-line.
func splitLines(b []byte, fromStart bool) []string {
	b = bytes.TrimRight(b, "\n")
	if len(b) == 0 {
		return nil
	}
	parts := strings.Split(string(b), "\n")
	if !fromStart && len(parts) > 0 {
		parts = parts[1:]
	}
	for i, p := range parts {
		parts[i] = strings.TrimRight(p, "\r")
	}
	return parts
}
