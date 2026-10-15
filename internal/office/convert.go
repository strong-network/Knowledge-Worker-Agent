// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package office

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ErrMissing means no LibreOffice was found to convert with.
var ErrMissing = errors.New("LibreOffice is not installed")

// cacheTTL is how long a PDF nobody has opened stays in the cache.
const cacheTTL = 30 * 24 * time.Hour

// Converter turns office files into PDFs and caches them by file version.
type Converter struct {
	// Bin returns the LibreOffice launcher, or "" when there is none.
	Bin func() string
	// Dir holds the cached PDFs and LibreOffice's profile; cacheDir() when empty.
	Dir     string
	Timeout time.Duration

	// One conversion at a time: two LibreOffice processes sharing a profile
	// fail, and one at a time keeps the cost of a burst of opens bounded.
	mu sync.Mutex
}

var defaultConverter = &Converter{Bin: Find, Timeout: 3 * time.Minute}

func (c *Converter) dir() string {
	if c.Dir != "" {
		return c.Dir
	}
	return cacheDir()
}

// PDF returns the path of a PDF of src, converting it unless the cache already
// holds one for this version of the file.
func (c *Converter) PDF(ctx context.Context, src string) (string, error) {
	info, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a file", filepath.Base(src))
	}
	dir := c.dir()
	sum := sha256.Sum256([]byte(src))
	key := hex.EncodeToString(sum[:12])
	cached := filepath.Join(dir, fmt.Sprintf("%s-%d-%d.pdf", key, info.Size(), info.ModTime().UnixNano()))
	if hit(cached) {
		return cached, nil
	}
	bin := c.Bin()
	if bin == "" {
		return "", ErrMissing
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if hit(cached) {
		return cached, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	removeStale(dir, "convert-*")
	if err := c.convert(ctx, bin, dir, src, cached); err != nil {
		return "", err
	}
	old, _ := filepath.Glob(filepath.Join(dir, key+"-*.pdf"))
	for _, p := range old {
		if p != cached {
			os.Remove(p)
		}
	}
	prune(dir, time.Now())
	return cached, nil
}

// hit reports whether a cached PDF exists, and marks it used so prune keeps it.
func hit(path string) bool {
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		return false
	}
	now := time.Now()
	os.Chtimes(path, now, now)
	return true
}

// convert runs LibreOffice on a copy of src and moves the PDF it wrote to dst.
func (c *Converter) convert(ctx context.Context, bin, dir, src, dst string) error {
	work, err := os.MkdirTemp(dir, "convert-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	// A copy, so LibreOffice never touches the user's file and a file still
	// being written is read once, whole or not at all.
	in := filepath.Join(work, filepath.Base(src))
	if err := copyFile(src, in); err != nil {
		return err
	}
	out := filepath.Join(work, "out")

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	profileDir := filepath.Join(dir, "profile")
	if err := lockDownProfile(profileDir); err != nil {
		return err
	}
	profile := (&url.URL{Scheme: "file", Path: profileDir}).String()
	cmd := exec.CommandContext(ctx, bin, "-env:UserInstallation="+profile,
		"--headless", "--norestore", "--nolockcheck", "--convert-to", "pdf", "--outdir", out, in)
	// soffice starts soffice.bin as a child: stop the whole group on timeout.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	runErr := cmd.Run()

	// LibreOffice exits 0 when it can't load a file, so the PDF is the verdict.
	pdf := filepath.Join(out, strings.TrimSuffix(filepath.Base(in), filepath.Ext(in))+".pdf")
	if info, err := os.Stat(pdf); err == nil && info.Size() > 0 {
		return os.Rename(pdf, dst)
	}
	if ctx.Err() == context.DeadlineExceeded {
		return errors.New("Converting the file took too long. Try again, or download it.")
	}
	msg := lastLine(output.String())
	if strings.Contains(msg, "could not be loaded") {
		return errors.New("LibreOffice couldn't open the file. It may be damaged, or not really the type its name says.")
	}
	if msg == "" && runErr != nil {
		msg = runErr.Error()
	}
	if msg == "" {
		msg = "LibreOffice wrote no PDF"
	}
	return errors.New(msg)
}

// macroSettings switch macros off in the profile conversions use: the files
// are often ones the agent fetched, and a preview must never run their code.
const macroSettings = `<?xml version="1.0" encoding="UTF-8"?>
<oor:items xmlns:oor="http://openoffice.org/2001/registry" xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
<item oor:path="/org.openoffice.Office.Common/Security/Scripting"><prop oor:name="DisableMacrosExecution" oor:op="fuse"><value>true</value></prop></item>
<item oor:path="/org.openoffice.Office.Common/Security/Scripting"><prop oor:name="MacroSecurityLevel" oor:op="fuse"><value>3</value></prop></item>
</oor:items>
`

// lockDownProfile writes macroSettings into a profile LibreOffice hasn't
// created yet; it keeps them in the file from then on.
func lockDownProfile(dir string) error {
	path := filepath.Join(dir, "user", "registrymodifications.xcu")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(macroSettings), 0o600)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// prune removes cached PDFs nobody has opened for cacheTTL.
func prune(dir string, now time.Time) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".pdf" {
			continue
		}
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > cacheTTL {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
