// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package opencodeinstaller downloads and installs the OpenCode CLI from its
// official GitHub release artifacts (github.com/anomalyco/opencode, linux-x64 /
// linux-arm64 / -musl tarballs with a single `opencode` binary at the root).
package opencodeinstaller

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodeembed"
)

// PinnedVersion is the only opencode release the installer puts on disk. To bump
// it, take the new tarballs' digests from the GitHub release API into
// pinnedSHA256, and update OPENCODE_VERSION and the SHA-256 args in
// Dockerfile.release to match (a test checks they agree).
const PinnedVersion = "v1.18.33"

// pinnedSHA256 is the SHA-256 of each PinnedVersion tarball, by installer target.
// A var so tests can pin the fake tarballs they serve.
var pinnedSHA256 = map[string]string{
	"linux-x64":        "e546123213ae47909a4268692aa4b94950d011afe9cac9938753a2194f1c16d5",
	"linux-x64-musl":   "56636216a0b6595339ffdd5e2c885e6ea68a854f880720120e8cfa71172042c2",
	"linux-arm64":      "c63486624621924bf43be5c01abd252885661a734814224f6d70188a33aea858",
	"linux-arm64-musl": "916e4ca40cdd43025b7741a6c60cd1141b8622a8531e2eebdffcfd3e41f56907",
}

// opencodeReleasesBase is the release host. It is a var, not a const, so tests
// can point the installer at a local server instead of reaching GitHub.
var opencodeReleasesBase = "https://github.com/anomalyco/opencode/releases"

// embeddedTarball resolves the build-time fallback asset. Indirected through a
// var so tests can exercise the fallback without staging a real asset into the
// embed FS (which is fixed at compile time).
var embeddedTarball = opencodeembed.Tarball

// DefaultInstallDir matches the location used by opencode's official curl
// installer (~/.opencode/bin). We keep parity so users who installed by hand
// see the same layout.
var DefaultInstallDir = func() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/home/developer"
	}
	return filepath.Join(home, ".opencode", "bin")
}()

// VerifyBin returns true if the given path (or PATH-resolvable name) can be
// executed and responds to `--version` without error.
func VerifyBin(bin string) bool {
	if bin == "" {
		return false
	}
	if err := exec.Command(bin, "--version").Run(); err != nil {
		return false
	}
	return true
}

