// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodeserver"
)

// fastSettle shrinks the settle timing so the tests take milliseconds.
func fastSettle(t *testing.T) {
	t.Helper()
	poll, settle, grace := skillPoll, skillSettle, skillEmptyGrace
	skillPoll, skillSettle, skillEmptyGrace = 2*time.Millisecond, 20*time.Millisecond, 60*time.Millisecond
	t.Cleanup(func() { skillPoll, skillSettle, skillEmptyGrace = poll, settle, grace })
}

func skillsNamed(names ...string) []opencodeserver.Skill {
	out := make([]opencodeserver.Skill, len(names))
	for i, n := range names {
		out[i] = opencodeserver.Skill{Name: n, Description: n}
	}
	return out
}

// answers replies with each list in turn, then the last one for good.
func answers(lists ...[]opencodeserver.Skill) (func(context.Context) ([]opencodeserver.Skill, error), *int) {
	calls := 0
	return func(context.Context) ([]opencodeserver.Skill, error) {
		calls++
		if calls <= len(lists) {
			return lists[calls-1], nil
		}
		return lists[len(lists)-1], nil
	}, &calls
}

// What 2.x does for a folder it hasn't used yet: nothing, then its built-in
// skills, then the rest.
func TestSettleSkillsWaitsForAFolderToFinishLoading(t *testing.T) {
	fastSettle(t)
	full := skillsNamed("browser", "opencode", "report", "pdf", "ux-writing")
	fetch, _ := answers(nil, skillsNamed("opencode", "report"), skillsNamed("browser", "opencode", "report"), full)

	got, err := settleSkills(context.Background(), fetch)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(full) {
		t.Fatalf("got %d skills, want all %d: %+v", len(got), len(full), got)
	}
}

func TestSettleSkillsIgnoresTheOrder(t *testing.T) {
	fastSettle(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	calls := 0
	fetch := func(context.Context) ([]opencodeserver.Skill, error) {
		calls++
		if calls%2 == 0 {
			return skillsNamed("b", "a"), nil
		}
		return skillsNamed("a", "b"), nil
	}

	if _, err := settleSkills(ctx, fetch); err != nil {
		t.Fatalf("%v after %d calls: a reordered list should count as unchanged", err, calls)
	}
}

func TestSettleSkillsAcceptsAnEmptyListAfterTheGrace(t *testing.T) {
	fastSettle(t)
	fetch, _ := answers(nil)

	start := time.Now()
	got, err := settleSkills(context.Background(), fetch)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
	if took := time.Since(start); took < skillEmptyGrace {
		t.Errorf("gave up on an empty list after %s, before the grace of %s", took, skillEmptyGrace)
	}
}

// A request that ends while the list is still filling must not leave a partial
// list behind for the cache.
func TestSettleSkillsReportsACancelledRequest(t *testing.T) {
	fastSettle(t)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	fetch := func(context.Context) ([]opencodeserver.Skill, error) {
		calls++
		if calls == 2 {
			cancel()
		}
		return skillsNamed(string(rune('a' + calls))), nil
	}

	got, err := settleSkills(ctx, fetch)
	if !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("got %+v, %v; want no list and context.Canceled", got, err)
	}
}

func TestSettleSkillsReturnsAFailure(t *testing.T) {
	fastSettle(t)
	boom := errors.New("server gone")
	fetch := func(context.Context) ([]opencodeserver.Skill, error) { return nil, boom }

	if _, err := settleSkills(context.Background(), fetch); !errors.Is(err, boom) {
		t.Fatalf("got %v, want %v", err, boom)
	}
}
