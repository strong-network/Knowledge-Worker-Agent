// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"sort"
	"sync"
)

// Viewer is one open /events connection. Closing it tells the
// connection to send a final frame naming why, then end.
type Viewer struct {
	// GuestID is empty for the owner.
	GuestID string

	once    sync.Once
	done    chan struct{}
	reason  string
	changed chan struct{}
	queued  chan struct{}
	options chan struct{}
}

// Changed receives when a guest's connection to the session opens or closes.
// Only owner viewers are told; for a guest it never receives.
func (v *Viewer) Changed() <-chan struct{} { return v.changed }

// Queued receives when the session's queue changes. Owner viewers only.
func (v *Viewer) Queued() <-chan struct{} { return v.queued }

// Options receives when the owner changes what guests may do. Guests only.
func (v *Viewer) Options() <-chan struct{} { return v.options }

// Done is closed when the viewer has been told to end.
func (v *Viewer) Done() <-chan struct{} { return v.done }

// Reason says why the viewer was closed; valid once Done is closed.
func (v *Viewer) Reason() string {
	select {
	case <-v.done:
		return v.reason
	default:
		return ""
	}
}

// Close ends the viewer with the given reason; later calls are no-ops.
func (v *Viewer) Close(reason string) {
	v.once.Do(func() {
		v.reason = reason
		close(v.done)
	})
}

var (
	viewersMu sync.Mutex
	viewers   = map[string]map[*Viewer]struct{}{}
)

// AddViewer registers an open connection on a session.
func AddViewer(sessionID, guestID string) *Viewer {
	v := &Viewer{GuestID: guestID, done: make(chan struct{})}
	if guestID == "" {
		v.changed = make(chan struct{}, 1)
		v.queued = make(chan struct{}, 1)
	} else {
		v.options = make(chan struct{}, 1)
	}
	viewersMu.Lock()
	defer viewersMu.Unlock()
	set := viewers[sessionID]
	if set == nil {
		set = map[*Viewer]struct{}{}
		viewers[sessionID] = set
	}
	set[v] = struct{}{}
	if guestID != "" {
		pokeOwners(set)
	}
	return v
}

// RemoveViewer unregisters a connection when it ends.
func RemoveViewer(sessionID string, v *Viewer) {
	viewersMu.Lock()
	defer viewersMu.Unlock()
	if set := viewers[sessionID]; set != nil {
		delete(set, v)
		if v.GuestID != "" {
			pokeOwners(set)
		}
		if len(set) == 0 {
			delete(viewers, sessionID)
		}
	}
}

// pokeOwners tells the owner viewers in set that presence changed. Called with
// viewersMu held; never blocks, since one pending poke already means "re-read".
func pokeOwners(set map[*Viewer]struct{}) {
	for o := range set {
		poke(o.changed)
	}
}

// queueChanged tells the session's owner viewers to re-read its queue: a guest
// may have added to it, and the owner's tab has no other way to learn that.
func queueChanged(sessionID string) {
	viewersMu.Lock()
	defer viewersMu.Unlock()
	for v := range viewers[sessionID] {
		poke(v.queued)
	}
}

// ShareOptionsChanged tells the session's guest viewers to re-read what they
// may do, so a toggle in the owner's modal reaches pages already open.
func ShareOptionsChanged(sessionID string) {
	viewersMu.Lock()
	defer viewersMu.Unlock()
	for v := range viewers[sessionID] {
		poke(v.options)
	}
}

func poke(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Present returns the guests with a connection open to the session, each once
// however many tabs they have, sorted. Never nil.
func Present(sessionID string) []string {
	viewersMu.Lock()
	seen := map[string]bool{}
	for v := range viewers[sessionID] {
		if v.GuestID != "" {
			seen[v.GuestID] = true
		}
	}
	viewersMu.Unlock()
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// CloseViewers ends the session's open connections: guests with guestReason and
// the owner's with ownerReason. An empty reason leaves that kind open. Returns
// how many were closed.
func CloseViewers(sessionID, guestReason, ownerReason string) int {
	viewersMu.Lock()
	var targets []*Viewer
	var reasons []string
	for v := range viewers[sessionID] {
		r := ownerReason
		if v.GuestID != "" {
			r = guestReason
		}
		if r != "" {
			targets = append(targets, v)
			reasons = append(reasons, r)
		}
	}
	viewersMu.Unlock()
	for i, v := range targets {
		v.Close(reasons[i])
	}
	return len(targets)
}

// EndSession ends every connection to a chat that is being deleted. Guests are
// told sharing ended; the owner's other tabs that the chat is gone.
func EndSession(sessionID string) {
	CloseViewers(sessionID, "sharing_ended", "session_deleted")
}
