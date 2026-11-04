// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package onboarding keeps what the setup guide and what's new need to remember
// between page loads: whether the user closed or hid the guide, whether they
// asked to be reminded later, and which connectors and model providers they
// have been told about.
//
// "Closed" and "later" last until Knowledge Worker Agent starts again, so they
// are stored with the id of the start they happened in. Deciding what to show
// is the browser's job: it has the providers' and connectors' status.
package onboarding

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

const metaKey = "onboarding"

const (
	maxName  = 200
	maxNames = 1000
)

// startID tells this start of the server from the others.
var startID = newStartID()

func newStartID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// state is the stored record.
type state struct {
	GuideHidden        bool     `json:"guide_hidden,omitempty"`
	GuideClosedStart   string   `json:"guide_closed_start,omitempty"`
	WhatsNewLaterStart string   `json:"whats_new_later_start,omitempty"`
	SeenConnectors     []string `json:"seen_connectors,omitempty"`
	SeenProviders      []string `json:"seen_providers,omitempty"`
}

// View is what the browser gets: the start-scoped fields as booleans for this
// start.
type View struct {
	GuideHidden    bool     `json:"guideHidden"`
	GuideClosed    bool     `json:"guideClosed"`
	WhatsNewLater  bool     `json:"whatsNewLater"`
	SeenConnectors []string `json:"seenConnectors"`
	SeenProviders  []string `json:"seenProviders"`
}

var mu sync.Mutex

func load() state {
	var s state
	if raw, ok := db.GetAppMeta(metaKey); ok {
		_ = json.Unmarshal([]byte(raw), &s)
	}
	return s
}

func save(s state) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return db.SetAppMeta(metaKey, string(b))
}

func (s state) view() View {
	return View{
		GuideHidden:    s.GuideHidden,
		GuideClosed:    s.GuideClosedStart == startID,
		WhatsNewLater:  s.WhatsNewLaterStart == startID,
		SeenConnectors: nonNil(s.SeenConnectors),
		SeenProviders:  nonNil(s.SeenProviders),
	}
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// update applies change to the stored state and returns the result.
func update(change func(*state)) (View, error) {
	mu.Lock()
	defer mu.Unlock()
	s := load()
	change(&s)
	if err := save(s); err != nil {
		return View{}, err
	}
	return s.view(), nil
}

// addNames adds names to list, skipping blank, overlong and known ones. A list
// over the cap loses its oldest names.
func addNames(list, names []string, lower bool) []string {
	known := make(map[string]bool, len(list))
	for _, n := range list {
		known[n] = true
	}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if lower {
			n = strings.ToLower(n)
		}
		if n == "" || utf8.RuneCountInString(n) > maxName || known[n] {
			continue
		}
		known[n] = true
		list = append(list, n)
	}
	if len(list) > maxNames {
		list = list[len(list)-maxNames:]
	}
	return list
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func reply(w http.ResponseWriter, v View, err error) {
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't save that. Try again.")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// HandleGet — GET /api/onboarding.
func HandleGet(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	v := load().view()
	mu.Unlock()
	writeJSON(w, http.StatusOK, v)
}

// HandleGuide — POST /api/onboarding/guide {hide}: the guide was closed during
// this start, and with hide, for good.
func HandleGuide(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Hide bool `json:"hide"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "The request body wasn't valid.")
		return
	}
	v, err := update(func(s *state) {
		s.GuideClosedStart = startID
		if body.Hide {
			s.GuideHidden = true
		}
	})
	reply(w, v, err)
}

// HandleLater — POST /api/onboarding/whats-new/later: show what's new again at
// the next start.
func HandleLater(w http.ResponseWriter, r *http.Request) {
	v, err := update(func(s *state) { s.WhatsNewLaterStart = startID })
	reply(w, v, err)
}

// HandleSeen — POST /api/onboarding/seen {connectors, providers}: the user has
// been told about these. Connector names are kept in lower case, as the
// connector status is keyed.
func HandleSeen(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Connectors []string `json:"connectors"`
		Providers  []string `json:"providers"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "The request body wasn't valid.")
		return
	}
	v, err := update(func(s *state) {
		s.SeenConnectors = addNames(s.SeenConnectors, body.Connectors, true)
		s.SeenProviders = addNames(s.SeenProviders, body.Providers, false)
	})
	reply(w, v, err)
}
