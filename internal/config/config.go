// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

var (
	OpencodeBin string
	Host        string
	Port        int
	Workspace   string
	DBPath      string
	// OpencodeAgentsDir is OpenCode's global agent directory
	// (<XDG_CONFIG_HOME|~/.config>/opencode/agent). OpenCode resolves
	// --agent by the file stem of the *.md files here. It is the baseline
	// tier ListAgents reads; agents materialized into the platform-owned dir
	// are layered above it — see agentDirs.
	OpencodeAgentsDir string
	OwnerFullName     string
	OwnerEmail        string
	// DefaultBackend is the backend assigned to new sessions. OpenCode is
	// the only supported backend, so this is always BackendOpencode; it is
	// retained for the /api/me response and the SessionConfig field.
	DefaultBackend string
)

// defaultModel is the model used when a session config carries no explicit
// model. It is populated from opencode's discovered github-copilot model list
// (see SetDefaultModel) after login/model discovery. A session with an empty
// Model must still be launched with an explicit `--model`, because opencode has
// no built-in default provider on a fresh machine — omitting `--model` there
// leaves the run with no provider selected ("no providers"), which is exactly
// the fresh-machine failure this guards against.
var (
	defaultModelMu sync.RWMutex
	defaultModel   string
)

// SetDefaultModel picks a sensible default model from a discovered model list
// (as returned by `opencode models github-copilot`) and stores it. It mirrors
// the frontend's "Default" preset (frontend/src/utils/models.ts): the default
// is the latest Claude Sonnet. Failing that it falls back to the latest Claude
// Opus (the frontend's "Thinking" preset), then any github-copilot/* model,
// then the first entry. An empty/all-blank list clears the default. Safe for
// concurrent use.
func SetDefaultModel(models []string) {
	SetDefaultModelForProvider(models, "")
}

// SetDefaultModelForProvider is like SetDefaultModel but biases the choice
// toward preferredProvider — the provider the user is actually signed in to
// (e.g. "google-vertex" when Vertex ADC is present, "github-copilot" when a
// Copilot OAuth token is stored). When preferredProvider is non-empty and that
// provider contributed usable models, the default is chosen only from those
// models, so a user signed in to Vertex gets a Vertex default (Sonnet 5) and a
// user signed in to Copilot gets a Copilot default (Sonnet 4.6). This matters
// because a merely *configured* provider (e.g. Vertex env vars set but no gcloud
// login) still appears in the discovered model list; gating on the signed-in
// provider avoids defaulting to a provider the user cannot yet use. When
// preferredProvider is empty or contributed no models, it falls back to the full
// list. Safe for concurrent use.
func SetDefaultModelForProvider(models []string, preferredProvider string) {
	picked := pickDefaultModelForProvider(models, preferredProvider)
	defaultModelMu.Lock()
	defaultModel = picked
	defaultModelMu.Unlock()
}

// pickDefaultModelForProvider chooses the default, preferring models whose id is
// prefixed with preferredProvider. If that provider contributed no models (or
// preferredProvider is blank) it falls back to picking from the full list.
func pickDefaultModelForProvider(models []string, preferredProvider string) string {
	if strings.TrimSpace(preferredProvider) != "" {
		if scoped := modelsForProvider(models, preferredProvider); len(scoped) > 0 {
			if m := pickDefaultModel(scoped); m != "" {
				return m
			}
		}
	}
	return pickDefaultModel(models)
}

// modelsForProvider returns the (trimmed) entries in models whose id begins with
// "<provider>/", e.g. provider "google-vertex" selects
// "google-vertex/claude-sonnet-5@default". Comparison is case-insensitive.
func modelsForProvider(models []string, provider string) []string {
	prefix := strings.ToLower(strings.TrimSpace(provider)) + "/"
	out := make([]string, 0, len(models))
	for _, m := range models {
		m = strings.TrimSpace(m)
		if strings.HasPrefix(strings.ToLower(m), prefix) {
			out = append(out, m)
		}
	}
	return out
}

// DefaultModel returns the currently stored default model (possibly empty).
func DefaultModel() string {
	defaultModelMu.RLock()
	defer defaultModelMu.RUnlock()
	return defaultModel
}

// availableModels is the set of models the user can actually run right now —
// the auth-gated discovery result (see SetAvailableModels). It backs
// ResolveModel's staleness check so a session pinned to a model from a provider
// the user is not signed in to doesn't keep trying to run it.
var (
	availableModelsMu sync.RWMutex
	availableModels   []string
)

// SetAvailableModels records the currently runnable model list (the same
// auth-gated list served by /api/models). An empty list means "unknown" —
// ResolveModel then trusts whatever a session has pinned rather than
// second-guessing it. Safe for concurrent use.
func SetAvailableModels(models []string) {
	cleaned := make([]string, 0, len(models))
	for _, m := range models {
		if m = strings.TrimSpace(m); m != "" {
			cleaned = append(cleaned, m)
		}
	}
	availableModelsMu.Lock()
	availableModels = cleaned
	availableModelsMu.Unlock()
}

