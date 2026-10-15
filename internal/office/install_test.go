// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package office

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeBundle builds a tarball of real .deb files named like the LibreOffice
// bundle's. The main package carries a launcher that prints version.
func fakeBundle(t *testing.T, version string, skip string) []byte {
	t.Helper()
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("the install is for x86-64 Linux")
	}
	if _, err := exec.LookPath("dpkg-deb"); err != nil {
		t.Skip("dpkg-deb not installed")
	}
	work := t.TempDir()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, data []byte) {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg})
		tw.Write(data)
	}
	for _, pkg := range append(packages, "libreoffice26.2-dict-en") {
		if pkg == skip {
			continue
		}
		root := filepath.Join(work, pkg)
		os.MkdirAll(filepath.Join(root, "DEBIAN"), 0o755)
		os.WriteFile(filepath.Join(root, "DEBIAN", "control"), []byte("Package: "+pkg+"\nVersion: 26.2.6\nArchitecture: amd64\nMaintainer: test <test@example.com>\nDescription: test\n"), 0o644)
		payload := filepath.Join(root, "opt", optDir, "share", pkg)
		os.MkdirAll(filepath.Dir(payload), 0o755)
		os.WriteFile(payload, []byte(pkg), 0o644)
		if pkg == "libreoffice26.2" {
			bin := filepath.Join(root, "opt", optDir, "program", "soffice")
			os.MkdirAll(filepath.Dir(bin), 0o755)
			os.WriteFile(bin, []byte("#!/bin/sh\necho '"+version+"'\n"), 0o755)
		}
		deb := filepath.Join(work, pkg+"_26.2.6-1_amd64.deb")
		if out, err := exec.Command("dpkg-deb", "--build", "--root-owner-group", root, deb).CombinedOutput(); err != nil {
			t.Fatalf("dpkg-deb --build: %v %s", err, out)
		}
		data, _ := os.ReadFile(deb)
		add("LibreOffice_26.2.6.3_Linux_x86-64_deb/DEBS/"+filepath.Base(deb), data)
	}
	add("LibreOffice_26.2.6.3_Linux_x86-64_deb/readmes/README_en-US", []byte("readme"))
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func serve(t *testing.T, body []byte, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/bundle.tar.gz"
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// wait runs an install to completion and returns its final status.
func wait(t *testing.T, in *Installer) InstallStatus {
	t.Helper()
	if !in.Start() {
		t.Fatal("Start didn't start an install")
	}
	deadline := time.Now().Add(60 * time.Second)
	for in.Status().Installing {
		if time.Now().After(deadline) {
			t.Fatal("the install didn't finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return in.Status()
}

func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(filepath.Dir(dir), ".libreoffice-install-*"))
	return m
}

func TestInstallUnpacksTheChosenPackages(t *testing.T) {
	bundle := fakeBundle(t, "LibreOffice 26.2.6.3", "")
	dir := filepath.Join(t.TempDir(), "libreoffice")
	in := &Installer{Dir: dir, URL: serve(t, bundle, 200), SHA256: sum(bundle)}
	if st := wait(t, in); st.Error != "" {
		t.Fatalf("install failed: %s", st.Error)
	}
	if got := find(dir); got != installedBin(dir) {
		t.Errorf("find = %q, want the installed launcher", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "opt", optDir, "share", "libobasis26.2-calc")); err != nil {
		t.Error("a chosen package wasn't unpacked")
	}
	if _, err := os.Stat(filepath.Join(dir, "opt", optDir, "share", "libreoffice26.2-dict-en")); err == nil {
		t.Error("a package outside the list was unpacked")
	}
	if l := leftovers(t, dir); len(l) > 0 {
		t.Errorf("staging left behind: %v", l)
	}
}

func TestInstallRefusesADownloadThatDoesNotMatchItsChecksum(t *testing.T) {
	bundle := fakeBundle(t, "LibreOffice 26.2.6.3", "")
	dir := filepath.Join(t.TempDir(), "libreoffice")
	in := &Installer{Dir: dir, URL: serve(t, bundle, 200), SHA256: strings.Repeat("0", 64)}
	st := wait(t, in)
	if !strings.Contains(st.Error, "damaged on each of 3 tries") {
		t.Fatalf("error = %q, want the damaged download reported", st.Error)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("something was installed")
	}
	if l := leftovers(t, dir); len(l) > 0 {
		t.Errorf("staging left behind: %v", l)
	}
}

// One mirror serving a bad copy costs a retry, not the install.
func TestInstallRetriesADamagedDownload(t *testing.T) {
	bundle := fakeBundle(t, "LibreOffice 26.2.6.3", "")
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.Write(bytes.Repeat([]byte{0}, len(bundle)))
			return
		}
		w.Write(bundle)
	}))
	t.Cleanup(srv.Close)
	dir := filepath.Join(t.TempDir(), "libreoffice")
	in := &Installer{Dir: dir, URL: srv.URL, SHA256: sum(bundle)}
	if st := wait(t, in); st.Error != "" {
		t.Fatalf("install failed: %s", st.Error)
	}
	if requests != 2 {
		t.Errorf("downloaded %d times, want 2", requests)
	}
	if find(dir) == "" {
		t.Error("nothing was installed")
	}
}

