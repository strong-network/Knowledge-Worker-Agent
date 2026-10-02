// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package providers

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Reach classifies whether a provider can actually serve a turn, as opposed to
// whether we hold a credential for it. The two are independent: a workspace can
// hold perfectly valid Vertex credentials and still be refused by an
// organization's VPC Service Controls perimeter, which no auth check can see.
type Reach string

const (
	// ReachUnknown — never probed, or the last probe could not be run. Treated
	// as usable everywhere: a provider is not penalised for a test we failed to
	// perform.
	ReachUnknown Reach = ""
	// ReachOK — a probe completed and the provider answered.
	ReachOK Reach = "ok"
	// ReachUnreachable — a probe completed and the provider did not answer.
	ReachUnreachable Reach = "unreachable"
)

// ProbeFunc tests one provider, returning nil when it answered. The error text
// is shown to the user, so it should be the provider's own message where one is
// available.
type ProbeFunc func(ctx context.Context, providerID string) error

// probeTimeout bounds a single probe. Generous: the fallback probe runs a real
// (one-token) turn, which on a cold provider takes several seconds.
const probeTimeout = 45 * time.Second

type reachResult struct {
	state     Reach
	note      string
	checkedAt time.Time
}

var (
	reachMu      sync.RWMutex
	reachByID    = map[string]reachResult{}
	probeFns     = map[string]ProbeFunc{}
	defaultProbe ProbeFunc
	// onReachChange is called when a probe changes a provider's verdict, so the
	// model list can be rebuilt. A provider that has just become unreachable
	// must stop backing the Default preset.
	onReachChange func()
)

// SetDefaultProbe registers the probe used for any provider without one of its
// own. Wired by cmd/server, which owns the opencode invocation this package
// must not depend on.
func SetDefaultProbe(fn ProbeFunc) {
	reachMu.Lock()
	defer reachMu.Unlock()
	defaultProbe = fn
}

// SetProbe registers a provider-specific probe, which takes precedence over the
// default. Worth supplying when the provider's own API reports a failure reason
// the generic path cannot see.
func SetProbe(providerID string, fn ProbeFunc) {
	reachMu.Lock()
	defer reachMu.Unlock()
	probeFns[providerID] = fn
}

// SetReachChangeHook registers the callback fired when a verdict changes.
func SetReachChangeHook(fn func()) {
	reachMu.Lock()
	defer reachMu.Unlock()
	onReachChange = fn
}

func probeFor(id string) ProbeFunc {
	reachMu.RLock()
	defer reachMu.RUnlock()
	if fn, ok := probeFns[id]; ok && fn != nil {
		return fn
	}
	return defaultProbe
}

// reachFor reports the cached verdict for a provider.
func reachFor(id string) reachResult {
	reachMu.RLock()
	defer reachMu.RUnlock()
	return reachByID[id]
}

// Unreachable reports whether a provider has been proven unable to serve a
// turn. Unknown counts as reachable, so an unprobed provider is never withheld.
func Unreachable(id string) bool {
	return reachFor(id).state == ReachUnreachable
}

// Probe tests one provider now and caches the verdict, returning it. Callers
// that only want the cached value should read the registry instead.
func Probe(ctx context.Context, id string) (Reach, string) {
	fn := probeFor(id)
	if fn == nil {
		return ReachUnknown, ""
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	state, note := ReachOK, ""
	if err := fn(ctx, id); err != nil {
		state, note = ReachUnreachable, err.Error()
	}
	store(id, state, note)
	return state, note
}

func store(id string, state Reach, note string) {
	reachMu.Lock()
	changed := reachByID[id].state != state
	reachByID[id] = reachResult{state: state, note: note, checkedAt: time.Now()}
	hook := onReachChange
	reachMu.Unlock()
	if changed && hook != nil {
		hook()
	}
}

// Forget drops a cached verdict, so the next refresh re-probes from scratch.
// Used after a sign-out or sign-in, where the old verdict describes credentials
// that no longer exist.
func Forget(id string) {
	reachMu.Lock()
	delete(reachByID, id)
	reachMu.Unlock()
}

// RefreshReachability probes every authenticated provider concurrently and
// returns once all have settled. Providers we cannot probe are left Unknown.
func RefreshReachability(ctx context.Context) {
	ids := Resolve().Authenticated()
	var wg sync.WaitGroup
	for _, id := range ids {
		if probeFor(id) == nil {
			continue
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			Probe(ctx, id)
		}(id)
	}
	wg.Wait()

	// A provider that is no longer authenticated should not keep an old verdict:
	// re-authenticating must start from a clean test.
	live := map[string]bool{}
	for _, id := range ids {
		live[id] = true
	}
	reachMu.Lock()
	for id := range reachByID {
		if !live[id] {
			delete(reachByID, id)
		}
	}
	reachMu.Unlock()
}

// UsableProviders returns the authenticated providers that are not known to be
// unreachable, in registry order. This is the set that may contribute models.
func UsableProviders() []string {
	all := Resolve().Authenticated()
	out := make([]string, 0, len(all))
	for _, id := range all {
		if !Unreachable(id) {
			out = append(out, id)
		}
	}
	return out
}

// probedIDs lists the providers holding a cached verdict, for tests.
func probedIDs() []string {
	reachMu.RLock()
	defer reachMu.RUnlock()
	out := make([]string, 0, len(reachByID))
	for id := range reachByID {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
