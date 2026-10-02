// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/workdirs"
)

// The skills picker reads through this cache so that opening it is a local
// map lookup rather than a bet on process lifecycle. Without it, every open
// calls Supervisor.Ensure, which starts an `opencode serve` for the workdir and
// blocks on its health check — bounded at skillListTimeout, i.e. up to a minute
// on a cold chat. That was tolerable while skills were buried behind the "+"
// menu; it is not once the composer offers them directly.
//
// What is cached is opencode's own answer, never a reconstruction of it.
// opencode unions several config tiers to produce the list (see
// opencodeserver.ListSkills), and a Go-side scanner that tried to reproduce
// that union would be free to drift from it. Caching keeps the enumeration
// authoritative and only decouples *when* it is paid for.
//
// Summaries are cached, not raw opencodeserver.Skill values: those carry the
// whole SKILL.md body in Content, which runs to tens of kilobytes per skill and
// has no business sitting in a process-global map keyed by every chat folder
// the workspace has ever opened.
//
// The send path deliberately does not read this cache — see resolveForcedSkill,
// which needs Content and Location and must resolve them at send time.

// skillCacheTTL is how long an enumeration is served without revalidating. It
// mirrors opencodeModelRefreshInterval in cmd/server: skills change when a
// SKILL.md is added or the bundled defaults are reinstalled, which is rare, and
// a stale entry is corrected in the background rather than waited on.
const skillCacheTTL = 30 * time.Minute

type skillCacheEntry struct {
	skills    []SkillSummary
	fetchedAt time.Time
}

var (
	skillCacheMu sync.Mutex
	skillCache   = map[string]skillCacheEntry{}
	// skillRevalidating single-flights background refreshes. Each one may start
	// an opencode process, so a user clicking the picker repeatedly on a cold
	// workspace must not queue up a process per click.
	skillRevalidating = map[string]bool{}
)

// canonicalSkillDir normalizes a workdir into a cache key.
//
// It has to mirror opencodeserver.canonicalDir, which is unexported and applied
// below this layer: fetchSkills only learns the canonical directory as Ensure's
// second return value, i.e. after the expensive call this cache exists to
// avoid. Diverging here is silent — two spellings of one directory become two
// entries that each miss, and the cache quietly degrades to no cache at all.
func canonicalSkillDir(workdir string) string {
	d := workdir
	if info, err := os.Stat(d); err != nil || !info.IsDir() {
		d = config.Workspace
	}
	if abs, err := filepath.Abs(d); err == nil {
		d = abs
	}
	return d
}

// lookupSkillCache returns the entry for an already-canonical dir.
func lookupSkillCache(dir string) (skillCacheEntry, bool) {
	skillCacheMu.Lock()
	defer skillCacheMu.Unlock()
	e, ok := skillCache[dir]
	return e, ok
}

// storeSkillCache records a successful enumeration for its own directory.
// Only real answers are stored: a seeded list is served but never written, so
// a borrowed answer can never become the source another directory borrows from.
func storeSkillCache(dir string, skills []SkillSummary) {
	skillCacheMu.Lock()
	defer skillCacheMu.Unlock()
	skillCache[dir] = skillCacheEntry{skills: skills, fetchedAt: time.Now()}
}

// InvalidateSkillCache drops every cached enumeration. It is called after the
// bootstrap pass that installs the bundled skills, which is the one moment we
// know the answer may have changed.
func InvalidateSkillCache() {
	skillCacheMu.Lock()
	defer skillCacheMu.Unlock()
	skillCache = map[string]skillCacheEntry{}
}

// seedFromSibling supplies a provisional answer for a directory that has never
// been enumerated, by borrowing the newest one from a sibling chat folder.
//
// This is what makes the cache useful in the case the picker exists for: the
// first open in a brand-new chat, whose folder was created seconds earlier and
// so can never be a hit. Two conditions must both hold, and the second is not
// implied by the first:
//
//   - the directory is an auto-created per-chat folder (workdirs.IsAutoChat).
//     A workdir the user chose — an existing folder or a repo clone — can carry
//     a project-local .opencode/skills tier of its own, so it is never seeded.
//   - the candidate has the same parent. IsAutoChat accepts both
//     <workspace>/Chats/chat-* and the legacy <workspace>/chat-*, whose ancestor
//     chains differ by a level. Requiring an identical parent means the seed
//     holds whether or not opencode walks parent directories looking for a
//     project tier — an assumption about an external binary we should not make.
//
// Auto-created chat folders are scaffolded with only inputs/, working/ and
// .system/, and nothing here ever writes an .opencode/ tier into one, so
// siblings resolve skills from an identical ancestor chain and an identically
// empty project tier.
func seedFromSibling(dir string) ([]SkillSummary, bool) {
	if !workdirs.IsAutoChat(dir) {
		return nil, false
	}
	parent := filepath.Dir(dir)

	skillCacheMu.Lock()
	defer skillCacheMu.Unlock()

	var best skillCacheEntry
	var found bool
	for candidate, e := range skillCache {
		if candidate == dir || filepath.Dir(candidate) != parent {
			continue
		}
		if !workdirs.IsAutoChat(candidate) {
			continue
		}
		if !found || e.fetchedAt.After(best.fetchedAt) {
			best, found = e, true
		}
	}
	if !found {
		return nil, false
	}
	return best.skills, true
}

// revalidateSkills refreshes a directory's entry in the background.
//
// It runs on its own context: the request that triggered it is answered from
// the cache and its context is cancelled the moment the response is written,
// which would abort the refresh before opencode finished starting.
//
// A failed enumeration leaves any existing entry alone. The failure being
// worked around here is a timeout, and letting a timeout evict a known-good
// list would turn the cache into a liability on exactly the boot where opencode
// is unwell.
func revalidateSkills(dir string) {
	skillCacheMu.Lock()
	if skillRevalidating[dir] {
		skillCacheMu.Unlock()
		return
	}
	skillRevalidating[dir] = true
	skillCacheMu.Unlock()

	defer func() {
		skillCacheMu.Lock()
		delete(skillRevalidating, dir)
		skillCacheMu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), skillListTimeout)
	defer cancel()

	skills, err := enumerateSkills(ctx, dir)
	if err != nil {
		log.Printf("[SKILLS] background refresh failed for %q: %v", dir, err)
		return
	}
	storeSkillCache(dir, skills)
}

// startRevalidate is a seam: tests replace it to observe refreshes without
// spawning goroutines they would then have to wait on.
var startRevalidate = func(dir string) { go revalidateSkills(dir) }