func TestInstallNamesAMissingPackage(t *testing.T) {
	bundle := fakeBundle(t, "LibreOffice 26.2.6.3", "libobasis26.2-writer")
	dir := filepath.Join(t.TempDir(), "libreoffice")
	in := &Installer{Dir: dir, URL: serve(t, bundle, 200), SHA256: sum(bundle)}
	if st := wait(t, in); !strings.Contains(st.Error, "libobasis26.2-writer") {
		t.Fatalf("error = %q, want the missing package named", st.Error)
	}
}

// A build that doesn't start never replaces a working install.
func TestInstallKeepsTheOldCopyWhenTheNewOneDoesNotStart(t *testing.T) {
	good := fakeBundle(t, "LibreOffice 26.2.6.3", "")
	dir := filepath.Join(t.TempDir(), "libreoffice")
	if st := wait(t, &Installer{Dir: dir, URL: serve(t, good, 200), SHA256: sum(good)}); st.Error != "" {
		t.Fatal(st.Error)
	}
	bad := fakeBundle(t, "something else", "")
	st := wait(t, &Installer{Dir: dir, URL: serve(t, bad, 200), SHA256: sum(bad)})
	if !strings.Contains(st.Error, "doesn't start") {
		t.Fatalf("error = %q, want the start check to fail", st.Error)
	}
	if out, err := exec.Command(installedBin(dir)).Output(); err != nil || !strings.Contains(string(out), "LibreOffice") {
		t.Errorf("the previous install was replaced: %q %v", out, err)
	}
}

func TestInstallReportsAnHTTPError(t *testing.T) {
	fakeBundle(t, "", "") // skips where installing can't run
	dir := filepath.Join(t.TempDir(), "libreoffice")
	in := &Installer{Dir: dir, URL: serve(t, []byte("gone"), 404), SHA256: strings.Repeat("0", 64)}
	if st := wait(t, in); !strings.Contains(st.Error, "404") {
		t.Fatalf("error = %q, want the HTTP status", st.Error)
	}
}

func TestStartRunsOneInstallAtATime(t *testing.T) {
	bundle := fakeBundle(t, "LibreOffice 26.2.6.3", "")
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.Write(bundle)
	}))
	t.Cleanup(srv.Close)
	in := &Installer{Dir: filepath.Join(t.TempDir(), "libreoffice"), URL: srv.URL, SHA256: sum(bundle)}
	if !in.Start() {
		t.Fatal("the first Start didn't start")
	}
	if in.Start() {
		t.Error("a second Start began another install while one was running")
	}
	if st := in.Status(); !st.Installing || st.Stage == "" {
		t.Errorf("status while running = %+v", st)
	}
	close(release)
	for deadline := time.Now().Add(60 * time.Second); in.Status().Installing; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the install didn't finish")
		}
	}
	if st := in.Status(); st.Error != "" {
		t.Errorf("install failed: %s", st.Error)
	}
}
