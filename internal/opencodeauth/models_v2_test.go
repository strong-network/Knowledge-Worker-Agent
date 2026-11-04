// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeauth

import (
	"runtime"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodev2"
)

// opencode 2.x's `models` takes no provider, so KWA filters its list.
func TestFetchModelsFor_V2ListsAllAndFilters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub not supported on windows")
	}
	bin := writeFakeOpencode(t, `#!/bin/sh
case "$1" in --version) echo "opencode v2.2.5"; exit 0;; serve) exit 1;; esac
if [ "$1" = models ] && [ "$#" = 2 ] && [ "$2" = --standalone ]; then
  printf 'github-copilot/b\nopenai/x\ngithub-copilot/a\n'; exit 0
fi
echo "unexpected arguments: $*" >&2; exit 2
`)
	oldBin, oldCfg := OpencodeBin, config.OpencodeBin
	OpencodeBin, config.OpencodeBin = bin, bin
	t.Setenv("KWA_HOME", t.TempDir())
	defer func() { OpencodeBin, config.OpencodeBin = oldBin, oldCfg }()
	opencodev2.Detect(bin)

	got, err := FetchModelsFor("github-copilot")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "github-copilot/a,github-copilot/b" {
		t.Fatalf("got %q", got)
	}
}