// resolveAvailable reports the runnable identifier for a pinned model:
//   - the model itself when it is in the available list (case-insensitive);
//   - the provider-qualified equivalent when a bare id was pinned (legacy
//     sessions stored e.g. "claude-opus-4.8" before ids carried a provider),
//     so those sessions upgrade to the right provider instead of breaking;
//   - "" when the model is not runnable at all (e.g. a google-vertex model
//     pinned while the user is only signed in to GitHub Copilot).
//
// When the available list is unknown (empty) every model is accepted, so a
// failed or not-yet-run discovery never invalidates a working selection.
func resolveAvailable(model string) string {
	availableModelsMu.RLock()
	defer availableModelsMu.RUnlock()
	if len(availableModels) == 0 {
		return model
	}
	for _, a := range availableModels {
		if strings.EqualFold(a, model) {
			return a
		}
	}
	if !strings.Contains(model, "/") {
		suffix := "/" + strings.ToLower(model)
		for _, a := range availableModels {
			if strings.HasSuffix(strings.ToLower(a), suffix) {
				return a
			}
		}
	}
	return ""
}

// ModelRunnable reports whether a pinned model can actually be run right now,
// using the same matching ResolveModel uses.
//
// This exists for callers that must NOT take ResolveModel's fallback. An
// unattended run has nobody to notice that a substitute was chosen, so a
// scheduled task pinned to a model whose provider is gone has to fail and say
// so rather than quietly producing output from a different model.
//
// Follows resolveAvailable's convention: while the available list is unknown
// (discovery has not run, or failed) every model is reported runnable, so a
// transient discovery failure never cancels a task that would have worked.
func ModelRunnable(model string) bool {
	m := strings.TrimSpace(model)
	if m == "" {
		// No pin: the run uses the server default, which is by construction
		// drawn from the discovered list.
		return true
	}
	return resolveAvailable(m) != ""
}

// ResolveModel returns the model a run should use: the session's explicit model
// when it is still runnable, otherwise the server-side default.
//
// A session persists whichever model the user pinned via the composer's
// Default/Thinking presets or the model picker, and that pin outlives the
// conditions it was made under — sign in to a different provider, or restore a
// session from durable storage, and the pin can name a model the user can no
// longer run. Without this fallback such a session keeps sending an unusable
// model (and fails at submit time) until the user manually starts a new chat.
// The pin is not rewritten, so it starts working again if that provider is
// signed in later.
//
// The result may still be empty if no default has been discovered yet.
func ResolveModel(model string) string {
	m := strings.TrimSpace(model)
	if m == "" {
		return DefaultModel()
	}
	if runnable := resolveAvailable(m); runnable != "" {
		return runnable
	}
	// The pinned model isn't runnable (e.g. its provider isn't authenticated).
	// Fall back to the auth-aware default rather than failing the turn.
	if d := DefaultModel(); d != "" {
		return d
	}
	return m
}

