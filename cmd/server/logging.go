// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// logFilePath returns KWA_LOG_FILE, or the layout's log file when unset.
func logFilePath() string {
	if path := strings.TrimSpace(env.Get("KWA_LOG_FILE")); path != "" {
		return path
	}
	return layout.LogFile()
}

// setupFileLogging mirrors everything the process writes to stdout and stderr
// into a log file, while still writing to the real stdout/stderr so
// `docker logs` and terminal output keep working.
//
// Teeing at the stream level (rather than just log.SetOutput) matters because
// output here comes from two places: the stdlib logger, which writes to
// stderr, and the startup banner and various status lines, which use fmt.Print*
// to stdout. Only capturing the logger would silently drop the latter.
//
// It works by swapping os.Stdout/os.Stderr for pipes and copying each pipe to
// both the original stream and the file. Note the stdlib logger caches
// os.Stderr at init, so log.SetOutput must be re-pointed at the new os.Stderr
// afterwards or log lines would bypass the tee.
//
// The destination is KWA_LOG_FILE, defaulting to layout.LogFile(): /tmp,
// which every user can write, until the one-folder migration moves it into
// the root, where it survives a restart.
// Setting it to "off", "none" or "-" disables the file copy entirely.
//
// Failing to open the log file is never fatal: the server keeps its original
// stdout/stderr and reports why. A read-only or missing log directory must not
// stop the server booting.
//
// The returned func restores the original streams and flushes the copiers; the
// caller should defer it. It is a no-op when no file was opened.
func setupFileLogging() func() {
	path := logFilePath()
	switch strings.ToLower(path) {
	case "off", "none", "-":
		return func() {}
	}

	f, err := openLogFile(path)
	if err != nil {
		// The real streams are untouched, so just say why and carry on.
		log.Printf("logging: file logging disabled (%v); using stdout/stderr only", err)
		return func() {}
	}

	origStdout, origStderr := os.Stdout, os.Stderr
	// Serialises the two copiers and caps the file size (see lockedWriter).
	size, _ := f.Seek(0, io.SeekEnd) // appending, so start from current length
	fw := &lockedWriter{w: f, path: path, maxBytes: maxLogBytes(), size: size}

	var wg sync.WaitGroup
	restoreOut, err := teeStream(&os.Stdout, origStdout, fw, &wg)
	if err != nil {
		log.Printf("logging: file logging disabled (%v); using stdout/stderr only", err)
		_ = f.Close()
		return func() {}
	}
	restoreErr, err := teeStream(&os.Stderr, origStderr, fw, &wg)
	if err != nil {
		log.Printf("logging: file logging disabled (%v); using stdout/stderr only", err)
		restoreOut()
		wg.Wait()
		_ = f.Close()
		return func() {}
	}

	// The logger captured the original os.Stderr at init; re-point it at the
	// replacement so log output is teed as well.
	log.SetOutput(os.Stderr)

	log.Printf("logging: mirroring stdout and stderr to %s", path)

	return func() {
		restoreOut()
		restoreErr()
		wg.Wait() // let the copiers drain before closing the file
		log.SetOutput(origStderr)
		// Close via the writer: after a rotation it holds a different file
		// than f, so closing f directly would leak the current one.
		_ = fw.Close()
	}
}

// teeStream replaces *stream with a pipe whose contents are copied to both the
// original stream and the log file. The returned func closes the pipe writer
// and restores the original stream.
func teeStream(stream **os.File, orig *os.File, file io.Writer, wg *sync.WaitGroup) (func(), error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create pipe: %w", err)
	}
	*stream = w

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer r.Close()
		// Copy errors are ignored: a broken pipe here must not take the
		// server down, and there is nowhere useful left to report it.
		_, _ = io.Copy(io.MultiWriter(orig, file), r)
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			*stream = orig
			_ = w.Close() // unblocks the copier, which then exits
		})
	}, nil
}

// lockedWriter serialises writes to the log file and caps its size. os.Stdout
// and os.Stderr are drained by separate goroutines, so without the mutex their
// writes could interleave within a single line.
//
// When the file exceeds maxBytes it is rotated: the current file becomes
// <path>.1 (replacing any previous .1) and a fresh file is opened. Only one
// backup is kept, so disk use stays bounded at roughly 2*maxBytes — /tmp is
// typically small, and an unbounded log there could fill the filesystem.
type lockedWriter struct {
	mu       sync.Mutex
	w        *os.File
	path     string
	maxBytes int64
	size     int64
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.maxBytes > 0 && l.size+int64(len(p)) > l.maxBytes {
		// Rotation failures are not fatal: keep writing to the current file
		// rather than losing output.
		if err := l.rotate(); err == nil {
			l.size = 0
		}
	}

	n, err := l.w.Write(p)
	l.size += int64(n)
	return n, err
}

// rotate must be called with the mutex held.
func (l *lockedWriter) rotate() error {
	if err := l.w.Close(); err != nil {
		return err
	}
	// os.Rename replaces an existing .1, so only one backup is ever kept.
	if err := os.Rename(l.path, l.path+".1"); err != nil {
		// Reopen the original so logging survives a failed rotation.
		if f, rerr := openLogFile(l.path); rerr == nil {
			l.w = f
		}
		return err
	}
	f, err := openLogFile(l.path)
	if err != nil {
		return err
	}
	l.w = f
	return nil
}

func (l *lockedWriter) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Close()
}

// defaultMaxLogBytes caps the log file before it is rotated. /tmp is often a
// small filesystem, so an unbounded log could fill it.
const defaultMaxLogBytes = 50 << 20 // 50 MiB

// maxLogBytes reads the rotation threshold from KWA_LOG_MAX_BYTES.
// A value of 0 (or a negative/unparseable one) disables rotation.
func maxLogBytes() int64 {
	raw := strings.TrimSpace(env.Get("KWA_LOG_MAX_BYTES"))
	if raw == "" {
		return defaultMaxLogBytes
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		log.Printf("logging: invalid KWA_LOG_MAX_BYTES %q; using default", raw)
		return defaultMaxLogBytes
	}
	if n <= 0 {
		return 0 // rotation disabled
	}
	return n
}

// openLogFile creates the parent directory if needed and opens the file for
// appending, so restarts add to the existing log instead of truncating it.
func openLogFile(path string) (*os.File, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create log dir %s: %w", dir, err)
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file %s: %w", path, err)
	}
	return f, nil
}
