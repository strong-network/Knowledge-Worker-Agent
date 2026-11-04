// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package opencodeinstaller downloads and installs the OpenCode CLI: 2.x from
// its npm packages (@opencode/cli-<target>), 1.x from its GitHub release
// artifacts (github.com/anomalyco/opencode), for linux-x64 / linux-arm64 /
// -musl.
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

// PinnedVersion is the opencode 1.x release the installer puts on disk when
// 1.x is asked for. To bump it, take the new tarballs' digests from the GitHub
// release API into pinnedSHA256.
const PinnedVersion = "v1.19.4"

// pinnedSHA256 is the SHA-256 of each PinnedVersion tarball, by installer target.
// A var so tests can pin the fake tarballs they serve.
var pinnedSHA256 = map[string]string{
	"linux-x64":        "849bd6b3d959a1d2ef200ed1634a51c79a45ca236aa26665d75debd694805cce",
	"linux-x64-musl":   "265bd8027e84fe01837f72df8b7615d2959491f45569d52a21f2fcdbec9decdf",
	"linux-arm64":      "8568230ccd847dc93e95b9f6894379077d3e014a16b48040743573e77c23e85f",
	"linux-arm64-musl": "aa32a91f62cfddfa4615ba4deffc2b13696e77cbee83551824e3f16e6770b37e",
}

// opencodeReleasesBase is the release host. It is a var, not a const, so tests
// can point the installer at a local server instead of reaching GitHub.
var opencodeReleasesBase = "https://github.com/anomalyco/opencode/releases"

// PinnedVersion2 is the opencode 2.x release the installer puts on disk when
// 2.x is asked for, as it is by default. 2.x is published on npm, one package
// per target, not as GitHub release assets. To bump it, take each package's
// tarball, check it against npm's sha512 integrity, pin its SHA-256 in
// pinnedSHA256v2, and update OPENCODE_VERSION and the SHA-256 args in
// Dockerfile.release to match (a test checks they agree).
const PinnedVersion2 = "v2.2.5"

// pinnedSHA256v2 is the SHA-256 of each PinnedVersion2 tarball, by target.
var pinnedSHA256v2 = map[string]string{
	"linux-x64":        "92690eee07a680d3cb760ca02ba43878de9eab1b4e5d5dfae8795518c53628f4",
	"linux-x64-musl":   "60d1081dcc5fe92935b65b9b2da8fdb9897af1e1727249950e4207e7b29f279f",
	"linux-arm64":      "f8c5a82075fede9b5b044ed6c8a6d13ad807a7e62caea1cd0a878adef49eb053",
	"linux-arm64-musl": "ef26ea9a81b8adf4185a47b7d3aa5bb3a2a69bc4297cbdaaff9641881339b594",
}

// npmRegistry hosts the 2.x packages. A var so tests can serve their own.
var npmRegistry = "https://registry.npmjs.org"

// release is an opencode version the installer can put on disk.
type release struct {
	version string
	sha256  map[string]string
	url     func(target string) (url, name string)
	// embedded is whether this build may carry the tarball as a fallback.
	embedded bool
}

// releaseFor returns the pinned release of major (1 unless 2 or more).
func releaseFor(major int) release {
	if major >= 2 {
		return release{version: PinnedVersion2, sha256: pinnedSHA256v2, url: npmURL, embedded: true}
	}
	return release{
		version: PinnedVersion,
		sha256:  pinnedSHA256,
		url:     func(target string) (string, string) { return buildDownloadURL(PinnedVersion, target) },
	}
}

// Pinned returns the version the installer pins for major.
func Pinned(major int) string { return releaseFor(major).version }

// npmURL is the 2.x package tarball for target, such as
// .../@opencode/cli-linux-x64/-/cli-linux-x64-2.2.5.tgz.
func npmURL(target string) (string, string) {
	name := fmt.Sprintf("cli-%s-%s.tgz", target, strings.TrimPrefix(PinnedVersion2, "v"))
	return npmRegistry + "/@opencode/cli-" + target + "/-/" + name, name
}

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
	// Major picks the pinned release: 2.x for 2 or more, else 1.x.
	Major int
}

// EnsureOptions configures Ensure.
type EnsureOptions struct {
	CurrentBin string
	InstallDir string
	// Major picks the pinned release: 2.x for 2 or more, else 1.x.
	Major int
}

// EnsureResult describes what Ensure decided to do.
type EnsureResult struct {
	Bin         string
	Installed   bool
	FromVersion string
	ToVersion   string
}

// Ensure installs the pinned release of opts.Major into opts.InstallDir unless
// opts.CurrentBin already works and reports that version. Any other version,
// older or newer, is replaced. If the install fails, a working existing binary
// is kept.
func Ensure(out io.Writer, opts EnsureOptions) (EnsureResult, error) {
	if out == nil {
		out = os.Stderr
	}
	pinned := releaseFor(opts.Major).version
	res := EnsureResult{Bin: opts.CurrentBin, ToVersion: pinned}

	working := VerifyBin(opts.CurrentBin)
	if working {
		res.FromVersion = extractSemver(VersionString(opts.CurrentBin))
	}

	if working && res.FromVersion == pinned {
		fmt.Fprintf(out, "  ✓ OpenCode at the pinned version (%s)\n", res.FromVersion)
		return res, nil
	}

	if working {
		fmt.Fprintf(out, "  ↻ Replacing opencode %s with the pinned %s\n", res.FromVersion, pinned)
	} else {
		fmt.Fprintf(out, "  ⤓ Installing opencode %s\n", pinned)
	}

	bin, err := Install(out, InstallOptions{InstallDir: opts.InstallDir, Major: opts.Major})
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

// Install downloads the pinned release of opts.Major and installs it without
// shelling out. The tarball, downloaded or embedded, is installed only if its
// SHA-256 matches the pin. Returns the path to the installed binary on success.
func Install(out io.Writer, opts InstallOptions) (string, error) {
	if out == nil {
		out = os.Stderr
	}
	rel := releaseFor(opts.Major)
	target, err := detectTarget()
	if err != nil {
		return "", err
	}
	wantSHA, ok := rel.sha256[target]
	if !ok {
		return "", fmt.Errorf("no pinned checksum for opencode %s on %s", rel.version, target)
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

	tarballURL, tarballName := rel.url(target)

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
		if !ok || !rel.embedded {
			return "", fmt.Errorf("download tarball: %w (this build has no embedded %s %s fallback)", netErr, rel.version, target)
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
