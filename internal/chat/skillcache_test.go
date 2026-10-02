// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodeserver"
)

// countingSkills stubs the enumeration and reports how many times it ran, which
// is the only way to tell "served from cache" from "asked opencode again".
func countingSkills(t *testing.T, skills []opencodeserver.Skill, err error) *int32Counter {
	t.Helper()
	c := &int32Counter{}
	prev := listSkillsFn
	listSkillsFn = func(context.Context, string) ([]opencodeserver.Skill, error) {
		c.inc()
		return skills, err
	}
	InvalidateSkillCache()
	t.Cleanup(func() {
		listSkillsFn = prev
		InvalidateSkillCache()
	})
	return c
}

type int32Counter struct {
	mu sync.Mutex
	n  int
}

func (c *int32Counter) inc() {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}

func (c *int32Counter) get() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// captureRevalidations replaces the background refresh with a synchronous
// recorder. Tests assert on which directories were scheduled without having to
// wait on goroutines.
func captureRevalidations(t *testing.T) *[]string {
	t.Helper()
	var mu sync.Mutex
	got := []string{}
	prev := startRevalidate
	startRevalidate = func(dir string) {
		mu.Lock()
		got = append(got, dir)
		mu.Unlock()
	}
	t.Cleanup(func() { startRevalidate = prev })
	return &got
}

// chatWorkspace sets up a workspace with a Chats/ subdirectory and returns both
// paths, so tests can build the two folder shapes IsAutoChat accepts.
func chatWorkspace(t *testing.T) (workspace, chats string) {
	t.Helper()
	workspace = t.TempDir()
	chats = filepath.Join(workspace, "Chats")
	if err := os.MkdirAll(chats, 0o755); err != nil {
		t.Fatalf("mkdir chats: %v", err)
	}
	prev := config.Workspace
	config.Workspace = workspace
	t.Cleanup(func() { config.Workspace = prev })
	return workspace, chats
}

// mkdir creates a directory and returns it.
func mkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	return path
}

var oneSkill = []opencodeserver.Skill{{Name: "pdf", Description: "Work with PDFs", Content: "body"}}

func TestSkillCacheServesSecondCallWithoutAsking(t *testing.T) {
	_, chats := chatWorkspace(t)
	dir := mkdir(t, filepath.Join(chats, "chat-aaaaaaaaaaaa"))
	calls := countingSkills(t, oneSkill, nil)

	if _, ok := ListSkills(context.Background(), dir); !ok {
		t.Fatal("first call should succeed")
	}
	if _, ok := ListSkills(context.Background(), dir); !ok {
		t.Fatal("second call should succeed")
	}
	if calls.get() != 1 {
		t.Fatalf("expected the second call to be served from cache, got %d enumerations", calls.get())
	}
}

func TestSkillCacheStaleEntryIsServedAndRevalidated(t *testing.T) {
	_, chats := chatWorkspace(t)
	dir := mkdir(t, filepath.Join(chats, "chat-bbbbbbbbbbbb"))
	calls := countingSkills(t, oneSkill, nil)
	scheduled := captureRevalidations(t)

	if _, ok := ListSkills(context.Background(), dir); !ok {
		t.Fatal("first call should succeed")
	}
	// Age the entry past its TTL.
	skillCacheMu.Lock()
	e := skillCache[dir]
	e.fetchedAt = time.Now().Add(-skillCacheTTL - time.Minute)
	skillCache[dir] = e
	skillCacheMu.Unlock()

	list, ok := ListSkills(context.Background(), dir)
	if !ok || len(list) != 1 {
		t.Fatalf("stale entry should still be served immediately, got ok=%v list=%+v", ok, list)
	}
	if calls.get() != 1 {
		t.Fatalf("a stale read must not block on opencode, got %d enumerations", calls.get())
	}
	if len(*scheduled) != 1 || (*scheduled)[0] != dir {
		t.Fatalf("expected one background refresh for %q, got %v", dir, *scheduled)
	}
}

