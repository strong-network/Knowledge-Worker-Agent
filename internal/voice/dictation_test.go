// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env/envtest"
)

// fakeTranscriber stands in for the engine: it records requests and answers
// with numbered words, so every pass's text is recognisable.
type fakeTranscriber struct {
	mu    sync.Mutex
	reqs  []request
	warm  int
	reply func(r request, n int) (string, error)
}

func (f *fakeTranscriber) Transcribe(ctx context.Context, r request) (string, error) {
	f.mu.Lock()
	n := len(f.reqs)
	f.reqs = append(f.reqs, r)
	reply := f.reply
	f.mu.Unlock()
	if reply == nil {
		reply = numberedWords
	}
	return reply(r, n)
}

func (f *fakeTranscriber) Warm() {
	f.mu.Lock()
	f.warm++
	f.mu.Unlock()
}

func (f *fakeTranscriber) requests() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]request(nil), f.reqs...)
}

func (f *fakeTranscriber) warmCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.warm
}

func (f *fakeTranscriber) finals() []request {
	var out []request
	for _, r := range f.requests() {
		if r.audioCtx == 0 {
			out = append(out, r)
		}
	}
	return out
}

// numberedWords answers with about 2.5 words per second of audio.
func numberedWords(r request, n int) (string, error) {
	kind := "live"
	if r.audioCtx == 0 {
		kind = "final"
	}
	words := max(1, len(r.pcm)*5/(2*sampleRate))
	var b strings.Builder
	for i := 0; i < words; i++ {
		fmt.Fprintf(&b, " %s%d-%d", kind, n, i)
	}
	return b.String(), nil
}

type testService struct {
	*Service
	fake *fakeTranscriber
	mux  *http.ServeMux
}

func newTestService(t *testing.T, vocabDir string) *testService {
	t.Helper()
	fake := &fakeTranscriber{}
	s := &Service{eng: fake, vocabDir: func() string { return vocabDir }, idleAfter: 10 * time.Second, stopWait: 5 * time.Second}
	mux := http.NewServeMux()
	registerRoutes(mux, func() *Service { return s })
	return &testService{Service: s, fake: fake, mux: mux}
}

func (ts *testService) do(t *testing.T, method, path, contentType string, body []byte) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	ts.mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (ts *testService) start(t *testing.T) string {
	t.Helper()
	code, out := ts.do(t, "POST", "/api/voice/dictations", "", nil)
	if code != http.StatusCreated {
		t.Fatalf("start = %d %v", code, out)
	}
	return out["id"].(string)
}

func (ts *testService) upload(t *testing.T, id string, pcm []int16) (int, map[string]any) {
	t.Helper()
	return ts.do(t, "POST", "/api/voice/dictations/"+id+"/audio", "application/octet-stream", pcmBytes(pcm))
}

// uploadAll sends pcm in quarter-second uploads, pausing between them so the
// worker gets to run, and returns every response's final text.
func (ts *testService) uploadAll(t *testing.T, id string, pcm []int16, pause time.Duration) []string {
	t.Helper()
	var finals []string
	for i := 0; i < len(pcm); i += sampleRate / 4 {
		code, out := ts.upload(t, id, pcm[i:min(len(pcm), i+sampleRate/4)])
		if code != http.StatusOK {
			t.Fatalf("upload = %d %v", code, out)
		}
		finals = append(finals, out["final"].(string))
		time.Sleep(pause)
	}
	return finals
}

func (ts *testService) stop(t *testing.T, id string) (int, map[string]any) {
	t.Helper()
	return ts.do(t, "POST", "/api/voice/dictations/"+id+"/stop", "", nil)
}

// waitForLive waits until n live passes have run. A test that needs one cannot
// assume the worker goroutine was scheduled between its uploads: on one CPU, or
// a loaded machine, every upload can land before the worker first runs, and a
// stop by then sends it straight to the final pass with no live pass at all.
func (ts *testService) waitForLive(t *testing.T, n int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		lives := len(ts.fake.requests()) - len(ts.fake.finals())
		if lives >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited for %d live passes, saw %d", n, lives)
		}
	}
}

func pcmBytes(pcm []int16) []byte {
	b := make([]byte, 2*len(pcm))
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(s))
	}
	return b
}

