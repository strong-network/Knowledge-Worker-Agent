// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package office

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeSoffice writes a stand-in for LibreOffice. It logs each run, notes when
// two runs overlap (mkdir is atomic, so simultaneous starts are caught too), and writes "<input name>.pdf" holding the input's content,
// unless FAKE_MODE in its folder's mode file says otherwise.
func fakeSoffice(t *testing.T) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "soffice")
	script := `#!/bin/sh
d=$(dirname "$0")
mode=$(cat "$d/mode" 2>/dev/null)
for a; do in="$a"; done
out=""; prev=""
for a; do [ "$prev" = "--outdir" ] && out="$a"; prev="$a"; done
echo "$in" >> "$d/runs"
mkdir "$d/running" 2>/dev/null || echo overlap >> "$d/overlaps"
case "$mode" in
  slow) sleep 0.2 ;;
  hang) sleep 30 & echo $! > "$d/child"; wait ;;
  nopdf) rmdir "$d/running"; echo "Error: source file could not be loaded"; exit 0 ;;
  touch) echo changed >> "$in" ;;
esac
mkdir -p "$out"
base=$(basename "$in"); base="${base%.*}"
{ printf '%%PDF-fake '; cat "$in"; } > "$out/$base.pdf"
rmdir "$d/running"
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func setMode(t *testing.T, dir, mode string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte(mode), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runs(dir string) int {
	b, _ := os.ReadFile(filepath.Join(dir, "runs"))
	return strings.Count(string(b), "\n")
}

func writeDoc(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newConverter(t *testing.T, bin string) *Converter {
	return &Converter{Bin: func() string { return bin }, Dir: t.TempDir(), Timeout: 10 * time.Second}
}

func TestPDFConvertsOnceAndCaches(t *testing.T) {
	bin, fake := fakeSoffice(t)
	c := newConverter(t, bin)
	src := filepath.Join(t.TempDir(), "Deck.pptx")
	writeDoc(t, src, "slides v1")

	pdf, err := c.PDF(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(pdf); string(b) != "%PDF-fake slides v1" {
		t.Errorf("PDF = %q", b)
	}
	again, err := c.PDF(context.Background(), src)
	if err != nil || again != pdf {
		t.Fatalf("second call = %q, %v; want the cached %q", again, err, pdf)
	}
	if n := runs(fake); n != 1 {
		t.Errorf("LibreOffice ran %d times, want 1", n)
	}
}

func TestPDFConvertsAgainWhenTheFileChanges(t *testing.T) {
	bin, fake := fakeSoffice(t)
	c := newConverter(t, bin)
	src := filepath.Join(t.TempDir(), "Report.docx")
	writeDoc(t, src, "draft")
	if _, err := c.PDF(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	writeDoc(t, src, "final version")
	pdf, err := c.PDF(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(pdf); string(b) != "%PDF-fake final version" {
		t.Errorf("PDF = %q, want the new content", b)
	}
	if n := runs(fake); n != 2 {
		t.Errorf("LibreOffice ran %d times, want 2", n)
	}
	if old, _ := filepath.Glob(filepath.Join(c.Dir, "*.pdf")); len(old) != 1 {
		t.Errorf("cache holds %v, want only the current PDF", old)
	}
}

// LibreOffice exits 0 when it can't load a file; the missing PDF is the error.
func TestPDFReportsAConversionThatWroteNothing(t *testing.T) {
	bin, fake := fakeSoffice(t)
	setMode(t, fake, "nopdf")
	c := newConverter(t, bin)
	src := filepath.Join(t.TempDir(), "Broken.xlsx")
	writeDoc(t, src, "not a spreadsheet")
	_, err := c.PDF(context.Background(), src)
	if err == nil || !strings.Contains(err.Error(), "couldn't open the file") {
		t.Fatalf("err = %v, want the file reported as unreadable", err)
	}
	if left, _ := filepath.Glob(filepath.Join(c.Dir, "*.pdf")); len(left) != 0 {
		t.Errorf("cache holds %v after a failure", left)
	}
}

func TestPDFWithoutLibreOffice(t *testing.T) {
	c := newConverter(t, "")
	src := filepath.Join(t.TempDir(), "Deck.pptx")
	writeDoc(t, src, "x")
	if _, err := c.PDF(context.Background(), src); !errors.Is(err, ErrMissing) {
		t.Fatalf("err = %v, want ErrMissing", err)
	}
	if _, err := c.PDF(context.Background(), filepath.Join(t.TempDir(), "gone.pptx")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: err = %v, want os.ErrNotExist", err)
	}
}

// Two LibreOffice processes sharing a profile fail, so conversions queue.
func TestPDFConvertsOneAtATime(t *testing.T) {
	bin, fake := fakeSoffice(t)
	setMode(t, fake, "slow")
	c := newConverter(t, bin)
	dir := t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := range 4 {
		src := filepath.Join(dir, "deck"+strconv.Itoa(i)+".pptx")
		writeDoc(t, src, "deck "+strconv.Itoa(i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.PDF(context.Background(), src)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(fake, "overlaps")); len(b) > 0 {
		t.Errorf("conversions overlapped: %q", b)
	}
	if n := runs(fake); n != 4 {
		t.Errorf("LibreOffice ran %d times, want 4", n)
	}
}

func TestPDFLeavesTheOriginalAlone(t *testing.T) {
	bin, fake := fakeSoffice(t)
	setMode(t, fake, "touch")
	c := newConverter(t, bin)
	src := filepath.Join(t.TempDir(), "Deck.pptx")
	writeDoc(t, src, "original")
	if _, err := c.PDF(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(src); string(b) != "original" {
		t.Errorf("source = %q, want it untouched", b)
	}
	if b, _ := os.ReadFile(filepath.Join(fake, "runs")); strings.Contains(string(b), src) {
		t.Errorf("LibreOffice was given the original path: %q", b)
	}
}

// A conversion that hangs is stopped, child processes included.
func TestPDFStopsAHungConversion(t *testing.T) {
	bin, fake := fakeSoffice(t)
	setMode(t, fake, "hang")
	c := newConverter(t, bin)
	c.Timeout = 300 * time.Millisecond
	src := filepath.Join(t.TempDir(), "Deck.pptx")
	writeDoc(t, src, "x")
	start := time.Now()
	_, err := c.PDF(context.Background(), src)
	if err == nil || !strings.Contains(err.Error(), "took too long") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Errorf("returned after %s", d)
	}
	b, _ := os.ReadFile(filepath.Join(fake, "child"))
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid <= 0 {
		t.Fatal("the fake recorded no child")
	}
	for i := 0; i < 50 && syscall.Kill(pid, 0) == nil; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Error("LibreOffice's child process outlived the timeout")
	}
}

func TestPruneDropsPDFsNobodyOpened(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old, recent := filepath.Join(dir, "a-1-1.pdf"), filepath.Join(dir, "b-1-1.pdf")
	writeDoc(t, old, "%PDF")
	writeDoc(t, recent, "%PDF")
	stale := now.Add(-cacheTTL - time.Hour)
	os.Chtimes(old, stale, stale)
	prune(dir, now)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("a PDF unopened for longer than the TTL was kept")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Error("a recent PDF was removed")
	}
}

func TestRemoveStaleKeepsWhatMayStillBeRunning(t *testing.T) {
	dir := t.TempDir()
	stale, fresh := filepath.Join(dir, "convert-1"), filepath.Join(dir, "convert-2")
	os.Mkdir(stale, 0o700)
	os.Mkdir(fresh, 0o700)
	old := time.Now().Add(-2 * time.Hour)
	os.Chtimes(stale, old, old)
	removeStale(dir, "convert-*")
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("an hour-old leftover was kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a folder that may still be in use was removed")
	}
}

func TestConversionsRunWithMacrosOff(t *testing.T) {
	bin, _ := fakeSoffice(t)
	c := newConverter(t, bin)
	src := filepath.Join(t.TempDir(), "Deck.pptx")
	writeDoc(t, src, "x")
	if _, err := c.PDF(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(c.Dir, "profile", "user", "registrymodifications.xcu"))
	if err != nil || !strings.Contains(string(b), `"DisableMacrosExecution"`) {
		t.Errorf("the profile doesn't switch macros off: %v %q", err, b)
	}
}

func TestPreviewable(t *testing.T) {
	for name, want := range map[string]bool{
		"a.pptx": true, "b.PPT": true, "c.docx": true, "d.doc": true, "e.xlsx": true, "f.xls": true,
		"g.pdf": false, "h.odp": false, "pptx": false, "i.csv": false,
	} {
		if got := Previewable(name); got != want {
			t.Errorf("Previewable(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestFindPrefersTheInstalledCopy(t *testing.T) {
	onPath, _ := fakeSoffice(t)
	t.Setenv("PATH", filepath.Dir(onPath))
	dir := t.TempDir()
	if got := find(dir); got != onPath {
		t.Errorf("without an install, find = %q, want the one on PATH", got)
	}
	installed := installedBin(dir)
	os.MkdirAll(filepath.Dir(installed), 0o755)
	writeDoc(t, installed, "#!/bin/sh\n")
	os.Chmod(installed, 0o755)
	if got := find(dir); got != installed {
		t.Errorf("find = %q, want the installed copy %q", got, installed)
	}
	t.Setenv("PATH", t.TempDir())
	if got := find(t.TempDir()); got != "" {
		t.Errorf("with nothing installed, find = %q", got)
	}
}