// pickDefaultModel chooses the default model from a discovered list.
//
// A provider artifact's nomination wins when it names a model that is actually
// present. Otherwise this mirrors the frontend "Default" preset: latest
// Claude Sonnet, else latest Claude Opus, else any github-copilot/* model, else
// the first non-blank entry — which is what runs for the built-in providers.
func pickDefaultModel(models []string) string {
	cleaned := make([]string, 0, len(models))
	for _, m := range models {
		if m = strings.TrimSpace(m); m != "" {
			cleaned = append(cleaned, m)
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	// 0) An admin-nominated Default, when the workspace has one. Checked
	// against the discovered list so a nomination naming a model that is not
	// there falls through to the heuristic rather than pinning a dead id.
	if nominated := PresetModel(PresetDefault); nominated != "" {
		for _, m := range cleaned {
			if strings.EqualFold(m, nominated) {
				return m
			}
		}
	}
	// 1) Default preset: a pinned, vetted Claude Sonnet. The pins are tried in
	// order so a newly-published, higher-versioned Sonnet is not auto-selected
	// before it has been vetted. The vetted default is Sonnet 5 on both
	// providers; the pin is unqualified so it matches either spelling
	// ("github-copilot/claude-sonnet-5", "google-vertex/claude-sonnet-5@default").
	// Mirrors latestSonnet() in frontend/src/utils/models.ts.
	for _, pin := range preferredDefaultSonnets {
		if m := preferredModel(cleaned, pin); m != "" {
			return m
		}
	}
	// 1) Default preset fallback: latest Claude Sonnet.
	if m := latestModel(cleaned, "sonnet"); m != "" {
		return m
	}
	// 2) Thinking preset: pinned Opus 5 when present, else the previously
	// vetted Opus 4.8, else the latest Claude Opus.
	for _, pin := range preferredThinkingOpus {
		if m := preferredModel(cleaned, pin); m != "" {
			return m
		}
	}
	if m := latestModel(cleaned, "opus"); m != "" {
		return m
	}
	// 3) any github-copilot/* model.
	for _, m := range cleaned {
		if strings.HasPrefix(strings.ToLower(m), "github-copilot/") {
			return m
		}
	}
	// 4) fall back to the first available identifier.
	return cleaned[0]
}

// preferredDefaultSonnets lists the "Default" preset Sonnet pins in priority
// order; the first that matches a discovered model wins. Pinning avoids
// auto-selecting a newly-published, higher-versioned Sonnet before it has been
// vetted. Sonnet 5 is the vetted default on both providers, so the pin is
// unqualified and matches either id spelling. Sonnet 4.6 is kept behind it so a
// provider that does not offer Sonnet 5 falls back to the previously vetted
// model rather than to whatever is newest. When no pin matches, resolution
// falls back to the latest-Sonnet logic. Mirrors PREFERRED_DEFAULT_SONNETS in
// frontend/src/utils/models.ts.
var preferredDefaultSonnets = []string{"claude-sonnet-5", "claude-sonnet-4.6"}

// preferredThinkingOpus lists the "Thinking" preset Opus pins in priority order.
// Opus 5 is the vetted Thinking model on both providers, so the pin is
// unqualified and matches either id spelling. The Opus 4.8 pins are kept behind
// it for a provider that does not offer Opus 5 yet (Vertex ids use dashes,
// "claude-opus-4-8"; Copilot uses dots, "claude-opus-4.8"), so a newer, unvetted
// Opus is still not auto-selected. When no pin matches, resolution falls back to
// the latest-Opus logic. Mirrors PREFERRED_THINKING_OPUS in
// frontend/src/utils/models.ts.
var preferredThinkingOpus = []string{"claude-opus-5", "google-vertex/claude-opus-4-8", "claude-opus-4.8"}

// SmallModel returns the model that background, server-initiated generations
// should run on, or "" when the workspace offers nothing suitable.
//
// This is deliberately NOT ResolveModel's chain. ResolveModel ends at the
// workspace default, which is a Sonnet-class model by construction — fine for a
// turn the user asked for, wrong for a sweep they did not. Day notes generate
// in bulk in the background (a first pass on a real workspace has ~173 days of
// backlog), so "fall back to the default" means an unrequested job quietly
// billing frontier-model rates over days. Returning "" instead lets the caller
// decline to run, which is the only bound that actually holds.
//
// Order:
//  1. The workspace's "small" nomination, when it is runnable. An admin
//     who names an expensive model here has chosen that explicitly.
//  2. The latest model in the first cheap tier that the workspace actually
//     offers, tried vendor by vendor (see smallModelFamilies). Built-in
//     providers ship no nominations at all, so without this notes would never
//     run outside central-config workspaces — and a cheap model is right there.
//  3. "" — nothing suitable. The caller skips and says so.
func SmallModel() string {
	if small := PresetModel(PresetSmall); small != "" {
		if runnable := resolveAvailable(small); runnable != "" {
			return runnable
		}
	}
	availableModelsMu.RLock()
	defer availableModelsMu.RUnlock()
	for _, family := range smallModelFamilies {
		if m := latestInFamily(availableModels, family); m != "" {
			return m
		}
	}
	return ""
}

// smallModelFamilies names the cheap tier of each vendor, in the order they are
// tried when no provider has nominated a `small` preset.
//
// This was the single string "haiku", which holds only while every provider is
// a Claude vendor. Knowledge Worker Agent offers several, so on a workspace backed by
// Gemini, GPT or Mistral nothing matched, SmallModel returned "", and every
// caller quietly declined to run. It also aged in one specific direction: the
// day a provider retires Haiku 4.5, a workspace that was working stops working,
// with no configuration change to point at.
//
// A nomination still wins; this is only the fallback for workspaces that make
// none, which today is every built-in-mode (non-central) one.
//
// Order is "most confidently cheap first" rather than strictly cheapest —
// several of these tiers are within noise of each other and their relative
// price changes without notice. Haiku leads so a workspace resolving one today
// keeps resolving exactly what it resolves now; the rest follow each vendor's
// own name for its smallest tier. Adding a vendor means adding its word here.
var smallModelFamilies = []string{
	"haiku", // Anthropic
	"nano",  // OpenAI
	"mini",  // OpenAI
	"lite",  // Google, e.g. gemini-*-flash-lite
	"flash", // Google
	"small", // Mistral
	"tiny",  // Mistral
}

// modelWordRe splits a model id into the words vendors separate with
// punctuation: "google-vertex/gemini-3.1-flash-lite" becomes gemini, 3, 1,
// flash, lite.
var modelWordRe = regexp.MustCompile(`[a-z0-9]+`)

// namesFamily reports whether id names family as a whole word.
//
// Whole word, not substring, and the difference is the reason this function
// exists: "gemini" contains "mini". Under a substring test every Gemini model
// joins the "mini" tier — including gemini-3.1-pro, which versionScore would
// then rank at the top — so the guard against handing a background sweep an
// expensive model would itself select one. Matching words is what keeps it
// honest.
func namesFamily(id, family string) bool {
	for _, word := range modelWordRe.FindAllString(strings.ToLower(id), -1) {
		if word == family {
			return true
		}
	}
	return false
}

// latestInFamily returns the highest-versioned model naming family as a whole
// word, or "" when none do.
//
// latestModel is deliberately left on substring matching: it is called with
// "sonnet" and "opus", neither of which hides inside another vendor's name, and
// its callers are additionally pinned to vetted ids. Narrowing it belongs to a
// change that can be verified against those paths, not to this one.
func latestInFamily(models []string, family string) string {
	f := strings.ToLower(family)
	best := ""
	bestScore := -1
	for _, m := range models {
		if !namesFamily(m, f) {
			continue
		}
		if s := versionScore(m); s > bestScore {
			bestScore = s
			best = m
		}
	}
	return best
}

// preferredModel returns the identifier in models that is exactly the pinned
// model, or "" if none is. Among several (the same model on two providers) it
// returns the one with the highest version score for stability. Used to pin a
// preset to a specific vetted model.
//
// Exact, not a substring: "claude-opus-5" is inside "claude-opus-5.5", so a
// substring pin armed the unvetted Opus 5.5 as soon as Copilot listed it.
func preferredModel(models []string, pin string) string {
	best := ""
	bestScore := -1
	for _, m := range models {
		if !isPinnedModel(m, pin) {
			continue
		}
		if sc := versionScore(m); sc > bestScore {
			bestScore = sc
			best = m
		}
	}
	return best
}

// isPinnedModel reports whether id names the pinned model. The comparison
// ignores case and any "@version" suffix (Vertex ids end in "@default"); a pin
// without a provider matches the model on any provider.
func isPinnedModel(id, pin string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	if at := strings.IndexByte(id, '@'); at >= 0 {
		id = id[:at]
	}
	pin = strings.ToLower(strings.TrimSpace(pin))
	if !strings.Contains(pin, "/") {
		id = id[strings.LastIndexByte(id, '/')+1:]
	}
	return pin != "" && id == pin
}

// latestModel returns the identifier in models whose lowercased name contains
// family with the highest version score, or "" if none match. This mirrors
// pickLatest() in frontend/src/utils/models.ts so the backend default and the
// frontend "Default"/"Thinking" presets resolve to the same models.
func latestModel(models []string, family string) string {
	f := strings.ToLower(family)
	best := ""
	bestScore := -1
	for _, m := range models {
		if !strings.Contains(strings.ToLower(m), f) {
			continue
		}
		if s := versionScore(m); s > bestScore {
			bestScore = s
			best = m
		}
	}
	return best
}

// versionScore extracts the last number-with-optional-decimal in id and weights
// it so 4.10 > 4.9 and 4.x > 3.x. Mirrors versionScore() in
// frontend/src/utils/models.ts.
func versionScore(id string) int {
	matches := versionNumRe.FindAllString(id, -1)
	if len(matches) == 0 {
		return 0
	}
	last := matches[len(matches)-1]
	parts := strings.Split(last, ".")
	get := func(i int) int {
		if i >= len(parts) {
			return 0
		}
		n, _ := strconv.Atoi(parts[i])
		return n
	}
	return get(0)*10000 + get(1)*100 + get(2)
}

// versionNumRe matches a run of digits with optional dotted decimals (e.g.
// "4", "4.6", "4.10.2").
var versionNumRe = regexp.MustCompile(`\d+(?:\.\d+)*`)

// Backend identifiers. OpenCode is the only supported backend; the constant
// and the SessionConfig.Backend field are retained so persisted session rows
// (and the frontend contract) stay stable.
const (
	BackendOpencode = "opencode"
	// BackendCopilot is the removed GitHub Copilot backend. It is no longer a
	// runnable backend; the identifier is kept only to detect legacy,
	// now-deprecated session rows that were persisted with it.
	BackendCopilot = "copilot"
)

// NormalizeBackend returns the canonical backend id. OpenCode is the only
// backend, so any value (including empty/legacy "copilot" rows) normalizes to
// BackendOpencode.
func NormalizeBackend(s string) string {
	return BackendOpencode
}

// IsLegacyCopilotBackend reports whether a RAW (un-normalized) persisted
// backend value refers to the removed GitHub Copilot backend. Callers must
// pass the value straight from storage, before NormalizeBackend runs.
func IsLegacyCopilotBackend(raw string) bool {
	return strings.EqualFold(strings.TrimSpace(raw), BackendCopilot)
}

func EnvOrDefault(key, fallback string) string {
	if v := env.Get(key); v != "" {
		return v
	}
	return fallback
}

func Init() {
	OpencodeBin = ResolveOpencodeBin(env.Get("KWA_OPENCODE_BIN"))
	// Loopback by default: the API has no authentication. The images set 0.0.0.0.
	Host = EnvOrDefault("KWA_HOST", "127.0.0.1")
	Port, _ = strconv.Atoi(EnvOrDefault("KWA_PORT", "8765"))
	if Port == 0 {
		Port = 8765
	}
	Workspace = layout.Workspace()
	os.MkdirAll(Workspace, 0o755)
	Workspace, _ = filepath.Abs(Workspace)

	home, _ := os.UserHomeDir()
	DBPath = layout.Database()
	if DBPath != "" {
		if abs, err := filepath.Abs(DBPath); err == nil {
			DBPath = abs
		}
		_ = os.MkdirAll(filepath.Dir(DBPath), 0o755)
	}

	// OpenCode's global agent dir. Mirrors OpenCode's own config-dir
	// resolution: $XDG_CONFIG_HOME/opencode when set, else ~/.config/opencode.
	opencodeCfg := filepath.Join(home, ".config", "opencode")
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		opencodeCfg = filepath.Join(xdg, "opencode")
	}
	OpencodeAgentsDir = filepath.Join(opencodeCfg, "agent")

	OwnerFullName = firstNonEmptyEnv("OWNER_FULL_NAME", "owner_full_name")
	OwnerEmail = firstNonEmptyEnv("OWNER_EMAIL", "owner_email")

	// Workspace hygiene windows.
	initHygiene()

	// OpenCode is the only supported backend.
	DefaultBackend = BackendOpencode
}

// OpencodeFallbackPaths lists locations to probe when the user-supplied
// `opencode` binary cannot be resolved. opencode's official installers drop it
// into ~/.opencode/bin (curl installer) or ~/.local/bin (npm/pnpm); we look
// in both plus the workspace-local ./node_modules fallback.
var OpencodeFallbackPaths = []string{
	"/home/developer/.local/bin/opencode",
	"/home/developer/.opencode/bin/opencode",
	"/usr/local/bin/opencode",
}

// ResolveOpencodeBin returns the path to the opencode binary, probing the
// caller-supplied value (env var) first and then known install locations if it
// isn't found on PATH. If it can't be resolved, the returned value is still
// "opencode" (or whatever the caller supplied) so that downstream "command not
// found" errors are still informative.
func ResolveOpencodeBin(supplied string) string {
	candidates := []string{}
	if supplied != "" {
		candidates = append(candidates, supplied)
	}
	candidates = append(candidates, "opencode")
	candidates = append(candidates, OpencodeFallbackPaths...)
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".local", "bin", "opencode"),
			filepath.Join(home, ".opencode", "bin", "opencode"),
		)
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if strings.ContainsRune(c, os.PathSeparator) {
			if info, err := os.Stat(c); err == nil && !info.IsDir() {
				return c
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	if supplied != "" {
		return supplied
	}
	return "opencode"
}

// SystemContext returns a system context string identifying the user, or empty if not configured.
func SystemContext() string {
	var parts []string
	if OwnerFullName != "" {
		parts = append(parts, "Name: "+OwnerFullName)
	}
	if OwnerEmail != "" {
		parts = append(parts, "Email: "+OwnerEmail)
	}
	if len(parts) == 0 {
		return ""
	}
	return "[System context — current user: " + strings.Join(parts, ", ") + "]\n\n"
}

// WorkspaceInstruction returns a short, agent-facing instruction telling the
// model to keep any files it creates or modifies inside the supplied
// workspace directory. Returns an empty string if workdir is not set.
//
// The text is intentionally brief and idempotent so it can be prepended to
// every user message without bloating the context window. It also nudges the
// model to use paths relative to the workspace, which matches how the file
// browser / editor in this UI present files to the user.
func WorkspaceInstruction(workdir string) string {
	workdir = strings.TrimSpace(workdir)
	if workdir == "" {
		return ""
	}
	moved := ""
	if old, ok := layout.OldPath(workdir); ok {
		moved = "This workspace was at " + old + " until Knowledge Worker Agent moved it. " +
			"A path under " + old + " earlier in this conversation is now under " + workdir + ".\n"
	}
	return "[System — project workspace]\n" +
		"This conversation has a project workspace at: " + workdir + "\n" + moved +
		"When you create, modify, or save files, place them inside that workspace directory unless the user explicitly asks for a different location. " +
		"Prefer paths relative to that directory when referring to project files, and do not write outside it without explicit user instruction.\n\n"
}

// FolderConventionInstruction returns the agent-facing instruction for the
// durable-store folder convention. The convention is agent-instructed,
// not hard-enforced: the share root holds finished deliverables, inputs/ holds
// user-provided source material, working/ is discardable scratch, and .system/
// is a hidden platform-managed area the agent must not write to. Returns "" when
// workdir is not set (nothing to anchor the convention to).
func FolderConventionInstruction(workdir string) string {
	if strings.TrimSpace(workdir) == "" {
		return ""
	}
	return "[System — workspace layout]\n" +
		"Organize files in this workspace by convention:\n" +
		"- Write finished deliverables to the workspace root (you may create subfolders there to organize them).\n" +
		"- Read user-provided source material (uploads, reference docs) from the `inputs/` folder.\n" +
		"- Put scratch and intermediate files in the `working/` folder; treat it as safe to discard.\n" +
		"- Do not write to the hidden `.system/` folder; it is managed by the platform.\n\n"
}

// PlanningInstruction returns the agent-facing rule for keeping the "Plan &
// progress" task list current. The long-form version of this rule already lives
// in the global instructions file, but that is delivered once per session and
// competes with ~50k tokens of other context, so in practice the planning step
// was frequently skipped. This short restatement is re-sent with every turn,
// which is the point: it is the only planning text the model sees on a resumed
// session with a long history.
//
// Keep it terse. It is a reminder of a rule stated in full elsewhere, not the
// statement of the rule, and it pays its context cost on every single turn.
func PlanningInstruction() string {
	return "[System — planning]\n" +
		"If a request needs three or more distinct steps, or more than a couple of tool calls, " +
		"call the `todowrite` tool with the full plan and the first item marked `in_progress` " +
		"BEFORE any research, delegation, or file changes — not after. " +
		"Then call `todowrite` again at every boundary: when you finish an item, mark it `completed` " +
		"and the next item `in_progress` in that same call. " +
		"Keep exactly one item `in_progress`, and never batch the updates until the end. " +
		"Skip the list entirely for single-step, conversational, or purely informational requests.\n\n"
}

// PromptWithSystemContext injects user identity metadata ahead of a prompt.
func PromptWithSystemContext(prompt string) string {
	if ctx := SystemContext(); ctx != "" {
		return ctx + prompt
	}
	return prompt
}

// ProjectInstructionsBlock returns the project's standing custom instructions
// formatted for injection into the system preamble. These supplement —
// never replace — the global instructions. Returns "" when there are none.
func ProjectInstructionsBlock(instructions string) string {
	instructions = strings.TrimSpace(instructions)
	if instructions == "" {
		return ""
	}
	return "[System — project instructions]\n" +
		"This chat belongs to a project with the following standing instructions. " +
		"Follow them in addition to (not instead of) any global guidance:\n" +
		instructions + "\n\n"
}

// PromptWithSystemPreamble injects the workdir-derived system preamble blocks
// (user identity, project workspace, workspace layout) ahead of a user prompt.
// Blocks that have no applicable data are omitted, so this is safe to call even
// when no owner or workdir is configured.
//
// Chat turns go through PromptWithSessionPreamble instead, which additionally
// carries the planning rule and any project instructions.
func PromptWithSystemPreamble(prompt, workdir string) string {
	preamble := SystemContext() + WorkspaceInstruction(workdir) + FolderConventionInstruction(workdir)
	if preamble == "" {
		return prompt
	}
	return preamble + prompt
}

// ForcedSkill is a skill the user armed in the composer for a single turn.
// It is deliberately *not* part of SessionConfig: that struct is
// persisted to SQLite, and a per-turn value living in persisted state would
// keep forcing itself on every later turn if the composer's reset never ran.
// Carrying it alongside the prompt makes "resets after send" a property of the
// request rather than a courtesy of the client.
//
// Name is the skill's identity, Content its SKILL.md body and Location the
// absolute path of that file — all three come straight from opencode's own
// skill enumeration, resolved at send time.
type ForcedSkill struct {
	Name     string
	Content  string
	Location string
}

// ForcedSkillInstruction renders an armed skill as a preamble block. The
// <skill_content> envelope mirrors what opencode's own `skill` tool emits when
// the model loads a skill itself (tool/skill.ts), so the model sees a familiar
// shape; the difference is that it is already present instead of depending on
// the model noticing it should go and fetch it.
//
// The directive is worded to sit *after* the planning rule rather than override
// it: plan first, then work through the skill.
//
// A named skill with no body means it could not be resolved at send time; that
// renders the missing-skill notice instead, so a skill that vanished between
// arming and sending degrades to an explanation rather than silence.
func ForcedSkillInstruction(skill ForcedSkill) string {
	name := strings.TrimSpace(skill.Name)
	if name == "" {
		return ""
	}
	body := strings.TrimSpace(skill.Content)
	if body == "" {
		return MissingSkillNotice(name)
	}
	b := "[System — forced skill]\n" +
		"The user selected the `" + name + "` skill for this message. " +
		"Its full instructions are included below, so you do not need to load it yourself. " +
		"Follow its workflow for this turn — this is not optional, and it applies even if you " +
		"would not have chosen the skill on your own. Plan the work first if the planning rule " +
		"above applies, then carry out the plan the way this skill prescribes.\n\n" +
		"<skill_content name=\"" + name + "\">\n" +
		"# Skill: " + name + "\n\n" +
		body + "\n"
	if dir := strings.TrimSpace(filepath.Dir(skill.Location)); dir != "" && dir != "." {
		b += "\nBase directory for this skill: " + dir + "\n" +
			"Relative paths in this skill (e.g., scripts/, reference/) are relative to this base directory.\n"
	}
	b += "</skill_content>\n\n"
	return b
}

// MissingSkillNotice is used when a skill was armed but no longer exists by the
// time the turn is dispatched. A missing skill must never cost the user their
// message, so the prompt still goes out — with the model told to say what
// happened rather than silently answering as if nothing had been asked for.
func MissingSkillNotice(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return "[System — note]\n" +
		"The user asked to use the `" + name + "` skill for this message, but it could not be " +
		"found in this workspace. Begin your reply by telling them so in one short sentence, " +
		"then answer as best you can without it.\n\n"
}

// PromptWithSessionPreamble is like PromptWithSystemPreamble but also layers in
// the planning rule and project-level standing instructions from the session
// config. Used by the stream runners, so this is the preamble that
// actually reaches the model on every chat turn.
//
// Block order is deliberate: global system rules first, then the project's own
// instructions, then any skill the user forced for this one turn — so the most
// specific guidance sits closest to the user's prompt. Unlike the other blocks,
// the planning rule is unconditional — it does not depend on a workdir or an
// owner being configured.
//
// forced is variadic only so the many callers that never force a skill keep
// their existing call shape; at most one skill is ever armed.
func PromptWithSessionPreamble(prompt string, cfg SessionConfig, forced ...ForcedSkill) string {
	return PromptWithSessionPreambleAs(prompt, cfg, "", forced...)
}

// PromptWithSessionPreambleAs is PromptWithSessionPreamble for a prompt written
// by guestName; "" means the owner wrote it. A guest's prompt must
// not carry the owner's identity line, or the model answers them as the owner.
func PromptWithSessionPreambleAs(prompt string, cfg SessionConfig, guestName string, forced ...ForcedSkill) string {
	identity := SystemContext()
	if guestName != "" {
		identity = GuestContext(guestName)
	}
	preamble := identity + WorkspaceInstruction(cfg.Workdir) + FolderConventionInstruction(cfg.Workdir) + PlanningInstruction() + ProjectInstructionsBlock(cfg.ProjectInstructions)
	for _, skill := range forced {
		preamble += ForcedSkillInstruction(skill)
	}
	if preamble == "" {
		return prompt
	}
	return preamble + prompt
}

// GuestContext is the identity line for a prompt a guest wrote. The name is
// self-declared, so it labels rather than authenticates, and it is untrusted
// text inside the system context: anything that could end the line is removed.
func GuestContext(guestName string) string {
	name := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '[' || r == ']' {
			return -1
		}
		return r
	}, guestName)
	name = strings.TrimSpace(name)
	if name == "" {
		name = "a guest"
	}
	owner := "the workspace owner"
	if OwnerFullName != "" {
		owner = "the workspace owner, " + OwnerFullName + ","
	}
	return "[System context — this message was written by " + name + ", a guest " + owner +
		" shared this chat with. The workspace, its files, credentials and connectors belong to the owner, not the guest. " +
		"Address the guest by that name; do not treat them as the owner.]\n\n"
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

