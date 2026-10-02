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
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodeembed"
)

// makeTarGz builds a flat opencode tarball whose binary contains body.
func makeTarGz(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: "opencode", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatalf("write body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

// TestExtractOpencodeTarGzFrom_Reader covers the reader-based extractor that the
// embedded fallback uses, independent of any file on disk.
func TestExtractOpencodeTarGzFrom_Reader(t *testing.T) {
	dest := t.TempDir()
	tgz := makeTarGz(t, "#!/bin/sh\necho embedded\n")

	if err := extractOpencodeTarGzFrom(bytes.NewReader(tgz), dest); err != nil {
		t.Fatalf("extract: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dest, "opencode"))
	if err != nil {
		t.Fatalf("read extracted binary: %v", err)
	}
	if !strings.Contains(string(got), "echo embedded") {
		t.Fatalf("unexpected extracted content: %q", got)
	}
	info, err := os.Stat(filepath.Join(dest, "opencode"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("extracted binary is not executable: %v", info.Mode())
	}
}

// TestInstall_FallsBackToEmbedded proves the whole contract: the download is
// attempted first, and when it fails the embedded tarball is installed into the
// same directory the network path would have used.
func TestInstall_FallsBackToEmbedded(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Error(w, "no egress", http.StatusBadGateway)
	}))
	defer srv.Close()
	swapReleasesBase(t, srv.URL)

	want := "#!/bin/sh\necho from-embedded\n"
	emb := makeTarGz(t, want)
	pinTarball(t, emb)
	swapEmbedded(t, func(target string) ([]byte, bool) {
		return emb, true
	})

	dir := t.TempDir()
	var logBuf bytes.Buffer
	bin, err := Install(&logBuf, InstallOptions{InstallDir: dir, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("Install with embedded fallback: %v", err)
	}

	if hits == 0 {
		t.Fatal("expected the network download to be attempted first")
	}
	if want, got := filepath.Join(dir, "opencode"), bin; got != want {
		t.Fatalf("installed to %q, want %q (same dir as the network path)", got, want)
	}
	body, err := os.ReadFile(bin)
	if err != nil {
		t.Fatalf("read installed binary: %v", err)
	}
	if !strings.Contains(string(body), "from-embedded") {
		t.Fatalf("installed binary is not the embedded one: %q", body)
	}
	if !strings.Contains(logBuf.String(), "Falling back to embedded") {
		t.Fatalf("expected fallback to be logged, got:\n%s", logBuf.String())
	}
}

// TestInstall_PrefersNetworkOverEmbedded asserts the embedded copy is only a
// backup: when the download succeeds, that is what gets installed.
func TestInstall_PrefersNetworkOverEmbedded(t *testing.T) {
	netBody := "#!/bin/sh\necho from-network\n"
	netTgz := makeTarGz(t, netBody)
	pinTarball(t, netTgz)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(netTgz)
	}))
	defer srv.Close()
	swapReleasesBase(t, srv.URL)

	var embeddedUsed bool
	swapEmbedded(t, func(target string) ([]byte, bool) {
		embeddedUsed = true
		return makeTarGz(t, "#!/bin/sh\necho from-embedded\n"), true
	})

	dir := t.TempDir()
	bin, err := Install(&bytes.Buffer{}, InstallOptions{InstallDir: dir, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if embeddedUsed {
		t.Fatal("embedded fallback was consulted even though the download succeeded")
	}
	body, err := os.ReadFile(bin)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(body), "from-network") {
		t.Fatalf("expected the downloaded binary, got %q", body)
	}
}

// TestInstall_FallsBackOnCorruptDownload covers a truncated/garbage body that
// downloads fine but fails to extract.
func TestInstall_FallsBackOnCorruptDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("this is not a gzip stream"))
	}))
	defer srv.Close()
	swapReleasesBase(t, srv.URL)
	emb := makeTarGz(t, "#!/bin/sh\necho from-embedded\n")
	pinTarball(t, emb)
	swapEmbedded(t, func(target string) ([]byte, bool) {
		return emb, true
	})

	dir := t.TempDir()
	bin, err := Install(&bytes.Buffer{}, InstallOptions{InstallDir: dir, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("expected fallback to rescue a corrupt download: %v", err)
	}
	body, _ := os.ReadFile(bin)
	if !strings.Contains(string(body), "from-embedded") {
		t.Fatalf("expected embedded binary after corrupt download, got %q", body)
	}
}

// TestInstall_NoEmbeddedFallbackFails asserts the error is actionable when the
// download fails and this build carries no embedded asset — which is the case
// for a plain `go build` (asset/ holds only .gitkeep).
func TestInstall_NoEmbeddedFallbackFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()
	swapReleasesBase(t, srv.URL)
	swapEmbedded(t, func(target string) ([]byte, bool) { return nil, false })
	pinTarball(t, makeTarGz(t, "#!/bin/sh\necho pinned\n"))

	dir := t.TempDir()
	_, err := Install(&bytes.Buffer{}, InstallOptions{
		InstallDir: dir,
		HTTPClient: srv.Client(),
	})
	if err == nil {
		t.Fatal("expected install to fail with no network and no embedded fallback")
	}
	if !strings.Contains(err.Error(), "no embedded") {
		t.Fatalf("expected a 'no embedded ... fallback' error, got: %v", err)
	}
}

func swapReleasesBase(t *testing.T, base string) {
	t.Helper()
	old := opencodeReleasesBase
	opencodeReleasesBase = base
	t.Cleanup(func() { opencodeReleasesBase = old })
}

func swapEmbedded(t *testing.T, fn func(string) ([]byte, bool)) {
	t.Helper()
	old := embeddedTarball
	embeddedTarball = fn
	t.Cleanup(func() { embeddedTarball = old })
}

// TestSourceBuildHasNoEmbeddedAsset documents that the committed tree ships no
// asset, so `go build` stays network-only.
func TestSourceBuildHasNoEmbeddedAsset(t *testing.T) {
	staged, _ := filepath.Glob(filepath.Join("..", "opencodeembed", "asset", "opencode-*.tar.gz"))
	if len(staged) > 0 {
		t.Skipf("asset staged (%v) — release build", staged)
	}
	target, err := detectTarget()
	if err != nil {
		t.Skipf("unsupported target: %v", err)
	}
	if opencodeembed.Has(target) {
		t.Fatalf("source build unexpectedly carries an embedded %s asset", target)
	}
}