func TestDictationLiveAndFinalPasses(t *testing.T) {
	ts := newTestService(t, "")
	id := ts.start(t)
	pcm := synth(1.5, 0.8, 1.5, 0.8, 1.2)
	ts.uploadAll(t, id, pcm, 10*time.Millisecond)
	code, out := ts.stop(t, id)
	if code != http.StatusOK {
		t.Fatalf("stop = %d %v", code, out)
	}
	finals := ts.fake.finals()
	if len(finals) != 3 {
		t.Fatalf("final passes = %d, want 3 (two pauses and the end)", len(finals))
	}
	var want []string
	for i, r := range ts.fake.requests() {
		if r.audioCtx == 0 {
			if r.noRetry {
				t.Error("a final pass turned the engine's retries off")
			}
			text, _ := numberedWords(r, i)
			want = append(want, cleanText(text))
			continue
		}
		if !r.noRetry || r.audioCtx != fittedContext(len(r.pcm)) {
			t.Errorf("live pass: audio_ctx %d for %d samples, noRetry %v", r.audioCtx, len(r.pcm), r.noRetry)
		}
	}
	if out["text"] != strings.Join(want, " ") {
		t.Fatalf("text = %q, want %q", out["text"], strings.Join(want, " "))
	}
	if n := ts.fake.warmCount(); n != 1 {
		t.Fatalf("engine warmed %d times, want once at start", n)
	}
}

func TestFinalTextNeverChanges(t *testing.T) {
	ts := newTestService(t, "")
	id := ts.start(t)
	finals := ts.uploadAll(t, id, synth(1, 0.7, 1, 0.7, 1, 0.7, 1), 5*time.Millisecond)
	_, out := ts.stop(t, id)
	finals = append(finals, out["text"].(string))
	grew := false
	for i := 1; i < len(finals); i++ {
		if !strings.HasPrefix(finals[i], finals[i-1]) {
			t.Fatalf("final text changed:\n%q\nthen\n%q", finals[i-1], finals[i])
		}
		grew = grew || finals[i] != finals[i-1]
	}
	if !grew {
		t.Fatal("final text never grew during the dictation")
	}
}

func TestBrokenLiveResultsAreSkipped(t *testing.T) {
	ts := newTestService(t, "")
	ts.fake.reply = func(r request, n int) (string, error) {
		if r.audioCtx > 0 && n > 0 {
			return "it was the beauty of it it was the beauty of it it was the beauty of it", nil
		}
		return numberedWords(r, n)
	}
	id := ts.start(t)
	pcm := synth(6)
	for i := 0; i < len(pcm); i += sampleRate / 4 {
		_, out := ts.upload(t, id, pcm[i:min(len(pcm), i+sampleRate/4)])
		if strings.Contains(out["live"].(string), "beauty") {
			t.Fatalf("a broken live result was shown: %q", out["live"])
		}
		time.Sleep(5 * time.Millisecond)
	}
	ts.waitForLive(t, 2)
	ts.stop(t, id)
	if lives := len(ts.fake.requests()) - len(ts.fake.finals()); lives < 2 {
		t.Fatalf("only %d live passes ran; the test proves nothing", lives)
	}
}

func TestLongSpeechIsCutWithin12Seconds(t *testing.T) {
	ts := newTestService(t, "")
	id := ts.start(t)
	pcm := synth(10, 0.3, 5)
	ts.uploadAll(t, id, pcm, 0)
	if code, _ := ts.stop(t, id); code != http.StatusOK {
		t.Fatalf("stop = %d", code)
	}
	finals := ts.fake.finals()
	if len(finals) != 2 {
		t.Fatalf("final passes = %d, want 2", len(finals))
	}
	first := float64(len(finals[0].pcm)) / sampleRate
	if first < 10 || first > 10.3 {
		t.Fatalf("first stretch %.2f s, want a cut in the dip at 10-10.3 s", first)
	}
	for _, r := range ts.fake.requests() {
		if len(r.pcm) > 12*sampleRate+frameSamples {
			t.Fatalf("a %.1f s request: stretches are capped at 12 s", float64(len(r.pcm))/sampleRate)
		}
	}
}