func TestSkillCacheSeedsFromSiblingChatFolder(t *testing.T) {
	_, chats := chatWorkspace(t)
	warm := mkdir(t, filepath.Join(chats, "chat-cccccccccccc"))
	fresh := mkdir(t, filepath.Join(chats, "chat-dddddddddddd"))
	calls := countingSkills(t, oneSkill, nil)
	scheduled := captureRevalidations(t)

	// Warm one sibling the honest way.
	if _, ok := ListSkills(context.Background(), warm); !ok {
		t.Fatal("warming call should succeed")
	}

	list, ok := ListSkills(context.Background(), fresh)
	if !ok || len(list) != 1 || list[0].Name != "pdf" {
		t.Fatalf("a brand-new chat folder should be seeded from its sibling, got ok=%v list=%+v", ok, list)
	}
	if calls.get() != 1 {
		t.Fatalf("seeding must not wait on opencode, got %d enumerations", calls.get())
	}
	if len(*scheduled) != 1 || (*scheduled)[0] != fresh {
		t.Fatalf("expected the seeded directory to be revalidated, got %v", *scheduled)
	}

	// The borrowed answer must not be recorded as the new directory's own, or
	// one seed becomes the source for the next.
	skillCacheMu.Lock()
	_, stored := skillCache[fresh]
	skillCacheMu.Unlock()
	if stored {
		t.Fatal("a seeded answer must not be written to the cache")
	}
}

func TestSkillCacheDoesNotSeedClientChosenWorkdir(t *testing.T) {
	workspace, chats := chatWorkspace(t)
	warm := mkdir(t, filepath.Join(chats, "chat-eeeeeeeeeeee"))
	// A repo clone or hand-picked folder: not an auto-chat folder, and it may
	// carry a project-local .opencode/skills tier of its own.
	chosen := mkdir(t, filepath.Join(workspace, "my-project"))
	calls := countingSkills(t, oneSkill, nil)

	if _, ok := ListSkills(context.Background(), warm); !ok {
		t.Fatal("warming call should succeed")
	}
	if _, ok := ListSkills(context.Background(), chosen); !ok {
		t.Fatal("chosen workdir should still resolve")
	}
	if calls.get() != 2 {
		t.Fatalf("a client-chosen workdir must be enumerated, not seeded; got %d enumerations", calls.get())
	}
}

func TestSkillCacheDoesNotSeedAcrossDifferentParents(t *testing.T) {
	workspace, chats := chatWorkspace(t)
	// Both of these satisfy IsAutoChat — the legacy shape sits directly in the
	// workspace, the current one under Chats/ — but their ancestor chains
	// differ by a level, so neither may seed the other.
	warm := mkdir(t, filepath.Join(chats, "chat-ffffffffffff"))
	legacy := mkdir(t, filepath.Join(workspace, "chat-111111111111"))
	calls := countingSkills(t, oneSkill, nil)

	if _, ok := ListSkills(context.Background(), warm); !ok {
		t.Fatal("warming call should succeed")
	}
	if _, ok := ListSkills(context.Background(), legacy); !ok {
		t.Fatal("legacy folder should still resolve")
	}
	if calls.get() != 2 {
		t.Fatalf("seeding must require the same parent, not merely IsAutoChat; got %d enumerations", calls.get())
	}
}

func TestSkillCacheFailedRefreshKeepsExistingEntry(t *testing.T) {
	_, chats := chatWorkspace(t)
	dir := mkdir(t, filepath.Join(chats, "chat-222222222222"))

	// Populate honestly, then make every later enumeration fail.
	countingSkills(t, oneSkill, nil)
	if _, ok := ListSkills(context.Background(), dir); !ok {
		t.Fatal("warming call should succeed")
	}
	prev := listSkillsFn
	listSkillsFn = func(context.Context, string) ([]opencodeserver.Skill, error) {
		return nil, errors.New("connection refused")
	}
	t.Cleanup(func() { listSkillsFn = prev })

	revalidateSkills(dir)

	list, ok := ListSkills(context.Background(), dir)
	if !ok || len(list) != 1 {
		t.Fatalf("a failed refresh must not evict a good entry, got ok=%v list=%+v", ok, list)
	}
}

