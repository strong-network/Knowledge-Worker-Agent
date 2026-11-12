// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodeinstaller"
)

func TestOpencodeMajorSetting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KWA_HOME", home)
	type want struct {
		major int
		set   bool
	}
	check := func(stage string, cases map[string]want) {
		for v, w := range cases {
			t.Setenv("KWA_OPENCODE_MAJOR", v)
			if major, set := opencodeMajorSetting(); major != w.major || set != w.set {
				t.Errorf("%s, KWA_OPENCODE_MAJOR=%q: %d, %v, want %d, %v", stage, v, major, set, w.major, w.set)
			}
		}
	}
	check("before the migration", map[string]want{"": {1, false}, "1": {1, true}, "2": {2, true}, " 2 ": {2, true}, "3": {1, false}, "two": {1, false}})

	if err := os.MkdirAll(filepath.Dir(layout.CompletionFile()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.CompletionFile(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	check("after the migration", map[string]want{"": {2, false}, "1": {1, true}, "2": {2, true}, "3": {2, false}})
}

func TestUseOpencode1(t *testing.T) {
	bin1 := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(bin1, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KWA_OPENCODE_BIN", bin1)
	prevMajor, prevSet, prevBin := OpencodeMajor, OpencodeMajorSet, OpencodeBin
	t.Cleanup(func() { OpencodeMajor, OpencodeMajorSet, OpencodeBin = prevMajor, prevSet, prevBin })

	OpencodeMajor, OpencodeMajorSet, OpencodeBin = 2, true, "/v2/opencode"
	if UseOpencode1(); OpencodeMajor != 2 || OpencodeBin != "/v2/opencode" {
		t.Errorf("KWA_OPENCODE_MAJOR=2: switched to %d, %s", OpencodeMajor, OpencodeBin)
	}
	OpencodeMajorSet = false
	if UseOpencode1(); OpencodeMajor != 1 || OpencodeBin != bin1 {
		t.Errorf("by default: %d, %s", OpencodeMajor, OpencodeBin)
	}
}

// 2.x goes in its own folder, so 1.x stays for going back to it.
func TestOpencodeInstallDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KWA_OPENCODE_INSTALL_DIR", dir)
	prev := OpencodeMajor
	t.Cleanup(func() { OpencodeMajor = prev })

	OpencodeMajor = 1
	if got := OpencodeInstallDir(); got != dir {
		t.Errorf("1.x: %s, want %s", got, dir)
	}
	OpencodeMajor = 2
	if got := OpencodeInstallDir(); got != filepath.Join(dir, "v2") {
		t.Errorf("2.x: %s", got)
	}
}

func TestInitSelectsV2BinaryWhenMajor2IsConfigured(t *testing.T) {
	home := t.TempDir()
	installRoot := filepath.Join(home, ".opencode", "bin")
	legacyBin := filepath.Join(installRoot, "opencode")
	v2Bin := filepath.Join(installRoot, "v2", "opencode")
	for _, bin := range []struct {
		path    string
		version string
	}{
		{legacyBin, "1.19.4"},
		{v2Bin, "2.2.5"},
	} {
		if err := os.MkdirAll(filepath.Dir(bin.path), 0o755); err != nil {
			t.Fatal(err)
		}
		contents := []byte("#!/bin/sh\nprintf '%s\\n' 'opencode v" + bin.version + "'\n")
		if err := os.WriteFile(bin.path, contents, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("HOME", home)
	t.Setenv("KWA_HOME", filepath.Join(home, "kwa"))
	t.Setenv("KWA_OPENCODE_INSTALL_DIR", installRoot)
	t.Setenv("KWA_OPENCODE_MAJOR", "2")
	t.Setenv("KWA_OPENCODE_BIN", "")

	prevMajor, prevSet, prevBin := OpencodeMajor, OpencodeMajorSet, OpencodeBin
	prevHost, prevPort, prevWorkspace, prevDBPath := Host, Port, Workspace, DBPath
	prevAgentsDir, prevOwnerName, prevOwnerEmail, prevBackend := OpencodeAgentsDir, OwnerFullName, OwnerEmail, DefaultBackend
	prevHygieneEnabled := HygieneEnabled
	prevReviewAfter, prevSnooze, prevEmptyAfter := HygieneReviewAfter, HygieneSnooze, HygieneEmptyAfter
	t.Cleanup(func() {
		OpencodeMajor, OpencodeMajorSet, OpencodeBin = prevMajor, prevSet, prevBin
		Host, Port, Workspace, DBPath = prevHost, prevPort, prevWorkspace, prevDBPath
		OpencodeAgentsDir, OwnerFullName, OwnerEmail, DefaultBackend = prevAgentsDir, prevOwnerName, prevOwnerEmail, prevBackend
		HygieneEnabled = prevHygieneEnabled
		HygieneReviewAfter, HygieneSnooze, HygieneEmptyAfter = prevReviewAfter, prevSnooze, prevEmptyAfter
	})

	Init()
	if OpencodeMajor != 2 {
		t.Fatalf("selected major = %d, want 2", OpencodeMajor)
	}
	if OpencodeBin != v2Bin {
		t.Fatalf("selected binary = %q, want %q", OpencodeBin, v2Bin)
	}
	if got := opencodeinstaller.InstalledVersion(OpencodeBin); got != opencodeinstaller.PinnedVersion2 {
		t.Fatalf("selected binary version = %q, want %s", got, opencodeinstaller.PinnedVersion2)
	}
}
