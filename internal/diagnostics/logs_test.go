// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package diagnostics

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeLines(t *testing.T, path string, from, to int) {
	t.Helper()
	var b strings.Builder
	for i := from; i <= to; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func lines(from, to int) []string {
	var out []string
	for i := from; i <= to; i++ {
		out = append(out, fmt.Sprintf("line %d", i))
	}
	return out
}

func TestTailSpansTheRotatedFile(t *testing.T) {
	log := filepath.Join(t.TempDir(), "kwa.log")
	writeLines(t, log+".1", 1, 5)
	writeLines(t, log, 6, 8)
	Configure(Sources{LogFile: log})

	all := Tail(LogKWA, 100, 1<<20)
	if !reflect.DeepEqual(all.Lines, lines(1, 8)) || all.Truncated || len(all.Paths) != 2 {
		t.Fatalf("all lines: %+v", all)
	}
	last := Tail(LogKWA, 4, 1<<20)
	if !reflect.DeepEqual(last.Lines, lines(5, 8)) || !last.Truncated {
		t.Fatalf("last 4: %+v", last)
	}
}

func TestTailByteBudgetDropsThePartialLine(t *testing.T) {
	log := filepath.Join(t.TempDir(), "kwa.log")
	writeLines(t, log, 1, 100) // "line 100\n" is 9 bytes
	Configure(Sources{LogFile: log})
	got := Tail(LogKWA, 1000, 20)
	if !reflect.DeepEqual(got.Lines, lines(99, 100)) || !got.Truncated {
		t.Fatalf("budget 20 bytes: %+v", got)
	}
}

func TestTailRedactsAndHandlesALogTurnedOff(t *testing.T) {
	log := filepath.Join(t.TempDir(), "kwa.log")
	if err := os.WriteFile(log, []byte("Authorization: Bearer abcdefghijklmnop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	Configure(Sources{LogFile: log})
	if got := Tail(LogKWA, 10, 1<<20); len(got.Lines) != 1 || strings.Contains(got.Lines[0], "abcdefghijklmnop") {
		t.Fatalf("not redacted: %+v", got)
	}
	Configure(Sources{LogFile: "off"})
	if got := Tail(LogKWA, 10, 1<<20); len(got.Lines) != 0 || len(got.Paths) != 0 {
		t.Fatalf("log off: %+v", got)
	}
}

func TestOpencodeLogIsTheNewestFile(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	dir := filepath.Join(data, "opencode", "log")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old, newer := filepath.Join(dir, "a.log"), filepath.Join(dir, "b.log")
	writeLines(t, old, 1, 2)
	writeLines(t, newer, 3, 4)
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(newer, past, past); err != nil {
		t.Fatal(err)
	}
	got := Tail(LogOpencode, 10, 1<<20)
	if !reflect.DeepEqual(got.Paths, []string{old}) || !reflect.DeepEqual(got.Lines, lines(1, 2)) {
		t.Fatalf("opencode log: %+v", got)
	}
}
