// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeinstaller

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildDownloadURL_Pinned(t *testing.T) {
	url, _ := buildDownloadURL("1.0.180", "linux-arm64")
	if url != "https://github.com/anomalyco/opencode/releases/download/v1.0.180/opencode-linux-arm64.tar.gz" {
		t.Errorf("unexpected url: %s", url)
	}
	url2, _ := buildDownloadURL("v1.0.181", "darwin-arm64")
	if url2 != "https://github.com/anomalyco/opencode/releases/download/v1.0.181/opencode-darwin-arm64.tar.gz" {
		t.Errorf("unexpected url: %s", url2)
	}
}

func TestExtractSemver(t *testing.T) {
	cases := map[string]string{
		"v1.0.180":                "v1.0.180",
		"1.0.180":                 "v1.0.180",
		"opencode 1.2.3 (built)":  "v1.2.3",
		"":                        "",
		"no version here":         "",
		"name version: v0.1.2 (rc)": "v0.1.2",
	}
	for in, want := range cases {
		if got := extractSemver(in); got != want {
			t.Errorf("extractSemver(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestExtract_FlatTarball verifies the extractor pulls a top-level opencode
// binary out and chmod-execs it.
func TestExtract_FlatTarball(t *testing.T) {
	tmp := t.TempDir()
	tarPath := filepath.Join(tmp, "test.tar.gz")

	body := []byte("#!/bin/sh\necho fake\n")

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "opencode", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()

	if err := os.WriteFile(tarPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := extractOpencodeTarGz(tarPath, dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "opencode"))
	if err != nil {
		t.Fatalf("expected opencode binary: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("content mismatch")
	}
}

// TestExtract_NestedTarball verifies the extractor flattens entries like
// "opencode-linux-x64/opencode" → destDir/opencode.
func TestExtract_NestedTarball(t *testing.T) {
	tmp := t.TempDir()
	tarPath := filepath.Join(tmp, "test.tar.gz")

	body := []byte("nested-binary")

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "opencode-linux-x64/", Mode: 0o755, Typeflag: tar.TypeDir})
	_ = tw.WriteHeader(&tar.Header{Name: "opencode-linux-x64/opencode", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write(body)
	tw.Close()
	gz.Close()

	if err := os.WriteFile(tarPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := extractOpencodeTarGz(tarPath, dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "opencode")); err != nil {
		t.Fatalf("expected destDir/opencode: %v", err)
	}
}