// VersionString runs `<bin> --version` and returns the trimmed output, or ""
// on error.
func VersionString(bin string) string {
	if bin == "" {
		return ""
	}
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// InstalledVersion returns the version bin reports, in PinnedVersion's form,
// or "" when it doesn't run or reports none.
func InstalledVersion(bin string) string { return extractSemver(VersionString(bin)) }

// InstallOptions tweaks Install. Zero value is fine.
type InstallOptions struct {
	// InstallDir overrides DefaultInstallDir.
	InstallDir string
	// HTTPClient lets callers inject a client (timeouts, proxies, tests).
	HTTPClient *http.Client
}

// EnsureOptions configures Ensure.
type EnsureOptions struct {
	CurrentBin string
	InstallDir string
}

// EnsureResult describes what Ensure decided to do.
type EnsureResult struct {
	Bin         string
	Installed   bool
	FromVersion string
	ToVersion   string
}

// Ensure installs PinnedVersion into opts.InstallDir unless opts.CurrentBin
// already works and reports that version. Any other version, older or newer, is
// replaced. If the install fails, a working existing binary is kept.
func Ensure(out io.Writer, opts EnsureOptions) (EnsureResult, error) {
	if out == nil {
		out = os.Stderr
	}
	res := EnsureResult{Bin: opts.CurrentBin, ToVersion: PinnedVersion}

	working := VerifyBin(opts.CurrentBin)
	if working {
		res.FromVersion = extractSemver(VersionString(opts.CurrentBin))
	}

	if working && res.FromVersion == PinnedVersion {
		fmt.Fprintf(out, "  ✓ OpenCode at the pinned version (%s)\n", res.FromVersion)
		return res, nil
	}

	if working {
		fmt.Fprintf(out, "  ↻ Replacing opencode %s with the pinned %s\n", res.FromVersion, PinnedVersion)
	} else {
		fmt.Fprintf(out, "  ⤓ Installing opencode %s\n", PinnedVersion)
	}

	bin, err := Install(out, InstallOptions{InstallDir: opts.InstallDir})
	if err != nil {
		if working {
			fmt.Fprintf(out, "  ! Update failed (%v); keeping existing %s\n", err, opts.CurrentBin)
			return res, nil
		}
		return res, err
	}
	res.Bin = bin
	res.Installed = true
	return res, nil
}

// Install downloads PinnedVersion and installs it without shelling out. The
// tarball, downloaded or embedded, is installed only if its SHA-256 matches
// pinnedSHA256. Returns the path to the installed binary on success.
func Install(out io.Writer, opts InstallOptions) (string, error) {
	if out == nil {
		out = os.Stderr
	}
	target, err := detectTarget()
	if err != nil {
		return "", err
	}
	wantSHA, ok := pinnedSHA256[target]
	if !ok {
		return "", fmt.Errorf("no pinned checksum for opencode %s on %s", PinnedVersion, target)
	}

	installDir := opts.InstallDir
	if installDir == "" {
		installDir = DefaultInstallDir
	}
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return "", fmt.Errorf("create install dir %s: %w", installDir, err)
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}

	tarballURL, tarballName := buildDownloadURL(PinnedVersion, target)

	fmt.Fprintf(out, "  ⤓ Installing OpenCode CLI (%s) into %s\n", target, installDir)
	fmt.Fprintf(out, "    Tarball: %s\n", tarballURL)

	tmpDir, err := os.MkdirTemp("", "opencode-install-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tarballPath := filepath.Join(tmpDir, tarballName)

	// Network first: download, verify and extract the published release. If any
	// step fails (no egress, proxy block, truncated, corrupt or altered body),
	// fall back to the tarball embedded at build time, when this build carries one
	// for target and it matches the pinned checksum too.
	netErr := downloadFile(client, tarballURL, tarballPath)
	if netErr == nil {
		netErr = verifyFileSHA256(tarballPath, wantSHA)
	}
	if netErr == nil {
		if err := extractOpencodeTarGz(tarballPath, installDir); err != nil {
			netErr = fmt.Errorf("extract: %w", err)
		}
	}
	if netErr != nil {
		embedded, ok := embeddedTarball(target)
		if !ok {
			return "", fmt.Errorf("download tarball: %w (this build has no embedded %s fallback)", netErr, target)
		}
		fmt.Fprintf(out, "  ! Download failed: %v\n", netErr)
		if got := sha256Hex(embedded); got != wantSHA {
			return "", fmt.Errorf("embedded opencode %s tarball has SHA-256 %s, want %s", target, got, wantSHA)
		}
		fmt.Fprintf(out, "  ⤓ Falling back to embedded opencode (%s, %d bytes)\n", target, len(embedded))
		if err := extractOpencodeTarGzFrom(bytes.NewReader(embedded), installDir); err != nil {
			return "", fmt.Errorf("extract embedded tarball: %w", err)
		}
	}
	binPath := filepath.Join(installDir, "opencode")
	if err := os.Chmod(binPath, 0o755); err != nil {
		return "", fmt.Errorf("chmod %s: %w", binPath, err)
	}
	if info, err := os.Stat(binPath); err != nil || info.IsDir() {
		return "", fmt.Errorf("expected %s after extraction, not found", binPath)
	}
	fmt.Fprintf(out, "  ✓ Installed opencode at %s\n", binPath)
	return binPath, nil
}

// detectTarget mirrors the logic in opencode's official install.sh — it picks
// linux-x64 / linux-arm64 / darwin-x64 / darwin-arm64 and appends "-musl" when
// running on Alpine. Baseline (non-AVX2) variants are not auto-selected here;
// if the resulting binary fails to start the user can override via KWA_OPENCODE_BIN.
func detectTarget() (string, error) {
	var os_, arch string
	switch runtime.GOOS {
	case "darwin":
		os_ = "darwin"
	case "linux":
		os_ = "linux"
	default:
		return "", fmt.Errorf("unsupported OS for auto-install: %s", runtime.GOOS)
	}
	switch runtime.GOARCH {
	case "amd64":
		arch = "x64"
	case "arm64":
		arch = "arm64"
	default:
		return "", fmt.Errorf("unsupported architecture for auto-install: %s", runtime.GOARCH)
	}

	target := os_ + "-" + arch
	if os_ == "linux" && isMusl() {
		target += "-musl"
	}
	return target, nil
}

// isMusl returns true when running on a musl-based libc (Alpine).
func isMusl() bool {
	if _, err := os.Stat("/etc/alpine-release"); err == nil {
		return true
	}
	out, err := exec.Command("ldd", "--version").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), "musl")
}