func TestVocabularyReachesTheEngineAndTheText(t *testing.T) {
	dir := t.TempDir()
	writeVocab(t, dir, "001-citrix.md", "| Term | Heard as |\n|---|---|\n| SecurSpaces | \"secure spaces\" |\n")
	ts := newTestService(t, dir)
	ts.fake.reply = func(r request, n int) (string, error) { return " Check every secure spaces claim.", nil }
	id := ts.start(t)
	ts.uploadAll(t, id, synth(2), 0)
	ts.waitForLive(t, 1)
	code, out := ts.stop(t, id)
	if code != http.StatusOK || out["text"] != "Check every SecurSpaces claim." {
		t.Fatalf("stop = %d %v", code, out)
	}
	sawLive := false
	for _, r := range ts.fake.requests() {
		switch {
		case r.audioCtx == 0 && !strings.HasPrefix(r.prompt, "SecurSpaces."):
			t.Fatalf("final pass prompt %q lacks the vocabulary", r.prompt)
		case r.audioCtx > 0 && strings.Contains(r.prompt, "SecurSpaces."):
			t.Fatalf("live pass prompt %q carries the vocabulary, which slows it down", r.prompt)
		case r.audioCtx > 0:
			sawLive = true
		}
	}
	if !sawLive {
		t.Fatal("no live pass ran; the test proves nothing about them")
	}
}

func TestSilenceIsNoSpeech(t *testing.T) {
	ts := newTestService(t, "")
	id := ts.start(t)
	ts.uploadAll(t, id, synth(0, 3), 0)
	code, out := ts.stop(t, id)
	if code != http.StatusUnprocessableEntity || out["error"] != "no_speech" {
		t.Fatalf("stop = %d %v", code, out)
	}
	if n := len(ts.fake.requests()); n != 0 {
		t.Fatalf("%d engine requests for silence", n)
	}
}

func TestOneDictationAtATime(t *testing.T) {
	ts := newTestService(t, "")
	id := ts.start(t)
	if code, out := ts.do(t, "POST", "/api/voice/dictations", "", nil); code != http.StatusConflict || out["error"] != "in_use" {
		t.Fatalf("second start = %d %v", code, out)
	}
	ts.stop(t, id)
	ts.start(t)
}

func TestUploadChecks(t *testing.T) {
	ts := newTestService(t, "")
	id := ts.start(t)
	path := "/api/voice/dictations/" + id + "/audio"
	for name, c := range map[string]struct {
		path, ct string
		body     []byte
		want     int
	}{
		"unknown dictation":  {"/api/voice/dictations/nope/audio", "application/octet-stream", make([]byte, 2), 404},
		"form post":          {path, "application/x-www-form-urlencoded", make([]byte, 2), 415},
		"no content type":    {path, "", make([]byte, 2), 415},
		"odd length":         {path, "application/octet-stream", make([]byte, 3), 400},
		"over 256 KiB":       {path, "application/octet-stream", make([]byte, maxUpload+2), 413},
		"exactly 256 KiB":    {path, "application/octet-stream", make([]byte, maxUpload), 200},
		"with a charset":     {path, "application/octet-stream; charset=binary", make([]byte, 2), 200},
		"stop of an unknown": {"/api/voice/dictations/nope/stop", "", nil, 404},
		"delete of unknown":  {"/api/voice/dictations/nope", "", nil, 404},
	} {
		method := "POST"
		if strings.HasPrefix(name, "delete") {
			method = "DELETE"
		}
		if code, out := ts.do(t, method, c.path, c.ct, c.body); code != c.want {
			t.Errorf("%s: %d %v, want %d", name, code, out, c.want)
		}
	}
	if n := len(ts.fake.requests()); n != 0 {
		t.Fatalf("refused uploads reached the engine: %d requests", n)
	}
}

func TestTwoMinuteLimit(t *testing.T) {
	ts := newTestService(t, "")
	id := ts.start(t)
	chunk := make([]int16, maxUpload/2)
	var out map[string]any
	for sent := 0; sent <= maxDictationSamples; sent += len(chunk) {
		var code int
		if code, out = ts.upload(t, id, chunk); code != http.StatusOK {
			t.Fatalf("upload = %d %v", code, out)
		}
	}
	if out["limit"] != true {
		t.Fatalf("limit not reported: %v", out)
	}
	d := ts.lookup(id)
	d.mu.Lock()
	kept := len(d.pcm)
	d.mu.Unlock()
	if kept != maxDictationSamples {
		t.Fatalf("kept %d samples, want exactly two minutes", kept)
	}
}

