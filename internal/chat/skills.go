// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"context"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodeserver"
)

// skillListTimeout bounds how long the picker (and the send path) will wait for
// opencode to enumerate skills. Starting a fresh instance dominates this; once
// one is warm the call is local and instant.
const skillListTimeout = 60 * time.Second

// Turn carries settings that belong to a single prompt and are deliberately
// not persisted with the session. A turn that does not name a skill
// cannot have one, which is what makes "the arm resets after send" a property
// of the request rather than something the browser has to remember to do.
type Turn struct {
	// Skill is the name of a skill the user armed in the composer for this
	// one message. Empty means nothing is forced.
	Skill string
	// Author is who wrote the prompt; the zero value is the owner.
	Author Author
	// ID names the turn. The send paths set it when they store the prompt as
	// the turn's user message, so that row, the reply and the live turn carry
	// the same id. A turn without one gets a fresh id, and announces no prompt.
	ID string
}

// firstTurn collapses the variadic turn argument the chat entry points take.
func firstTurn(turn []Turn) Turn {
	if len(turn) == 0 {
		return Turn{}
	}
	return turn[0]
}

// SkillSummary is the browser-facing shape of a skill: identity and the short
// description shown in the picker. The SKILL.md body is deliberately absent —
// it can be tens of kilobytes, the picker has no use for it, and it is injected
// server-side on send.
type SkillSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// listSkillsFn is the seam tests substitute for; production points it at
// opencode. It returns the raw enumeration for a directory.
var listSkillsFn = fetchSkills

func fetchSkills(ctx context.Context, workdir string) ([]opencodeserver.Skill, error) {
	sup := Supervisor()
	if sup == nil {
		return nil, errSkillsUnavailable
	}
	// Ensure, not Client: Client only finds an instance that is already
	// running, so on a brand-new chat — exactly when someone wants to arm a
	// skill — the picker would come up empty for a workspace full of skills.
	client, dir, err := sup.Ensure(ctx, workdir, config.Workspace)
	if err != nil {
		return nil, err
	}
	return client.ListSkills(ctx, dir)
}

// errSkillsUnavailable marks "we could not ask" as distinct from "there are
// none", so the picker can say the workspace is still starting instead of
// claiming a workspace with a dozen skills has none.
var errSkillsUnavailable = errSkills("opencode server is not available")

type errSkills string

func (e errSkills) Error() string { return string(e) }

// ListSkills returns the skills opencode resolves for workdir, ready for the
// composer's picker. Skills without a description are omitted, matching
// opencode's own <available_skills> list (Skill.fmt filters them): a skill the
// model is never told about should not be armable either.
//
// The second return value reports whether the list could be obtained at all.
// false means the enumeration failed, not that the workspace has no skills.
//
// Reads go through the skill cache (skillcache.go), so an open is normally a
// map lookup rather than an opencode process start. Only a directory that has
// never been enumerated and cannot be seeded from a sibling still waits.
func ListSkills(ctx context.Context, workdir string) ([]SkillSummary, bool) {
	dir := canonicalSkillDir(workdir)

	if e, ok := lookupSkillCache(dir); ok {
		if time.Since(e.fetchedAt) >= skillCacheTTL {
			startRevalidate(dir)
		}
		return e.skills, true
	}

	if seed, ok := seedFromSibling(dir); ok {
		// Serve the borrowed answer, then replace it with this directory's own.
		// The seed is deliberately not written to the cache: it belongs to the
		// sibling, and storing it would let one borrowed answer become the
		// source for the next.
		startRevalidate(dir)
		return seed, true
	}

	ctx, cancel := context.WithTimeout(ctx, skillListTimeout)
	defer cancel()

	out, err := enumerateSkills(ctx, dir)
	if err != nil {
		log.Printf("[SKILLS] cannot list skills for %q: %v", dir, err)
		return []SkillSummary{}, false
	}
	storeSkillCache(dir, out)
	return out, true
}

