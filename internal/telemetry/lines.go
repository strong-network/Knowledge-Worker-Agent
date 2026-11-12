// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"encoding/json"
	"strings"
)

// lineChanges counts the lines a successful tool call added and removed. It
// takes opencode's own diff counts when the event carries them, and otherwise
// reads the tool's input: an edit's old and new text, a write's content, or a
// patch's +/- lines. Tools that change no file count nothing.
func lineChanges(input, metadata json.RawMessage) (added, removed int) {
	var meta struct {
		Files []struct {
			Additions *int `json:"additions"`
			Deletions *int `json:"deletions"`
		} `json:"files"`
		FileDiff *struct {
			Additions *int `json:"additions"`
			Deletions *int `json:"deletions"`
		} `json:"filediff"`
	}
	if len(metadata) > 0 && json.Unmarshal(metadata, &meta) == nil {
		found := false
		for _, f := range meta.Files {
			if f.Additions != nil && f.Deletions != nil {
				added, removed, found = added+*f.Additions, removed+*f.Deletions, true
			}
		}
		if d := meta.FileDiff; !found && d != nil && d.Additions != nil && d.Deletions != nil {
			added, removed, found = *d.Additions, *d.Deletions, true
		}
		if found {
			return added, removed
		}
	}

	var in struct {
		OldString *string `json:"oldString"`
		NewString *string `json:"newString"`
		Content   *string `json:"content"`
		PatchText *string `json:"patchText"`
		Patch     *string `json:"patch"`
	}
	if len(input) == 0 || json.Unmarshal(input, &in) != nil {
		return 0, 0
	}
	switch {
	case in.OldString != nil && in.NewString != nil:
		return diffLines(splitLines(*in.OldString), splitLines(*in.NewString))
	case in.Content != nil:
		return len(splitLines(*in.Content)), 0
	case in.PatchText != nil:
		return patchLines(*in.PatchText)
	case in.Patch != nil:
		return patchLines(*in.Patch)
	}
	return 0, 0
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// maxDiffLines bounds the quadratic diff; past it, every line counts as
// changed.
const maxDiffLines = 2000

// diffLines counts the lines only in b (added) and only in a (removed), by
// longest common subsequence.
func diffLines(a, b []string) (added, removed int) {
	for len(a) > 0 && len(b) > 0 && a[0] == b[0] {
		a, b = a[1:], b[1:]
	}
	for len(a) > 0 && len(b) > 0 && a[len(a)-1] == b[len(b)-1] {
		a, b = a[:len(a)-1], b[:len(b)-1]
	}
	if len(a) == 0 || len(b) == 0 || len(a) > maxDiffLines || len(b) > maxDiffLines {
		return len(b), len(a)
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := range a {
		for j := range b {
			switch {
			case a[i] == b[j]:
				cur[j+1] = prev[j] + 1
			case prev[j+1] >= cur[j]:
				cur[j+1] = prev[j+1]
			default:
				cur[j+1] = cur[j]
			}
		}
		prev, cur = cur, prev
	}
	common := prev[len(b)]
	return len(b) - common, len(a) - common
}

// patchLines counts a patch's added and removed lines, skipping file headers.
func patchLines(p string) (added, removed int) {
	for _, l := range strings.Split(p, "\n") {
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
		case strings.HasPrefix(l, "+"):
			added++
		case strings.HasPrefix(l, "-"):
			removed++
		}
	}
	return added, removed
}
