// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package providers

import (
	"context"
	"errors"
	"testing"
)

func resetReach(t *testing.T) {
	t.Helper()
	clear := func() {
		reachMu.Lock()
		reachByID = map[string]reachResult{}
		probeFns = map[string]ProbeFunc{}
		signOutFns = map[string]func() error{}
		defaultProbe = nil
		onReachChange = nil
		reachMu.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

func TestUnknownReachabilityCountsAsUsable(t *testing.T) {
	resetReach(t)
	// A provider we have never managed to test must not be withheld: failing to
	// run a check is not evidence against the provider.
	if Unreachable("never-probed") {
		t.Fatal("an unprobed provider must not count as unreachable")
	}
}

func TestProbeRecordsVerdictAndMessage(t *testing.T) {
	resetReach(t)
	SetProbe("blocked", func(context.Context, string) error {
		return errors.New("prohibited by organization's policy")
	})
	SetProbe("fine", func(context.Context, string) error { return nil })

	if state, note := Probe(context.Background(), "blocked"); state != ReachUnreachable ||
		note != "prohibited by organization's policy" {
		t.Fatalf("got %q / %q", state, note)
	}
	if !Unreachable("blocked") {
		t.Fatal("a failed probe must mark the provider unreachable")
	}

	if state, _ := Probe(context.Background(), "fine"); state != ReachOK {
		t.Fatalf("got %q, want ok", state)
	}
	if Unreachable("fine") {
		t.Fatal("a passing probe must not mark the provider unreachable")
	}
}

func TestProviderSpecificProbeBeatsDefault(t *testing.T) {
	resetReach(t)
	SetDefaultProbe(func(context.Context, string) error { return errors.New("generic") })
	SetProbe("special", func(context.Context, string) error { return errors.New("specific") })

	if _, note := Probe(context.Background(), "special"); note != "specific" {
		t.Fatalf("provider probe should win, got %q", note)
	}
	if _, note := Probe(context.Background(), "other"); note != "generic" {
		t.Fatalf("default probe should apply, got %q", note)
	}
}

func TestReachChangeHookFiresOnlyOnChange(t *testing.T) {
	resetReach(t)
	calls := 0
	SetReachChangeHook(func() { calls++ })
	SetProbe("p", func(context.Context, string) error { return errors.New("down") })

	Probe(context.Background(), "p")
	if calls != 1 {
		t.Fatalf("expected 1 call after the first verdict, got %d", calls)
	}
	// Re-probing to the same verdict must not churn the model list.
	Probe(context.Background(), "p")
	if calls != 1 {
		t.Fatalf("an unchanged verdict must not fire the hook, got %d", calls)
	}

	SetProbe("p", func(context.Context, string) error { return nil })
	Probe(context.Background(), "p")
	if calls != 2 {
		t.Fatalf("expected the hook on recovery, got %d", calls)
	}
}

func TestForgetDropsVerdict(t *testing.T) {
	resetReach(t)
	SetProbe("p", func(context.Context, string) error { return errors.New("down") })
	Probe(context.Background(), "p")
	if len(probedIDs()) != 1 {
		t.Fatalf("expected a cached verdict, got %v", probedIDs())
	}
	Forget("p")
	if Unreachable("p") || len(probedIDs()) != 0 {
		t.Fatal("Forget must clear the cached verdict")
	}
}

func TestCanSignOut(t *testing.T) {
	resetReach(t)
	SetKeyStore(nil, func(string) error { return nil })
	t.Cleanup(func() { SetKeyStore(nil, nil) })

	SetSignOut("builtin", func() error { return nil })

	if !CanSignOut(Provider{ID: "builtin", Authenticated: true}) {
		t.Fatal("a builtin with a sign-out action should offer it")
	}
	if CanSignOut(Provider{ID: "builtin", Authenticated: false}) {
		t.Fatal("a provider that is not signed in has nothing to sign out of")
	}
	if !CanSignOut(Provider{ID: "k", Authenticated: true, AuthType: AuthUserKey}) {
		t.Fatal("a user-key provider signs out by dropping its key")
	}
	// A managed credential belongs to the platform; the user cannot restore it.
	if CanSignOut(Provider{ID: "m", Authenticated: true, AuthType: AuthManaged}) {
		t.Fatal("a managed provider must not offer sign-out")
	}
}
