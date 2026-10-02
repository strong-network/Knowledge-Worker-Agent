// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package voice

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
)

const maxUpload = 256 << 10

// Service runs dictations, one at a time, on the owner's listener only.
type Service struct {
	eng       transcriber
	vocabDir  func() string
	idleAfter time.Duration // a dictation with no upload for this long is discarded
	stopWait  time.Duration

	mu   sync.Mutex
	cur  *dictation
	idle *time.Timer
}

var (
	service   atomic.Pointer[Service]
	available atomic.Bool
)

// Available reports whether dictation can run: turned on, and the engine and
// model are present and the engine runs. Until then the routes answer 404.
func Available() bool { return available.Load() && service.Load() != nil }

// Start reads the settings, clears what an earlier run left behind and checks
// the engine in the background. The returned func stops the engine.
func Start() func() {
	on, bin, model := settings()
	if !on {
		log.Print("[voice] dictation turned off by KWA_VOICE")
		return func() {}
	}
	removeStaleDirs()
	eng := newEngine(bin, model)
	service.Store(&Service{eng: eng, vocabDir: vocabularyDir, idleAfter: 10 * time.Second, stopWait: 45 * time.Second})
	go func() {
		if err := checkEngine(bin, model, nil); err != nil {
			log.Printf("[voice] dictation unavailable: %v", err)
			return
		}
		available.Store(true)
		log.Printf("[voice] dictation available: %s", filepath.Base(model))
	}()
	return eng.Close
}

// settings reads KWA_VOICE, KWA_VOICE_BIN and KWA_VOICE_MODEL. A value of
// KWA_VOICE that isn't understood turns dictation off: an administrator who
// wrote "disabled" meant off.
func settings() (on bool, bin, model string) {
	switch v := strings.ToLower(strings.TrimSpace(env.Get("KWA_VOICE"))); v {
	case "", "1", "true", "yes", "on":
		on = true
	case "0", "false", "no", "off":
	default:
		log.Printf("[voice] KWA_VOICE=%q not understood; dictation is off", v)
	}
	return on, envPath("KWA_VOICE_BIN", installed(defaultEngines)), envPath("KWA_VOICE_MODEL", installed(defaultModels))
}

// The engine and model in the image. Images built before the rename have them
// under sds-chat, and a workspace that builds from source keeps its image.
var (
	defaultEngines = []string{"/usr/lib/knowledge-worker-agent/whisper/whisper-server", "/usr/lib/sds-chat/whisper/whisper-server"}
	defaultModels  = []string{"/usr/share/knowledge-worker-agent/whisper/ggml-small.en-q8_0.bin", "/usr/share/sds-chat/whisper/ggml-small.en-q8_0.bin"}
)

// installed returns the first of paths that exists, or else the first.
func installed(paths []string) string {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return paths[0]
}

func envPath(key, fallback string) string {
	p := strings.TrimSpace(env.Get(key))
	if p == "" {
		return fallback
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// RegisterRoutes adds the dictation routes to the owner's mux.
func RegisterRoutes(mux *http.ServeMux) {
	registerRoutes(mux, func() *Service {
		if !available.Load() {
			return nil
		}
		return service.Load()
	})
}

func registerRoutes(mux *http.ServeMux, get func() *Service) {
	route := func(h func(*Service, http.ResponseWriter, *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			s := get()
			if s == nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "voice_unavailable"})
				return
			}
			h(s, w, r)
		}
	}
	mux.HandleFunc("POST /api/voice/dictations", route((*Service).handleStart))
	mux.HandleFunc("POST /api/voice/dictations/{id}/audio", route((*Service).handleAudio))
	mux.HandleFunc("POST /api/voice/dictations/{id}/stop", route((*Service).handleStop))
	mux.HandleFunc("DELETE /api/voice/dictations/{id}", route((*Service).handleDiscard))
}

func (s *Service) handleStart(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.cur != nil {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "in_use"})
		return
	}
	d := newDictation(newID(), LoadVocabulary(s.vocabDir()), s.eng)
	s.cur = d
	s.idle = time.AfterFunc(s.idleAfter, func() { s.expire(d) })
	s.mu.Unlock()
	go s.eng.Warm()
	go d.run()
	log.Printf("[voice] dictation started (vocabulary: %d terms)", d.vocab.Len())
	writeJSON(w, http.StatusCreated, map[string]string{"id": d.id})
}

func (s *Service) handleAudio(w http.ResponseWriter, r *http.Request) {
	d := s.lookup(r.PathValue("id"))
	if d == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/octet-stream" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "want_octet_stream"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxUpload))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "too_large"})
		return
	case err != nil:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_body"})
		return
	case len(body)%2 != 0:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "odd_length"})
		return
	}
	if d.isStopping() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "stopping"})
		return
	}
	samples := make([]int16, len(body)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(body[2*i:]))
	}
	d.add(samples)
	s.touch(d)
	p, failed := d.progress()
	if failed {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "engine_failed", "text": p.Final})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Service) handleStop(w http.ResponseWriter, r *http.Request) {
	d := s.lookup(r.PathValue("id"))
	if d == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	s.mu.Lock()
	if s.cur == d && s.idle != nil {
		s.idle.Stop()
	}
	s.mu.Unlock()
	d.stop()
	timedOut := false
	select {
	case <-d.done:
	case <-time.After(s.stopWait):
		timedOut = true
		d.discard()
	}
	p, failed := d.progress()
	s.finish(d, "stopped")
	switch {
	case failed || timedOut:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "engine_failed", "text": p.Final})
	case p.Final == "":
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "no_speech"})
	default:
		writeJSON(w, http.StatusOK, map[string]string{"text": p.Final})
	}
}

func (s *Service) handleDiscard(w http.ResponseWriter, r *http.Request) {
	d := s.lookup(r.PathValue("id"))
	if d == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	d.discard()
	s.finish(d, "discarded")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) lookup(id string) *dictation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil || s.cur.id != id {
		return nil
	}
	return s.cur
}

func (s *Service) touch(d *dictation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == d && s.idle != nil {
		s.idle.Reset(s.idleAfter)
	}
}

// expire discards a dictation that stopped sending audio: a closed tab.
func (s *Service) expire(d *dictation) {
	if d.isStopping() {
		return
	}
	d.discard()
	s.finish(d, "discarded after "+s.idleAfter.String()+" without audio")
}

func (s *Service) finish(d *dictation, how string) {
	s.mu.Lock()
	if s.cur != d {
		s.mu.Unlock()
		return
	}
	s.cur = nil
	if s.idle != nil {
		s.idle.Stop()
	}
	s.mu.Unlock()
	d.mu.Lock()
	secs, st := float64(d.samples)/sampleRate, d.stats
	d.pcm = nil
	d.mu.Unlock()
	log.Printf("[voice] dictation %s: %.1f s of audio, %d final passes, %d live updates, %d skipped",
		how, secs, st.final, st.live, st.skipped)
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
