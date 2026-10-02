// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeinstaller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// pinTarball makes tgz the pinned tarball for the target this test runs on.
func pinTarball(t *testing.T, tgz []byte) {
	t.Helper()
	target, err := detectTarget()
	if err != nil {
		t.Skipf("unsupported target: %v", err)
	}
	old := pinnedSHA256
	pinnedSHA256 = map[string]string{target: sha256Hex(tgz)}
	t.Cleanup(func() { pinnedSHA256 = old })
}

func serveBytes(t *testing.T, body []byte, paths *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if paths != nil {
			*paths = append(*paths, r.URL.Path)
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	swapReleasesBase(t, srv.URL)
	return srv
}

func TestInstall_DownloadsThePinnedVersion(t *testing.T) {
	tgz := makeTarGz(t, "#!/bin/sh\necho pinned\n")
	pinTarball(t, tgz)
	var paths []string
	srv := serveBytes(t, tgz, &paths)
	swapEmbedded(t, func(string) ([]byte, bool) { return nil, false })

	if _, err := Install(&bytes.Buffer{}, InstallOptions{InstallDir: t.TempDir(), HTTPClient: srv.Client()}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	target, _ := detectTarget()
	want := "/download/" + PinnedVersion + "/opencode-" + target + ".tar.gz"
	if len(paths) != 1 || paths[0] != want {
		t.Fatalf("requested %v, want [%s]", paths, want)
	}
}

func TestInstall_RejectsADownloadThatDoesNotMatchThePin(t *testing.T) {
	pinTarball(t, makeTarGz(t, "#!/bin/sh\necho genuine\n"))
	srv := serveBytes(t, makeTarGz(t, "#!/bin/sh\necho tampered\n"), nil)
	swapEmbedded(t, func(string) ([]byte, bool) { return nil, false })

	dir := t.TempDir()
	_, err := Install(&bytes.Buffer{}, InstallOptions{InstallDir: dir, HTTPClient: srv.Client()})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected a checksum mismatch error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "opencode")); !os.IsNotExist(statErr) {
		t.Fatalf("a tarball that failed its checksum was installed (stat: %v)", statErr)
	}
}

func TestInstall_MismatchedDownloadFallsBackToAMatchingEmbeddedCopy(t *testing.T) {
	emb := makeTarGz(t, "#!/bin/sh\necho genuine\n")
	pinTarball(t, emb)
	srv := serveBytes(t, makeTarGz(t, "#!/bin/sh\necho tampered\n"), nil)
	swapEmbedded(t, func(string) ([]byte, bool) { return emb, true })

	bin, err := Install(&bytes.Buffer{}, InstallOptions{InstallDir: t.TempDir(), HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if body, _ := os.ReadFile(bin); !strings.Contains(string(body), "genuine") {
		t.Fatalf("expected the embedded binary, got %q", body)
	}
}

func TestInstall_RejectsAnEmbeddedCopyThatDoesNotMatchThePin(t *testing.T) {
	pinTarball(t, makeTarGz(t, "#!/bin/sh\necho genuine\n"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no egress", http.StatusBadGateway)
	}))
	defer srv.Close()
	swapReleasesBase(t, srv.URL)
	swapEmbedded(t, func(string) ([]byte, bool) { return makeTarGz(t, "#!/bin/sh\necho other\n"), true })

	dir := t.TempDir()
	_, err := Install(&bytes.Buffer{}, InstallOptions{InstallDir: dir, HTTPClient: srv.Client()})
	if err == nil || !strings.Contains(err.Error(), "embedded") {
		t.Fatalf("expected an embedded checksum error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "opencode")); !os.IsNotExist(statErr) {
		t.Fatalf("an embedded tarball that failed its checksum was installed (stat: %v)", statErr)
	}
}

// fakeOpencode writes a script that answers --version like opencode does.
func fakeOpencode(t *testing.T, version string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho "+version+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestEnsure_KeepsThePinnedVersionWithoutDownloading(t *testing.T) {
	var paths []string
	serveBytes(t, nil, &paths)
	current := fakeOpencode(t, strings.TrimPrefix(PinnedVersion, "v"))

	res, err := Ensure(&bytes.Buffer{}, EnsureOptions{CurrentBin: current, InstallDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if res.Installed || len(paths) != 0 {
		t.Fatalf("expected no install and no download, got Installed=%v requests=%v", res.Installed, paths)
	}
	if res.Bin != current {
		t.Fatalf("Bin = %q, want the existing %q", res.Bin, current)
	}
}

func TestEnsure_ReplacesAnyOtherVersionWithThePin(t *testing.T) {
	tgz := makeTarGz(t, "#!/bin/sh\necho pinned\n")
	pinTarball(t, tgz)
	serveBytes(t, tgz, nil)
	swapEmbedded(t, func(string) ([]byte, bool) { return nil, false })
	dir := t.TempDir()

	res, err := Ensure(&bytes.Buffer{}, EnsureOptions{CurrentBin: fakeOpencode(t, "99.0.0"), InstallDir: dir})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !res.Installed || res.Bin != filepath.Join(dir, "opencode") {
		t.Fatalf("expected the pinned version installed into %s, got %+v", dir, res)
	}
	if res.FromVersion != "v99.0.0" || res.ToVersion != PinnedVersion {
		t.Fatalf("versions = %s -> %s, want v99.0.0 -> %s", res.FromVersion, res.ToVersion, PinnedVersion)
	}
}

func TestEnsure_KeepsAWorkingBinaryWhenThePinnedInstallFails(t *testing.T) {
	pinTarball(t, makeTarGz(t, "#!/bin/sh\necho genuine\n"))
	serveBytes(t, makeTarGz(t, "#!/bin/sh\necho tampered\n"), nil)
	swapEmbedded(t, func(string) ([]byte, bool) { return nil, false })
	current := fakeOpencode(t, "1.0.0")

	res, err := Ensure(&bytes.Buffer{}, EnsureOptions{CurrentBin: current, InstallDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Ensure should keep the working binary, got %v", err)
	}
	if res.Installed || res.Bin != current {
		t.Fatalf("expected the existing binary kept, got %+v", res)
	}
}

// The release image embeds the tarball Dockerfile.release downloads, and Install
// only accepts an embedded copy that matches the pin, so the two must agree.
func TestDockerfileReleaseMatchesThePin(t *testing.T) {
	df, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile.release"))
	if err != nil {
		t.Fatalf("read Dockerfile.release: %v", err)
	}
	arg := func(name string) string {
		m := regexp.MustCompile(`(?m)^ARG ` + name + `=(\S+)$`).FindSubmatch(df)
		if m == nil {
			t.Fatalf("Dockerfile.release has no ARG %s", name)
		}
		return string(m[1])
	}
	if got := arg("OPENCODE_VERSION"); got != PinnedVersion {
		t.Errorf("Dockerfile.release OPENCODE_VERSION = %s, installer pins %s", got, PinnedVersion)
	}
	for argName, target := range map[string]string{
		"OPENCODE_SHA256_LINUX_X64":   "linux-x64",
		"OPENCODE_SHA256_LINUX_ARM64": "linux-arm64",
	} {
		if got, want := arg(argName), pinnedSHA256[target]; got != want {
			t.Errorf("Dockerfile.release %s = %s, installer pins %s for %s", argName, got, want, target)
		}
	}
}