func TestDiscardAndIdleExpiry(t *testing.T) {
	ts := newTestService(t, "")
	id := ts.start(t)
	ts.upload(t, id, synth(1))
	if code, _ := ts.do(t, "DELETE", "/api/voice/dictations/"+id, "", nil); code != http.StatusNoContent {
		t.Fatalf("delete = %d", code)
	}
	if code, _ := ts.upload(t, id, synth(0.1)); code != http.StatusNotFound {
		t.Fatalf("upload after delete = %d", code)
	}
	ts.idleAfter = 100 * time.Millisecond
	id = ts.start(t)
	ts.upload(t, id, synth(0.5))
	time.Sleep(300 * time.Millisecond)
	if code, _ := ts.upload(t, id, synth(0.1)); code != http.StatusNotFound {
		t.Fatalf("an abandoned dictation was kept: %d", code)
	}
	ts.start(t)
}

func TestEngineFailureKeepsTheFinalWords(t *testing.T) {
	ts := newTestService(t, "")
	ts.fake.reply = func(r request, n int) (string, error) {
		if r.audioCtx == 0 && len(ts.fake.finals()) > 1 {
			return "", errors.New("engine gone")
		}
		return numberedWords(r, n)
	}
	id := ts.start(t)
	pcm := synth(1, 0.8, 1, 0.8, 1)
	var code int
	var out map[string]any
	for i := 0; i < len(pcm); i += sampleRate / 4 {
		code, out = ts.upload(t, id, pcm[i:min(len(pcm), i+sampleRate/4)])
		time.Sleep(5 * time.Millisecond)
	}
	code, out = ts.stop(t, id)
	if code != http.StatusBadGateway || out["error"] != "engine_failed" || !strings.HasPrefix(out["text"].(string), "final") {
		t.Fatalf("stop = %d %v, want engine_failed with the first final words", code, out)
	}
	if n := len(ts.fake.finals()); n != 1+finalAttempts {
		t.Fatalf("final passes = %d, want one that worked and %d attempts at the next", n, finalAttempts)
	}
}

func TestRoutesAbsentWhenUnavailable(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, func() *Service { return nil })
	for _, c := range [][2]string{{"POST", "/api/voice/dictations"}, {"POST", "/api/voice/dictations/x/audio"},
		{"POST", "/api/voice/dictations/x/stop"}, {"DELETE", "/api/voice/dictations/x"}} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(c[0], c[1], nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", c[0], c[1], rec.Code)
		}
	}
}

func TestSettings(t *testing.T) {
	logs := captureLog(t)
	for v, want := range map[string]bool{"": true, "on": true, "TRUE": true, "1": true, "off": false, "No": false, "0": false, "disabled": false} {
		t.Setenv("KWA_VOICE", v)
		if on, _, _ := settings(); on != want {
			t.Errorf("KWA_VOICE=%q: on = %v, want %v", v, on, want)
		}
	}
	if !strings.Contains(logs.String(), `KWA_VOICE="disabled" not understood`) {
		t.Fatalf("an unknown value is not logged:\n%s", logs)
	}
	envtest.Clear(t, "KWA_VOICE_BIN")
	t.Setenv("KWA_VOICE_MODEL", "models/x.bin")
	_, bin, model := settings()
	if bin != installed(defaultEngines) || !strings.HasSuffix(model, "/models/x.bin") || model[0] != '/' {
		t.Fatalf("bin %q, model %q", bin, model)
	}
}

// An image built before the rename has the engine under the old name.
func TestInstalledFallsBackToTheOldPlace(t *testing.T) {
	dir := t.TempDir()
	newer, older := filepath.Join(dir, "new"), filepath.Join(dir, "old")
	if got := installed([]string{newer, older}); got != newer {
		t.Errorf("neither installed: %q, want the new place", got)
	}
	if err := os.WriteFile(older, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := installed([]string{newer, older}); got != older {
		t.Errorf("only the old one: %q", got)
	}
	if err := os.WriteFile(newer, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := installed([]string{newer, older}); got != newer {
		t.Errorf("both: %q, want the new place", got)
	}
}
