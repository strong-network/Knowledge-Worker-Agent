// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import "testing"

func TestLineChanges(t *testing.T) {
	for _, tc := range []struct {
		name, input, metadata string
		added, removed        int
	}{
		{"opencode's diff counts", `{"oldString":"a","newString":"b"}`,
			`{"files":[{"file":"x","additions":4,"deletions":2},{"file":"y","additions":1,"deletions":0}]}`, 5, 2},
		{"filediff", `{}`, `{"filediff":{"additions":3,"deletions":1}}`, 3, 1},
		{"an edit", `{"filePath":"x","oldString":"one\ntwo\nthree\n","newString":"one\n2\nthree\nfour\n"}`, ``, 2, 1},
		{"an edit that only removes", `{"oldString":"a\nb\nc","newString":"a\nc"}`, `{"diagnostics":{}}`, 0, 1},
		{"a write", `{"filePath":"x","content":"a\nb\nc\n"}`, ``, 3, 0},
		{"a patch", `{"patchText":"--- a/x\n+++ b/x\n@@ -1 +1,2 @@\n-old\n+new\n+more\n"}`, ``, 2, 1},
		{"a read", `{"path":"x"}`, ``, 0, 0},
		{"broken input", `[1`, ``, 0, 0},
	} {
		a, r := lineChanges([]byte(tc.input), []byte(tc.metadata))
		if a != tc.added || r != tc.removed {
			t.Errorf("%s: got +%d -%d, want +%d -%d", tc.name, a, r, tc.added, tc.removed)
		}
	}
}