func buildDownloadURL(version, target string) (string, string) {
	tarballName := fmt.Sprintf("opencode-%s.tar.gz", target)
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	return opencodeReleasesBase + "/download/" + version + "/" + tarballName, tarballName
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// verifyFileSHA256 fails unless the file at path has the given hex SHA-256.
func verifyFileSHA256(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("checksum mismatch: SHA-256 %s, want %s", got, want)
	}
	return nil
}

func downloadFile(client *http.Client, url, dst string) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %s downloading %s", resp.Status, url)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return nil
}

// extractOpencodeTarGz extracts the single `opencode` binary from the tarball
// into destDir. opencode's release tarballs are flat (no leading directory)
// and contain a single executable named "opencode". We still walk the archive
// so we tolerate future packaging changes (extra files, leading dir).
func extractOpencodeTarGz(tarballPath, destDir string) error {
	f, err := os.Open(tarballPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return extractOpencodeTarGzFrom(f, destDir)
}

// extractOpencodeTarGzFrom is extractOpencodeTarGz over an arbitrary reader, so
// the same logic serves both a downloaded file and the embedded fallback.
func extractOpencodeTarGzFrom(r io.Reader, destDir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	absDest, err := filepath.Abs(destDir)
	if err != nil {
		return err
	}

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") || strings.HasPrefix(clean, "/") {
			return fmt.Errorf("refusing unsafe tar entry: %q", hdr.Name)
		}
		// We only care about the executable; flatten the path so a possible
		// leading directory like "opencode-linux-x64/opencode" still lands at
		// destDir/opencode.
		base := filepath.Base(clean)
		if base != "opencode" {
			continue
		}
		target := filepath.Join(absDest, base)
		if !strings.HasPrefix(target, absDest+string(os.PathSeparator)) && target != absDest {
			return fmt.Errorf("refusing tar entry escaping dest: %q", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			// Write to a same-directory temp file then atomic-rename it over
			// the target. This avoids ETXTBSY ("text file busy") when the
			// existing opencode binary is currently being executed by an
			// active chat session — Linux refuses O_WRONLY|O_TRUNC on a
			// running executable, but it allows rename(2) over it: existing
			// processes keep running off the old inode while new spawns get
			// the new file.
			if err := writeAtomic(target, tr); err != nil {
				return err
			}
		}
	}
}

// writeAtomic copies r into a temp file beside target, fsyncs and chmods it
// 0755, then renames it over target. The rename is atomic on POSIX
// filesystems and works even if target is a currently-executing binary.
func writeAtomic(target string, r io.Reader) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".opencode.*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup if anything below fails.
	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(tmpPath)
		}
	}()

	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// extractSemver pulls the first vX.Y.Z (or X.Y.Z) token out of a string and
// returns it normalized as "vX.Y.Z". Returns "" if none found.
func extractSemver(s string) string {
	for _, f := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '(' || r == ')' || r == ','
	}) {
		t := strings.TrimSpace(f)
		t = strings.TrimPrefix(t, "v")
		parts := strings.Split(t, ".")
		if len(parts) < 3 {
			continue
		}
		ok := true
		for _, p := range parts[:3] {
			if p == "" {
				ok = false
				break
			}
			for _, r := range p {
				if r < '0' || r > '9' {
					ok = false
					break
				}
			}
			if !ok {
				break
			}
		}
		if ok {
			return "v" + parts[0] + "." + parts[1] + "." + parts[2]
		}
	}
	return ""
}
