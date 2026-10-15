// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package office

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The LibreOffice build Install unpacks: The Document Foundation's own Debian
// packages, which run from any folder. The archive URL is permanent, unlike
// the stable/ one; this build is the 26.2.6 release. Bump all four together.
const (
	Version   = "26.2.6"
	bundleURL = "https://downloadarchive.documentfoundation.org/libreoffice/old/26.2.6.3/deb/x86_64/LibreOffice_26.2.6.3_Linux_x86-64_deb.tar.gz"
	bundleSHA = "fd0e8f8f2408dd2e5b90286e60f3f97cf566ba441cd48cfc5bcc68067303e0bc"
	optDir    = "libreoffice26.2"
)

// packages are the bundle's packages a headless conversion of slides,
// documents and spreadsheets needs, out of 42: no dictionaries, databases,
// scripting or desktop integration.
var packages = []string{
	"libreoffice26.2-ure", "libreoffice26.2", "libobasis26.2-core", "libobasis26.2-images",
	"libobasis26.2-graphicfilter", "libobasis26.2-ooofonts", "libobasis26.2-en-us",
	"libobasis26.2-impress", "libobasis26.2-draw", "libobasis26.2-writer",
	"libobasis26.2-calc", "libobasis26.2-math", "libobasis26.2-xsltfilter",
}

// errDamaged is a download whose SHA-256 isn't the pinned one.
var errDamaged = errors.New("the LibreOffice download doesn't match its checksum")

const downloadTries = 3

// Sizes the UI quotes before an install, in MB of a million bytes.
const (
	DownloadMB  = 217
	InstalledMB = 546
	// What an install needs free while it runs: the download, the packages
	// taken out of it, and the unpacked result.
	neededMB = 1100
)

// Installer installs LibreOffice into Dir, one install at a time, and reports
// how far it has got.
type Installer struct {
	Dir    string
	URL    string
	SHA256 string
	Client *http.Client

	mu     sync.Mutex
	status InstallStatus
}

// InstallStatus is what GET /api/office/status reports about an install.
type InstallStatus struct {
	Installing bool   `json:"installing"`
	Stage      string `json:"stage,omitempty"`
	Done       int64  `json:"done,omitempty"`
	Total      int64  `json:"total,omitempty"`
	Error      string `json:"error,omitempty"`
}

var defaultInstaller = &Installer{}

func (in *Installer) dir() string {
	if in.Dir != "" {
		return in.Dir
	}
	return installDir()
}

// Status returns a copy of the install's progress.
func (in *Installer) Status() InstallStatus {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.status
}

func (in *Installer) set(f func(*InstallStatus)) {
	in.mu.Lock()
	defer in.mu.Unlock()
	f(&in.status)
}

// Start begins an install in the background, unless one is running. It
// reports whether it started one.
func (in *Installer) Start() bool {
	in.mu.Lock()
	if in.status.Installing {
		in.mu.Unlock()
		return false
	}
	in.status = InstallStatus{Installing: true, Stage: "starting"}
	in.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		err := in.install(ctx)
		in.set(func(s *InstallStatus) {
			*s = InstallStatus{}
			if errors.Is(err, errDamaged) {
				s.Error = fmt.Sprintf("The download was damaged on each of %d tries. Try again later.", downloadTries)
			} else if err != nil {
				s.Error = err.Error()
			}
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠ LibreOffice install failed: %v\n", err)
		} else {
			fmt.Printf("  ✓ LibreOffice %s installed in %s\n", Version, in.dir())
		}
	}()
	return true
}