type SessionConfig struct {
	Label string `json:"label"`
	// LabelManual records that Label was chosen deliberately — by the user
	// renaming the session, or by a feature that names its own sessions —
	// rather than derived from the first prompt. Automatic titling never
	// overwrites a manual label. The zero value (false) is the safe default:
	// sessions created before this field existed are treated as auto-named.
	LabelManual              bool     `json:"label_manual"`
	SessionName              string   `json:"session_name"`
	Backend                  string   `json:"backend"`
	Workdir                  string   `json:"workdir"`
	AddDirs                  []string `json:"add_dirs"`
	Mode                     string   `json:"mode"`
	Yolo                     bool     `json:"yolo"`
	AllowAllTools            bool     `json:"allow_all_tools"`
	AllowAllPaths            bool     `json:"allow_all_paths"`
	AllowAllURLs             bool     `json:"allow_all_urls"`
	DisallowTempDir          bool     `json:"disallow_temp_dir"`
	AllowTool                []string `json:"allow_tool"`
	DenyTool                 []string `json:"deny_tool"`
	AllowURL                 []string `json:"allow_url"`
	DenyURL                  []string `json:"deny_url"`
	Model                    string   `json:"model"`
	ReasoningEffort          string   `json:"reasoning_effort"`
	GithubMcpAll             bool     `json:"github_mcp_all"`
	AddGithubMcpTools        []string `json:"add_github_mcp_tools"`
	AddGithubMcpToolsets     []string `json:"add_github_mcp_toolsets"`
	MaxContinues             int      `json:"max_continues"`
	NoAskUser                bool     `json:"no_ask_user"`
	Agent                    string   `json:"agent"`
	Experimental             bool     `json:"experimental"`
	NoCustomInstructions     bool     `json:"no_custom_instructions"`
	McpConfig                string   `json:"mcp_config"`
	AvailableTools           []string `json:"available_tools"`
	ExcludedTools            []string `json:"excluded_tools"`
	DisableBuiltinMcps       bool     `json:"disable_builtin_mcps"`
	DisableMcpServer         []string `json:"disable_mcp_server"`
	EnableReasoningSummaries bool     `json:"enable_reasoning_summaries"`
	SecretEnvVars            []string `json:"secret_env_vars"`
	PluginDir                []string `json:"plugin_dir"`
	Group                    string   `json:"group"`
	Pinned                   bool     `json:"pinned"`
	Favorite                 bool     `json:"favorite"`
	// ProjectInstructions holds the owning project's standing custom
	// instructions. They layer on top of the global system preamble
	// for every chat in the project. Empty for loose (non-project) chats.
	ProjectInstructions string `json:"project_instructions"`
	// Temporary marks a chat as throwaway: it is never backed up to a
	// durable store and is purged after a short inactivity window. This is a
	// creation-time-only decision made in the composer — a durable store is
	// never provisioned for a Temporary chat, so it cannot be toggled on later
	// (there would be nothing to remove). Chat-only; projects are durable by
	// definition and cannot be Temporary.
	Temporary bool `json:"temporary"`
	// Keep marks a chat as exempt from inactivity cleanup, retained
	// indefinitely. Mutually exclusive with Temporary. For a chat that belongs
	// to a project, Keep is applied at the project level (the shared project
	// workspace is the durable unit), not stored here.
	Keep bool `json:"keep"`
	// McpSelection is the per-chat connector selection: a map of
	// MCP server name → whether it should be connected for this chat. A nil map
	// means "not yet seeded" and callers treat it as "all globally-enabled
	// servers on". An explicit entry overrides the global default for that
	// server in this chat only; the global opencode.json is never mutated.
	McpSelection map[string]bool `json:"mcp_selection,omitempty"`
}

