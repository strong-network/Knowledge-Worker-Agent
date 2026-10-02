// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package env

import (
	"slices"
	"strings"
	"testing"
)

func clearAll(t *testing.T) {
	t.Helper()
	for name, olds := range oldNames {
		for _, n := range append([]string{name}, olds...) {
			t.Setenv(n, "")
		}
	}
	for _, n := range ReplacedByHome {
		t.Setenv(n, "")
	}
}

func TestGetPrefersTheNewName(t *testing.T) {
	clearAll(t)
	if got := Get("KWA_OPENCODE_AUTO_UPDATE"); got != "" {
		t.Fatalf("nothing set: %q", got)
	}
	t.Setenv("COPILOT_AUTO_UPDATE", "c")
	if got := Get("KWA_OPENCODE_AUTO_UPDATE"); got != "c" {
		t.Errorf("only the oldest name set: %q", got)
	}
	t.Setenv("OPENCODE_AUTO_UPDATE", "o")
	if got := Get("KWA_OPENCODE_AUTO_UPDATE"); got != "o" {
		t.Errorf("both old names set: %q, want the first", got)
	}
	t.Setenv("KWA_OPENCODE_AUTO_UPDATE", " ")
	if got := Get("KWA_OPENCODE_AUTO_UPDATE"); got != "o" {
		t.Errorf("new name blank: %q, want an old name's value", got)
	}
	t.Setenv("KWA_OPENCODE_AUTO_UPDATE", "k")
	if got := Get("KWA_OPENCODE_AUTO_UPDATE"); got != "k" {
		t.Errorf("all set: %q, want the new name's value", got)
	}
}

func TestGetReadsANameWithNoOldOnes(t *testing.T) {
	t.Setenv("KWA_HOME", "/somewhere")
	if got := Get("KWA_HOME"); got != "/somewhere" {
		t.Errorf("Get(KWA_HOME) = %q", got)
	}
	if got := Names("KWA_HOME"); !slices.Equal(got, []string{"KWA_HOME"}) {
		t.Errorf("Names(KWA_HOME) = %q", got)
	}
}

func TestOldInUse(t *testing.T) {
	clearAll(t)
	if got := OldInUse(); len(got) != 0 {
		t.Fatalf("nothing set: %q", got)
	}
	t.Setenv("SDS_PROJECT_ID", "p")
	t.Setenv("COPILOT_DB_PATH", "/home/developer/.copilot-web.db")
	t.Setenv("KWA_PORT", "8765")
	want := []string{"COPILOT_DB_PATH (KWA_HOME replaces it)", "SDS_PROJECT_ID (now KWA_PROJECT_ID)"}
	if got := OldInUse(); !slices.Equal(got, want) {
		t.Errorf("OldInUse() = %q, want %q", got, want)
	}
}

func TestEveryNameIsUsedOnce(t *testing.T) {
	seen := map[string]string{}
	use := func(n, as string) {
		if prev, ok := seen[n]; ok {
			t.Errorf("%s is %s and %s", n, prev, as)
		}
		seen[n] = as
	}
	for name, olds := range oldNames {
		if !strings.HasPrefix(name, "KWA_") {
			t.Errorf("%s doesn't start with KWA_", name)
		}
		use(name, "a new name")
		for _, old := range olds {
			if strings.HasPrefix(old, "KWA_") {
				t.Errorf("old name %s starts with KWA_", old)
			}
			use(old, "an old name of "+name)
		}
	}
	for _, old := range ReplacedByHome {
		use(old, "replaced by KWA_HOME")
	}
}
