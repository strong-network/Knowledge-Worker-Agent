// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package obsidianinstaller

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTools puts fake npm and node first on PATH. npm logs its arguments and
// installs a runnable obsidian-mcp of the requested version, or fails when
// npmFails is set; node reports nodeVersion.
func fakeTools(t *testing.T, nodeVersion string, npmFails bool) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "npm.log")
	fail := ""
	if npmFails {
		fail = "exit 1\n"
	}
	npm := "#!/bin/sh\necho \"$@\" >> " + log + "\n" + fail +
		"prefix=\"$3\"; ver=\"${5#*@}\"\n" +
		"mkdir -p \"$prefix/bin\" \"$prefix/lib/node_modules/obsidian-mcp\"\n" +
		"printf '#!/bin/sh\\necho usage\\n' > \"$prefix/bin/obsidian-mcp\"; chmod +x \"$prefix/bin/obsidian-mcp\"\n" +
		"printf '{\"version\":\"%s\"}' \"$ver\" > \"$prefix/lib/node_modules/obsidian-mcp/package.json\"\n"
	for name, body := range map[string]string{"npm": npm, "node": "#!/bin/sh\necho v" + nodeVersion + "\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	return log
}

// existingInstall fakes a working obsidian-mcp that npm recorded as version.
func existingInstall(t *testing.T, version string) (installDir, bin string) {
	t.Helper()
	installDir = filepath.Join(t.TempDir(), "lib")
	bin = BinPath(installDir)
	pkg := filepath.Join(installDir, "lib", "node_modules", PackageName)
	for _, d := range []string{filepath.Dir(bin), pkg} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho usage\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"version":"`+version+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return installDir, bin
}

func npmCalls(t *testing.T, log string) string {
	t.Helper()
	b, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEnsureInstallsThePinnedVersion(t *testing.T) {
	log := fakeTools(t, "22.22.2", false)
	installDir := filepath.Join(t.TempDir(), "lib")

	res, err := Ensure(&bytes.Buffer{}, Options{InstallDir: installDir, VaultDir: t.TempDir()})
	if err != nil || !res.Installed || res.Bin != BinPath(installDir) {
		t.Fatalf("expected a fresh install, got %+v, err %v", res, err)
	}
	if calls := npmCalls(t, log); !strings.Contains(calls, "--prefix "+installDir+" -g "+PackageName+"@"+PinnedVersion) {
		t.Fatalf("npm not asked for %s@%s: %q", PackageName, PinnedVersion, calls)
	}
}

func TestEnsureLeavesThePinnedVersionAlone(t *testing.T) {
	log := fakeTools(t, "22.22.2", false)
	installDir, bin := existingInstall(t, PinnedVersion)

	res, err := Ensure(&bytes.Buffer{}, Options{InstallDir: installDir, VaultDir: t.TempDir()})
	if err != nil || res.Installed || res.Bin != bin {
		t.Fatalf("expected the pinned install kept, got %+v, err %v", res, err)
	}
	if calls := npmCalls(t, log); calls != "" {
		t.Fatalf("npm should not run for the pinned version, ran: %q", calls)
	}
}

func TestEnsureUpgradesAnotherVersionToThePin(t *testing.T) {
	log := fakeTools(t, "22.22.2", false)
	installDir, bin := existingInstall(t, "1.0.6")

	res, err := Ensure(&bytes.Buffer{}, Options{InstallDir: installDir, VaultDir: t.TempDir()})
	if err != nil || !res.Installed || res.Bin != bin {
		t.Fatalf("expected an upgrade, got %+v, err %v", res, err)
	}
	if !strings.Contains(npmCalls(t, log), PackageName+"@"+PinnedVersion) {
		t.Fatal("npm was not asked for the pinned version")
	}
	if v := installedVersion(installDir); v != PinnedVersion {
		t.Fatalf("installed version = %q, want %s", v, PinnedVersion)
	}
}

func TestEnsureKeepsAWorkingInstallWhenTheUpgradeFails(t *testing.T) {
	fakeTools(t, "22.22.2", true)
	installDir, bin := existingInstall(t, "1.0.6")

	res, err := Ensure(&bytes.Buffer{}, Options{InstallDir: installDir, VaultDir: t.TempDir()})
	if err != nil || res.Skipped || res.Bin != bin {
		t.Fatalf("expected the working install kept, got %+v, err %v", res, err)
	}
}

func TestEnsureNeedsNode22ForThePinnedVersion(t *testing.T) {
	t.Run("keeps a working install", func(t *testing.T) {
		log := fakeTools(t, "20.11.0", false)
		installDir, bin := existingInstall(t, "1.0.6")
		res, err := Ensure(&bytes.Buffer{}, Options{InstallDir: installDir, VaultDir: t.TempDir()})
		if err != nil || res.Skipped || res.Bin != bin {
			t.Fatalf("expected the working install kept, got %+v, err %v", res, err)
		}
		if calls := npmCalls(t, log); calls != "" {
			t.Fatalf("npm should not run on Node 20, ran: %q", calls)
		}
	})
	t.Run("skips a fresh install", func(t *testing.T) {
		fakeTools(t, "20.11.0", false)
		res, err := Ensure(&bytes.Buffer{}, Options{InstallDir: filepath.Join(t.TempDir(), "lib"), VaultDir: t.TempDir()})
		if err != nil || !res.Skipped || res.Bin != "" || !strings.Contains(res.SkipReason, "Node.js 22") {
			t.Fatalf("expected a skip that names Node.js 22, got %+v, err %v", res, err)
		}
	})
}
