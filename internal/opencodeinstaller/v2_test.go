// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeinstaller

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// npmTarGz is an npm package tarball holding body as package/bin/opencode, as
// @opencode/cli-<target> ships it.
func npmTarGz(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range map[string]string{"package/package.json": `{"name":"@opencode/cli-x"}`, "package/bin/opencode": body} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// serveNpm serves body for every request and pins it as the 2.x tarball for
// the target the test runs on.
func serveNpm(t *testing.T, body []byte, pin []byte, paths *[]string) *httptest.Server {
	t.Helper()
	target, err := detectTarget()
	if err != nil {
		t.Skipf("unsupported target: %v", err)
	}
	oldPins, oldRegistry := pinnedSHA256v2, npmRegistry
	pinnedSHA256v2 = map[string]string{target: sha256Hex(pin)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if paths != nil {
			*paths = append(*paths, r.URL.Path)
		}
		w.Write(body)
	}))
	npmRegistry = srv.URL
	t.Cleanup(func() { srv.Close(); pinnedSHA256v2, npmRegistry = oldPins, oldRegistry })
	return srv
}

func TestInstall_V2ComesFromNpm(t *testing.T) {
	tgz := npmTarGz(t, "#!/bin/sh\necho 'opencode v2.2.5'\n")
	var paths []string
	srv := serveNpm(t, tgz, tgz, &paths)
	dir := t.TempDir()
	bin, err := Install(&bytes.Buffer{}, InstallOptions{InstallDir: dir, HTTPClient: srv.Client(), Major: 2})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	target, _ := detectTarget()
	want := "/@opencode/cli-" + target + "/-/cli-" + target + "-2.2.5.tgz"
	if len(paths) != 1 || paths[0] != want {
		t.Fatalf("requested %v, want [%s]", paths, want)
	}
	if body, _ := os.ReadFile(bin); !strings.Contains(string(body), "v2.2.5") {
		t.Fatalf("installed %q", body)
	}
}

func TestInstall_V2RejectsAMismatch(t *testing.T) {
	genuine := npmTarGz(t, "#!/bin/sh\necho genuine\n")
	srv := serveNpm(t, npmTarGz(t, "#!/bin/sh\necho tampered\n"), genuine, nil)
	swapEmbedded(t, func(string) ([]byte, bool) { return nil, false })
	dir := t.TempDir()
	_, err := Install(&bytes.Buffer{}, InstallOptions{InstallDir: dir, HTTPClient: srv.Client(), Major: 2})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected a checksum mismatch, got %v", err)
	}
	if _, statErr := os.Stat(dir + "/opencode"); !os.IsNotExist(statErr) {
		t.Fatalf("installed a tarball that failed its checksum (stat: %v)", statErr)
	}
}

func TestEnsure_V2KeepsThePinnedVersionAndReplacesA1x(t *testing.T) {
	var paths []string
	tgz := npmTarGz(t, "#!/bin/sh\necho 'opencode v2.2.5'\n")
	serveNpm(t, tgz, tgz, &paths)

	current := fakeOpencode(t, "opencode v2.2.5")
	res, err := Ensure(&bytes.Buffer{}, EnsureOptions{CurrentBin: current, InstallDir: t.TempDir(), Major: 2})
	if err != nil || res.Installed || len(paths) != 0 || res.ToVersion != PinnedVersion2 {
		t.Fatalf("pinned 2.x: %+v, %v, requests %v", res, err, paths)
	}

	res, err = Ensure(&bytes.Buffer{}, EnsureOptions{CurrentBin: fakeOpencode(t, "1.19.4"), InstallDir: t.TempDir(), Major: 2})
	if err != nil || !res.Installed || res.FromVersion != "v1.19.4" || res.ToVersion != PinnedVersion2 {
		t.Fatalf("1.x with 2.x asked for: %+v, %v", res, err)
	}
}

func TestPinned(t *testing.T) {
	if Pinned(0) != PinnedVersion || Pinned(1) != PinnedVersion || Pinned(2) != PinnedVersion2 {
		t.Fatalf("Pinned = %s, %s, %s", Pinned(0), Pinned(1), Pinned(2))
	}
	for _, target := range []string{"linux-x64", "linux-x64-musl", "linux-arm64", "linux-arm64-musl"} {
		if len(pinnedSHA256v2[target]) != 64 {
			t.Errorf("no 2.x pin for %s", target)
		}
	}
}