func (in *Installer) install(ctx context.Context) error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return fmt.Errorf("the LibreOffice install is for x86-64 Linux only")
	}
	dpkg, err := exec.LookPath("dpkg-deb")
	if err != nil {
		return fmt.Errorf("unpacking LibreOffice needs dpkg-deb, which isn't installed")
	}
	dest := in.dir()
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	removeStale(parent, ".libreoffice-install-*")
	if free := freeMB(parent); free >= 0 && free < neededMB {
		return fmt.Errorf("installing LibreOffice needs about %d MB free; %s has %d MB", neededMB, parent, free)
	}
	stage, err := os.MkdirTemp(parent, ".libreoffice-install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)

	bundle := filepath.Join(stage, "bundle.tar.gz")
	// Each try can reach a different mirror, and one mirror can serve a bad copy.
	for try := 1; ; try++ {
		in.set(func(s *InstallStatus) { s.Stage = "downloading"; s.Done, s.Total = 0, 0 })
		err = in.download(ctx, bundle)
		if err == nil || try == downloadTries || ctx.Err() != nil {
			break
		}
		fmt.Fprintf(os.Stderr, "  ⚠ LibreOffice download, try %d of %d: %v\n", try, downloadTries, err)
	}
	if err != nil {
		return err
	}
	in.set(func(s *InstallStatus) { s.Stage = "unpacking"; s.Done, s.Total = 0, 0 })
	debs, err := extractDebs(bundle, filepath.Join(stage, "debs"))
	if err != nil {
		return err
	}
	os.Remove(bundle)
	root := filepath.Join(stage, "root")
	for _, deb := range debs {
		if out, err := exec.CommandContext(ctx, dpkg, "-x", deb, root).CombinedOutput(); err != nil {
			return fmt.Errorf("unpacking %s: %v: %s", filepath.Base(deb), err, strings.TrimSpace(string(out)))
		}
	}
	in.set(func(s *InstallStatus) { s.Stage = "checking" })
	if err := checkRuns(ctx, installedBin(root)); err != nil {
		return err
	}
	// Into place last, so a failed or interrupted install leaves nothing half done.
	old := dest + ".old"
	os.RemoveAll(old)
	if err := os.Rename(dest, old); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(root, dest); err != nil {
		os.Rename(old, dest)
		return err
	}
	os.RemoveAll(old)
	return nil
}

// download fetches the bundle to dst, checking its SHA-256 as it arrives.
func (in *Installer) download(ctx context.Context, dst string) error {
	url, want := in.URL, in.SHA256
	if url == "" {
		url, want = bundleURL, bundleSHA
	}
	client := in.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("downloading LibreOffice: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading LibreOffice: HTTP %s", resp.Status)
	}
	in.set(func(s *InstallStatus) { s.Total = resp.ContentLength })
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				return err
			}
			h.Write(buf[:n])
			in.set(func(s *InstallStatus) { s.Done += int64(n) })
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("downloading LibreOffice: %v", rerr)
		}
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("%w: SHA-256 %s, want %s", errDamaged, got, want)
	}
	return f.Close()
}

// extractDebs writes the bundle's packages listed in packages to dir.
func extractDebs(bundle, dir string) ([]string, error) {
	f, err := os.Open(bundle)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, p := range packages {
		want[p] = true
	}
	var debs []string
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := path.Base(hdr.Name)
		pkg, _, ok := strings.Cut(name, "_")
		if hdr.Typeflag != tar.TypeReg || !strings.HasSuffix(name, ".deb") || !ok || !want[pkg] {
			continue
		}
		dst := filepath.Join(dir, name)
		out, err := os.Create(dst)
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return nil, err
		}
		if err := out.Close(); err != nil {
			return nil, err
		}
		debs = append(debs, dst)
		delete(want, pkg)
	}
	if len(want) > 0 {
		var missing []string
		for p := range want {
			missing = append(missing, p)
		}
		return nil, fmt.Errorf("the LibreOffice download lacks %s", strings.Join(missing, ", "))
	}
	return debs, nil
}

// checkRuns fails unless the unpacked LibreOffice starts and reports a version.
func checkRuns(ctx context.Context, bin string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "LibreOffice") {
		return fmt.Errorf("the unpacked LibreOffice doesn't start: %v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// freeMB is the space free for this user in dir, or -1 when unknown.
func freeMB(dir string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize) >> 20
}