func DefaultSessionConfig() SessionConfig {
	return SessionConfig{
		Label:                "New chat",
		Backend:              NormalizeBackend(DefaultBackend),
		Workdir:              Workspace,
		Mode:                 "autopilot",
		Yolo:                 true,
		AddDirs:              []string{},
		AllowTool:            []string{},
		DenyTool:             []string{},
		AllowURL:             []string{},
		DenyURL:              []string{},
		AddGithubMcpTools:    []string{},
		AddGithubMcpToolsets: []string{},
		AvailableTools:       []string{},
		ExcludedTools:        []string{},
		DisableMcpServer:     []string{},
		SecretEnvVars:        []string{},
		PluginDir:            []string{},
	}
}

// BuildOpencodeArgs assembles the argv vector for `opencode run`. opencode's
// flag surface is much smaller than copilot's so most SessionConfig fields
// are intentionally ignored.
//
// Reference: https://opencode.ai/docs/cli (the `run` subcommand). Streaming
// JSON events are emitted to stdout when --format json is set; an "idle"
// session status terminates the run.
//
// Notes:
//   - opencode uses provider/model identifiers (e.g. "anthropic/claude-sonnet-4")
//     and rejects bare model names. We forward cfg.Model verbatim — the caller
//     is responsible for picking a valid one.
//   - --dangerously-skip-permissions is the rough equivalent of copilot --yolo
//     and is gated by cfg.Yolo for parity with the copilot path.
//   - opencodeSessionID resumes an existing opencode session; an empty value
//     creates a fresh one.
func BuildOpencodeArgs(prompt string, opencodeSessionID string, cfg SessionConfig) []string {
	args := []string{"run", "--format", "json"}

	if cfg.Yolo {
		args = append(args, "--dangerously-skip-permissions")
	}
	if m := ResolveModel(cfg.Model); m != "" {
		args = append(args, "--model", m)
	}
	// Agent selection. An explicit agent always wins; otherwise "Plan" mode
	// maps to opencode's built-in read-only `plan` agent (it drafts a plan and
	// won't modify files). opencode has no `--mode` flag (that's copilot), so
	// mode is otherwise ignored here.
	if agent := strings.TrimSpace(cfg.Agent); agent != "" {
		args = append(args, "--agent", agent)
	} else if strings.EqualFold(strings.TrimSpace(cfg.Mode), "plan") {
		args = append(args, "--agent", "plan")
	}
	if strings.TrimSpace(cfg.ReasoningEffort) != "" {
		// opencode calls this --variant; semantics line up closely enough
		// (high/medium/low/minimal).
		args = append(args, "--variant", strings.TrimSpace(cfg.ReasoningEffort))
	}
	if strings.TrimSpace(cfg.Workdir) != "" {
		args = append(args, "--dir", strings.TrimSpace(cfg.Workdir))
	}
	if opencodeSessionID != "" {
		args = append(args, "--session", opencodeSessionID)
	}

	// Prompt must be trailing. The "--" keeps prompts that begin with "-"
	// from being parsed as opencode flags.
	args = append(args, "--", prompt)
	return args
}