func TestSkillCacheCanonicalizesTheKey(t *testing.T) {
	_, chats := chatWorkspace(t)
	dir := mkdir(t, filepath.Join(chats, "chat-333333333333"))
	calls := countingSkills(t, oneSkill, nil)

	if _, ok := ListSkills(context.Background(), dir); !ok {
		t.Fatal("first call should succeed")
	}
	// Same directory, spelled with a redundant path segment. If the key is not
	// canonicalized this misses silently and the cache stops being a cache.
	alias := filepath.Join(chats, ".", "chat-333333333333") + string(filepath.Separator)
	if _, ok := ListSkills(context.Background(), alias); !ok {
		t.Fatal("aliased call should succeed")
	}
	if calls.get() != 1 {
		t.Fatalf("two spellings of one directory must share an entry, got %d enumerations", calls.get())
	}
}

func TestSkillCacheUnknownWorkdirFallsBackToWorkspace(t *testing.T) {
	workspace, _ := chatWorkspace(t)
	calls := countingSkills(t, oneSkill, nil)

	// A path that does not exist resolves to the workspace, matching what
	// opencodeserver.canonicalDir does below this layer.
	if _, ok := ListSkills(context.Background(), filepath.Join(workspace, "gone")); !ok {
		t.Fatal("missing workdir should fall back, not fail")
	}
	if _, ok := ListSkills(context.Background(), workspace); !ok {
		t.Fatal("workspace call should succeed")
	}
	if calls.get() != 1 {
		t.Fatalf("a missing workdir should share the workspace entry, got %d enumerations", calls.get())
	}
}

func TestSkillCacheRevalidationIsSingleFlight(t *testing.T) {
	_, chats := chatWorkspace(t)
	dir := mkdir(t, filepath.Join(chats, "chat-444444444444"))

	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	calls := &int32Counter{}
	prev := listSkillsFn
	listSkillsFn = func(context.Context, string) ([]opencodeserver.Skill, error) {
		calls.inc()
		entered <- struct{}{}
		<-release
		return oneSkill, nil
	}
	InvalidateSkillCache()
	t.Cleanup(func() {
		listSkillsFn = prev
		InvalidateSkillCache()
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		revalidateSkills(dir)
	}()

	// Block until the first refresh is provably inside the enumeration. Only
	// then is the in-flight flag set, and only then does "a second caller
	// arrives while one is running" actually describe the situation. Racing a
	// second caller against an unsynchronized first would test nothing: if the
	// first finished before the second looked, the second is *supposed* to run.
	<-entered

	// A caller that arrives while a refresh is in flight must return at once,
	// without starting a second opencode process.
	revalidateSkills(dir)
	if got := calls.get(); got != 1 {
		t.Fatalf("a refresh arriving during another must be dropped, got %d enumerations", got)
	}

	close(release)
	<-done

	// ...and the flag must be released afterwards, or the first refresh would
	// wedge the directory permanently.
	revalidateSkills(dir)
	if got := calls.get(); got != 2 {
		t.Fatalf("a refresh after the in-flight one finished must run, got %d enumerations", got)
	}
}

func TestInvalidateSkillCacheForcesAFreshRead(t *testing.T) {
	_, chats := chatWorkspace(t)
	dir := mkdir(t, filepath.Join(chats, "chat-555555555555"))
	calls := countingSkills(t, oneSkill, nil)

	if _, ok := ListSkills(context.Background(), dir); !ok {
		t.Fatal("first call should succeed")
	}
	InvalidateSkillCache()
	if _, ok := ListSkills(context.Background(), dir); !ok {
		t.Fatal("post-invalidation call should succeed")
	}
	if calls.get() != 2 {
		t.Fatalf("invalidation should force a fresh enumeration, got %d", calls.get())
	}
}