// enumerateSkills asks opencode for a directory's skills and reduces them to
// the picker's shape. It is the only place that conversion happens, so the
// cache and the live path cannot disagree about what a skill looks like.
func enumerateSkills(ctx context.Context, dir string) ([]SkillSummary, error) {
	raw, err := listSkillsFn(ctx, dir)
	if err != nil {
		return nil, err
	}

	out := make([]SkillSummary, 0, len(raw))
	for _, s := range raw {
		name := strings.TrimSpace(s.Name)
		desc := strings.TrimSpace(s.Description)
		if name == "" || desc == "" {
			continue
		}
		out = append(out, SkillSummary{ID: name, Name: name, Description: desc})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// skillToolName is the tool opencode uses when the model loads a skill itself.
// A skill armed in the composer is reported under the same name so the two
// arrive at the UI as one kind of thing.
const skillToolName = "skill"

// forcedSkillEvents renders resolved forced skills as the tool_call/tool_done
// pair the model's own `skill` calls produce, so a skill the user armed shows up
// in the transcript exactly like one the agent chose for itself. Without this a
// forced skill is invisible: it is injected into the preamble, never travels
// through the tool channel, and leaves no trace of having been applied — which
// is precisely the turn where the user has most reason to expect confirmation.
//
// The pair is marked synthetic because it is not a call the model made: it costs
// no round trip, so it must not inflate the turn's tool-call count.
//
// A name with no body is a skill that vanished between arming and sending. It is
// reported as a failed call, matching the missing-skill notice the preamble
// gives the model — the one case where the user genuinely needs to know that
// what they asked for did not happen.
func forcedSkillEvents(forced []config.ForcedSkill) []*Event {
	events := make([]*Event, 0, len(forced)*2)
	for _, sk := range forced {
		name := strings.TrimSpace(sk.Name)
		if name == "" {
			continue
		}
		callID := "forced-skill-" + name
		events = append(events, &Event{
			Kind:      "tool_call",
			Tool:      skillToolName,
			CallID:    callID,
			Args:      map[string]any{"name": name, "forced": true},
			Synthetic: true,
		})
		done := &Event{
			Kind:      "tool_done",
			Tool:      skillToolName,
			CallID:    callID,
			Success:   true,
			Synthetic: true,
		}
		if strings.TrimSpace(sk.Content) == "" {
			done.Success = false
			done.Output = "This skill was requested for this message, but it no longer exists in the workspace and its instructions could not be loaded."
		}
		events = append(events, done)
	}
	return events
}

// resolveForcedSkill turns an armed skill name into the block the preamble
// injects. The body is read here, at send time, rather than carried from the
// picker: a skill can be re-materialized in between, and its location is needed
// so relative paths inside it resolve.
//
// This deliberately bypasses the skill cache. The cache holds summaries, which
// have no Content or Location, and caching full bodies per workdir would mean
// holding tens of kilobytes per skill indefinitely. Nor would it buy anything:
// the turn being sent needs an opencode instance regardless, so there is no
// process start to avoid here — and serving a skill body from a cache is
// exactly the staleness this function's own contract rules out.
//
// A name that no longer resolves still returns a ForcedSkill — one with no body
// — which the preamble renders as a notice. Losing a skill must not cost the
// user their message.
func resolveForcedSkill(ctx context.Context, workdir, name string) (config.ForcedSkill, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return config.ForcedSkill{}, false
	}

	ctx, cancel := context.WithTimeout(ctx, skillListTimeout)
	defer cancel()

	raw, err := listSkillsFn(ctx, workdir)
	if err != nil {
		log.Printf("[SKILLS] cannot resolve forced skill %q: %v", name, err)
		return config.ForcedSkill{Name: name}, true
	}
	for _, s := range raw {
		if s.Name != name {
			continue
		}
		log.Printf("[SKILLS] forcing skill %q (%d bytes) for this turn", name, len(s.Content))
		return config.ForcedSkill{Name: s.Name, Content: s.Content, Location: s.Location}, true
	}
	log.Printf("[SKILLS] forced skill %q not found in workspace %q", name, workdir)
	return config.ForcedSkill{Name: name}, true
}
