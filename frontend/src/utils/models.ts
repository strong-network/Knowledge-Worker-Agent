// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Resolve the friendly "Default" / "Thinking" presets to concrete model
// identifiers from the available model list.
//
// Default  → the latest Claude Sonnet
// Thinking → the latest Claude Opus
//
// Model ids are "provider/model" strings, e.g. "github-copilot/claude-sonnet-5"
// or "anthropic/claude-opus-5". "Latest" is the highest version number found
// in the identifier for that family.

// PREFERRED_DEFAULT_SONNETS lists the "Default" preset Sonnet pins in priority
// order; the first present in the available model list wins. Pinning avoids
// auto-selecting a newly-published, higher-versioned Sonnet before it is vetted.
// Sonnet 5 is the vetted default on both providers, so the pin is unqualified
// and matches either id spelling ("github-copilot/claude-sonnet-5",
// "google-vertex/claude-sonnet-5@default"). Sonnet 4.6 is kept behind it so a
// provider that does not offer Sonnet 5 falls back to the previously vetted
// model. Mirrors preferredDefaultSonnets in internal/config/config.go.
const PREFERRED_DEFAULT_SONNETS = ['claude-sonnet-5', 'claude-sonnet-4.6']

// PREFERRED_THINKING_OPUS lists the "Thinking" preset Opus pins in priority
// order. Opus 5 is the vetted Thinking model on both providers, so the pin is
// unqualified. The Opus 4.8 pins are kept behind it for a provider that does not
// offer Opus 5 yet (Vertex ids use dashes, "claude-opus-4-8"; Copilot uses dots,
// "claude-opus-4.8"). Mirrors preferredThinkingOpus in
// internal/config/config.go.
const PREFERRED_THINKING_OPUS = ['claude-opus-5', 'google-vertex/claude-opus-4-8', 'claude-opus-4.8']

function versionScore(id: string): number {
  // Grab the last number-with-optional-decimal in the id (e.g. 4.6, 4.8, 3.5).
  const matches = id.match(/(\d+(?:\.\d+)*)/g)
  if (!matches || matches.length === 0) return 0
  const v = matches[matches.length - 1]
  const parts = v.split('.').map(n => parseInt(n, 10) || 0)
  // Weight major/minor/patch so 4.10 > 4.9, and 4.x > 3.x.
  return (parts[0] || 0) * 10000 + (parts[1] || 0) * 100 + (parts[2] || 0)
}

// pickLatest returns the id in `models` whose lowercased name contains `family`
// with the highest version score, or '' if none match.
export function pickLatest(models: string[], family: string): string {
  const f = family.toLowerCase()
  let best = ''
  let bestScore = -1
  for (const m of models) {
    if (!m.toLowerCase().includes(f)) continue
    const score = versionScore(m)
    if (score > bestScore) {
      bestScore = score
      best = m
    }
  }
  return best
}

// isPinnedModel reports whether `id` is exactly the pinned model, ignoring case
// and any "@version" suffix (Vertex ids end in "@default"). A pin without a
// provider matches the model on any provider. Mirrors isPinnedModel in
// internal/config/config.go.
export function isPinnedModel(id: string, pin: string): boolean {
  let m = id.trim().toLowerCase()
  const at = m.indexOf('@')
  if (at >= 0) m = m.slice(0, at)
  const p = pin.trim().toLowerCase()
  if (!p.includes('/')) m = m.slice(m.lastIndexOf('/') + 1)
  return p !== '' && m === p
}

// pickPinned returns the id in `models` that is exactly `pin`, or ''. Exact, not
// a substring: "claude-opus-5" is inside "claude-opus-5.5", so a substring pin
// armed the unvetted Opus 5.5 as soon as Copilot listed it.
export function pickPinned(models: string[], pin: string): string {
  let best = ''
  let bestScore = -1
  for (const m of models) {
    if (!isPinnedModel(m, pin)) continue
    const score = versionScore(m)
    if (score > bestScore) {
      bestScore = score
      best = m
    }
  }
  return best
}

export function latestSonnet(models: string[]): string {
  // Pin the "Default" preset to a known-good Claude Sonnet when present, so a
  // newly-published higher-versioned Sonnet is not auto-selected as the default
  // before it has been vetted. The vetted default is Sonnet 5 on both providers.
  // Falls back to the latest Sonnet when no pin is present. Mirrors
  // preferredDefaultSonnets in internal/config/config.go.
  for (const pin of PREFERRED_DEFAULT_SONNETS) {
    const m = pickPinned(models, pin)
    if (m) return m
  }
  return pickLatest(models, 'sonnet')
}

export function latestOpus(models: string[]): string {
  // Pin the "Thinking" preset to Opus 5 when present, else the previously vetted
  // Opus 4.8, else the latest Opus. Mirrors preferredThinkingOpus in
  // internal/config/config.go.
  for (const pin of PREFERRED_THINKING_OPUS) {
    const m = pickPinned(models, pin)
    if (m) return m
  }
  return pickLatest(models, 'opus')
}

// shortModelName strips the "provider/" prefix from a model id for display,
// e.g. "github-copilot/claude-opus-4.8" → "claude-opus-4.8". Returns the input
// unchanged if there's no prefix.
export function shortModelName(id: string): string {
  const i = id.indexOf('/')
  return i >= 0 ? id.slice(i + 1) : id
}

// providerOf returns the provider id a model belongs to: everything before the
// FIRST slash, since ids are "<provider>/<model>" and a model name may itself
// contain slashes. Returns '' for an id with no provider prefix.
export function providerOf(id: string): string {
  const i = id.indexOf('/')
  return i > 0 ? id.slice(0, i) : ''
}

// resolvePreset maps a preset name ("default" / "thinking") to a concrete model
// id.
//
// `nominated` is what the server already resolved from the assigned provider
// artifacts: it has applied the first-wins-on-assignment-order rule and dropped
// nominations whose model was not discovered. Reimplementing those rules here
// would put two answers to "what does Default mean" in the product, and the
// visible symptom of them disagreeing is a composer chip labelled "Default"
// that arms a different model than the server would have picked.
//
// The Claude heuristic below is NOT redundant with that: it is what runs in
// built-in mode, where no artifact nominates anything.
export function resolvePreset(
  preset: 'default' | 'thinking',
  models: string[],
  nominated?: Record<string, string>,
): string {
  const pick = nominated?.[preset]
  // Still check availability: the nomination is resolved against the server's
  // model list, and the browser's copy can lag it by a poll interval.
  if (pick && models.includes(pick)) return pick
  return preset === 'thinking' ? latestOpus(models) : latestSonnet(models)
}
