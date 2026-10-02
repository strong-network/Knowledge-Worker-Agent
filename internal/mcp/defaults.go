// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"fmt"
	"io"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

// AuthKind classifies what an MCP server needs before it will work. It drives
// the badge in the MCP Servers modal so a user can tell at a glance why a
// server is off and what turning it on requires. It is descriptive metadata
// only — it never enables anything by itself.
type AuthKind string

const (
	// AuthUnknown is a server we have no catalogue entry for (user-added).
	AuthUnknown AuthKind = ""
	// AuthNone connects anonymously — enabling it is all that's needed.
	AuthNone AuthKind = "none"
	// AuthOAuth needs a browser OAuth sign-in (`opencode mcp auth <name>`).
	AuthOAuth AuthKind = "oauth"
	// AuthSetup needs per-tenant configuration or an API token that this UI
	// cannot obtain, so the browser sign-in flow will not work for it.
	AuthSetup AuthKind = "setup"
	// AuthLocal is a local stdio process — nothing to sign in to.
	AuthLocal AuthKind = "local"
	// AuthAPIKey needs the user's personal API key, pasted in the connectors
	// modal and sent as a header. Only an artifact can declare it.
	AuthAPIKey AuthKind = "api-key"
)

// DefaultServer describes an MCP server that should be registered automatically
// at startup if it isn't already configured.
//
// Catalogue entries leave Enabled false: every third-party server is registered
// DISABLED (see EnsureServer), so a new workspace starts with no active MCP
// servers and the user opts in to each one. Auth is metadata for the UI badge
// only.
type DefaultServer struct {
	Name      string
	Transport string // "http" | "sse" | "stdio"
	URL       string
	Command   string
	Args      []string
	Env       map[string]string
	Headers   map[string]string
	Auth      AuthKind
	// Enabled writes the entry switched ON at first registration.
	//
	// This exists for first-party servers only — ones that talk to this
	// process, not to a third party. "recall" is the case it was added
	// for: it reads the user's own chat history out of the local database over
	// loopback, so there is no vendor to opt in to, no credential to hand over
	// and nothing to pay for. Making the user find and flip a switch before the
	// agent can see its own past conversations would just make the feature
	// invisible.
	//
	// It only ever governs the FIRST write: EnsureServer is add-if-absent, so a
	// server the user later switches off stays off.
	Enabled bool
	// Help and HelpURL are the same UI text a provisioned artifact carries in
	// its kwa.auth block, for servers that ship in the catalogue instead.
	//
	// They matter most for AuthSetup servers: those have no sign-in flow, so
	// without an explanation the row is a dead end the user cannot act on. A
	// provisioned artifact's text still wins where both exist.
	Help    string
	HelpURL string
}

// AtlassianDefault is the Atlassian Rovo MCP server (formerly "Atlassian Remote
// MCP"). opencode performs the OAuth flow in the user's browser the first time
// the server is used (OAuth 2.1 with dynamic client registration) and stores
// the tokens itself, so no credentials are configured here.
//
// Transport is Streamable HTTP at the /v1/mcp/authv2 endpoint. Atlassian's
// current guidance (support.atlassian.com/atlassian-rovo-mcp-server) points all
// MCP clients at /v1/mcp/authv2 — the OAuth 2.1 endpoint that advertises RFC
// 9728 protected-resource metadata for standards-based discovery. The older
// /v1/mcp endpoint (no resource_metadata) and the legacy HTTP+SSE endpoint at
// /v1/sse (sunset 30 June 2026) are superseded; see LegacyAtlassianURLs.
var AtlassianDefault = DefaultServer{
	Name:      "atlassian",
	Transport: "http",
	URL:       AtlassianURL,
	Auth:      AuthOAuth,
}

// AtlassianURL is the current Atlassian Rovo MCP Streamable-HTTP endpoint.
const AtlassianURL = "https://mcp.atlassian.com/v1/mcp/authv2"

// LegacyAtlassianURLs are superseded Atlassian MCP endpoints that should be
// migrated to AtlassianURL on startup (see MigrateAtlassianURL).
var LegacyAtlassianURLs = []string{
	"https://mcp.atlassian.com/v1/mcp",
	"https://mcp.atlassian.com/v1/sse",
}

// GitHubDefault is the hosted, Copilot-backed "GitHub MCP Server". It is a
// remote Streamable-HTTP server authenticated with a bearer token, and the ONLY
// token that makes it useful is a GitHub Personal Access Token carrying repo
// scope. Two things it is emphatically not:
//
//   - It is not OAuth-capable. Its auth server does not support dynamic client
//     registration, so the generic browser flow cannot authenticate it
//     ("Incompatible auth server: does not support dynamic client
//     registration").
//   - It is not authenticated by the Copilot login, despite an earlier comment
//     here claiming so. That token carries only `read:user`. Measured against
//     the live endpoint: it completes `initialize`, returns all 47 tools, and
//     makes `opencode mcp list` report "connected" — then fails every repo
//     operation with `403 Forbidden: insufficient scopes`. It satisfies every
//     check the UI performs and none of the work the agent needs, which is why
//     it is no longer accepted as a bearer (see opencodeauth.GitHubMCPToken).
//
// So it is classified AuthSetup: it needs a credential this UI cannot obtain
// through a sign-in flow, exactly like the other setup-kind servers. The token
// is injected at runtime by EnsureGitHub; nothing is hard-coded here.
var GitHubDefault = DefaultServer{
	Name:      "github",
	Transport: "http",
	URL:       GitHubURL,
	Auth:      AuthSetup,
	Help:      "Needs a GitHub personal access token with repo scope. Add one under Accounts — signing in to GitHub Copilot is not enough, because that login only grants read:user and every repository call fails with “insufficient scopes”.",
	HelpURL:   "https://github.com/settings/tokens",
}

// GitHubURL is the hosted GitHub MCP Streamable-HTTP endpoint.
const GitHubURL = "https://api.githubcopilot.com/mcp"

// DefaultServers is the built-in starter catalogue we ensure is present on
// startup via the standard add-if-absent path: one server for each way of
// connecting that needs no setup. The other ways are covered elsewhere: GitHub
// needs a token injected at runtime and is registered by EnsureGitHub, local
// servers (obsidian, recall) are registered by the code that installs them, and
// a personal API key is only ever declared by a config repository artifact.
//
// Every entry is registered DISABLED, so a new workspace has no active MCP
// server at all. The user sees the catalogue in the MCP modal and switches on
// the ones they want; nothing is connected — and no tools are advertised to
// the model — until they do.
//
// Auth records what each server needs, verified against `opencode mcp list`,
// and is surfaced as a badge in the modal.
var DefaultServers = []DefaultServer{
	AtlassianDefault,
	{Name: "microsoft-learn", Transport: "http", URL: "https://learn.microsoft.com/api/mcp", Auth: AuthNone},
}

// RetiredServers were in the built-in catalogue of earlier releases. They are
// no longer installed, and an untouched entry is removed at startup (see
// removeUntouched). One the user switched on or edited stays, so it keeps its
// classification and help text here: without them an anonymous server would
// offer a sign-in that cannot work, and a setup server would lose its only
// explanation.
var RetiredServers = []DefaultServer{
	{Name: "aws", Transport: "http", URL: "https://aws-mcp.eu-central-1.api.aws/mcp", Auth: AuthNone},
	{Name: "context7", Transport: "http", URL: "https://mcp.context7.com/mcp", Auth: AuthNone},
	{Name: "deepwiki", Transport: "http", URL: "https://mcp.deepwiki.com/mcp", Auth: AuthNone},

	{Name: "clickup", Transport: "http", URL: "https://mcp.clickup.com/mcp", Auth: AuthOAuth},
	{Name: "figma", Transport: "http", URL: "https://mcp.figma.com/mcp", Auth: AuthOAuth},
	{Name: "gainsight", Transport: "http", URL: "https://mcp.staircase.ai/mcp", Auth: AuthOAuth},
	{Name: "pendo", Transport: "http", URL: "https://app.pendo.io/mcp/v0/shttp", Auth: AuthOAuth},
	{Name: "sentry", Transport: "http", URL: "https://mcp.sentry.dev/mcp", Auth: AuthOAuth},

	{
		Name: "azure-devops", Transport: "http", URL: "https://mcp.dev.azure.com/{organization}", Auth: AuthSetup,
		Help: "Edit the server URL and replace {organization} with your Azure DevOps organization name, then sign in.",
	},
	{
		Name: "microsoft-foundry", Transport: "http", URL: "https://mcp.ai.azure.com", Auth: AuthSetup,
		Help: "This endpoint currently rejects the MCP handshake (HTTP 405), so it cannot be connected yet. Left listed so it is discoverable when that changes.",
	},
	{
		Name: "pagerduty", Transport: "http", URL: "https://mcp.pagerduty.com/mcp", Auth: AuthSetup,
		Help: "Needs a PagerDuty API token supplied as a header — its auth server does not support the browser sign-in flow.",
	},
	{
		Name: "slack", Transport: "http", URL: "https://mcp.slack.com/mcp", Auth: AuthSetup,
		Help: "Needs a Slack app token supplied as a header — its auth server does not support the browser sign-in flow.",
	},
	{
		Name: "zephyr", Transport: "http", URL: "https://api.zephyrscale.smartbear.com/v2", Auth: AuthSetup,
		Help: "Needs a SmartBear API token for Zephyr Scale, supplied as a header. Ask whoever administers your Zephyr instance.",
	},
}

// AuthKindFor reports what a configured server needs, for the UI badge.
// Anything on a local (stdio) transport is AuthLocal regardless of catalogue
// membership — there is nothing to sign in to. Otherwise the answer comes from
// the catalogue; servers the user added themselves are AuthUnknown.
func AuthKindFor(name, transport string) AuthKind {
	switch strings.ToLower(strings.TrimSpace(transport)) {
	case "stdio", "local":
		return AuthLocal
	}
	if d, ok := CatalogueEntry(name); ok {
		return d.Auth
	}
	return AuthUnknown
}

// CatalogueEntry returns the built-in catalogue definition for a server name.
// github is included even though it is not in DefaultServers: it is registered
// separately (EnsureGitHub) but is still a server Knowledge Worker Agent ships and describes.
// Retired servers are included too, for the entries users kept.
func CatalogueEntry(name string) (DefaultServer, bool) {
	if strings.EqualFold(name, GitHubDefault.Name) {
		return GitHubDefault, true
	}
	for _, list := range [][]DefaultServer{DefaultServers, RetiredServers} {
		for _, s := range list {
			if strings.EqualFold(s.Name, name) {
				return s, true
			}
		}
	}
	return DefaultServer{}, false
}

// EnsureGitHub registers (or updates) the GitHub MCP server, attaching a
// bearer Authorization header when a usable token exists.
//
// Unlike EnsureServer (add-if-absent), this UPSERTS the header so that:
//   - a fresh install gets github configured once the user supplies a token, and
//   - an install left in the broken OAuth-only state (from an earlier build) is
//     repaired on the next token change or bootstrap.
//
// With NO token the server is still registered, disabled and header-less, so it
// appears in the MCP modal and can explain itself. Previously it was skipped
// entirely, which meant a workspace with no token showed no github row at all
// and the user had nothing to discover. It cannot connect in that state —
// verified against the binary, a header-less entry fails with
// "SSE error: Non-200 status code (400)" — so the UI keeps its toggle disabled
// until a token exists rather than letting the user switch on a broken server.
func EnsureGitHub(logw io.Writer, token string) {
	if logw == nil {
		logw = io.Discard
	}
	token = strings.TrimSpace(token)
	sv := serverView{
		Name:      GitHubDefault.Name,
		Transport: "http",
		URL:       GitHubDefault.URL,
		Enabled:   false, // opt-in only, like every other default
	}
	if token != "" {
		sv.Headers = map[string]string{"Authorization": "Bearer " + token}
	}

	existing, ok, err := getServer(GitHubDefault.Name)
	if err == nil && ok {
		// A token refresh must never change what the user switched on: carry
		// the current enabled state across the rewrite.
		sv.Enabled = existing.Enabled
		switch {
		case token == "":
			// No token to write. Leave whatever is there alone rather than
			// stripping a working header — ClearGitHubPAT is the only caller
			// that legitimately removes one, and it does so by re-running with
			// the next-best token.
			if existing.Headers["Authorization"] != "" {
				return
			}
		case existing.URL == sv.URL && existing.Headers["Authorization"] == sv.Headers["Authorization"]:
			// Unchanged; skip the rewrite to avoid churn on every bootstrap.
			return
		}
	}

	if _, err := upsertServer(sv); err != nil {
		fmt.Fprintf(logw, "  ⚠ MCP defaults: failed to configure %q: %v\n", GitHubDefault.Name, err)
		return
	}
	if token == "" {
		fmt.Fprintf(logw, "  … MCP defaults: registered %q without a token — add a GitHub PAT to use it\n", GitHubDefault.Name)
		return
	}
	fmt.Fprintf(logw, "  ✓ MCP defaults: configured %q (bearer token)\n", GitHubDefault.Name)
}

// EnsureDefaults registers any missing default MCP servers in opencode.json,
// and removes the untouched entries of retired ones. Errors are logged to the
// provided writer but never fatal — startup proceeds regardless.
//
// With central management on this installs nothing and instead conservatively
// removes the untouched entries it wrote on earlier boots, leaving the
// workspace's catalogue to the administrator.
func EnsureDefaults(logw io.Writer) {
	if logw == nil {
		logw = io.Discard
	}
	if config.CentralConfigEnabled() {
		RemoveCatalogueDefaults(logw)
		return
	}
	// Migrate before ensuring: an existing "atlassian" entry on a legacy URL
	// would otherwise be left untouched by EnsureServer (which is add-if-absent).
	MigrateAtlassianURL(logw)
	for _, s := range DefaultServers {
		EnsureServer(logw, s)
	}
	removeUntouched(logw, RetiredServers)
}

// MigrateAtlassianURL upgrades an existing "atlassian" MCP server that still
// points at a superseded endpoint (LegacyAtlassianURLs) to the current
// AtlassianURL, preserving all other config. It is a no-op when Atlassian is
// absent or already on the current URL. Any stored OAuth token is invalidated
// by opencode when the resource URL changes, so the user re-authorizes on next
// use — expected and safe.
func MigrateAtlassianURL(logw io.Writer) {
	if logw == nil {
		logw = io.Discard
	}
	sv, ok, err := getServer(AtlassianDefault.Name)
	if err != nil {
		fmt.Fprintf(logw, "  ⚠ MCP defaults: cannot read opencode config: %v\n", err)
		return
	}
	if !ok {
		return
	}
	current := strings.TrimRight(strings.TrimSpace(sv.URL), "/")
	if current == AtlassianURL {
		return
	}
	isLegacy := false
	for _, legacy := range LegacyAtlassianURLs {
		if current == legacy {
			isLegacy = true
			break
		}
	}
	if !isLegacy {
		// User (or a future default) set a non-legacy custom URL; leave it be.
		return
	}
	sv.URL = AtlassianURL
	sv.Transport = "http"
	if _, err := upsertServer(sv); err != nil {
		fmt.Fprintf(logw, "  ⚠ MCP defaults: failed to migrate %q URL: %v\n", AtlassianDefault.Name, err)
		return
	}
	fmt.Fprintf(logw, "  ✓ MCP defaults: migrated %q → %s\n", AtlassianDefault.Name, AtlassianURL)
}

// EnsureServer registers a single MCP server if not already present. Used for
// servers whose configuration must be resolved at runtime (e.g. obsidian,
// where command/args depend on the install + vault paths).
//
// New entries are written disabled unless s.Enabled is set, so a fresh
// workspace has no active third-party MCP server. Because this is
// add-if-absent, a server the user has already switched on -- or deliberately
// switched off -- is left completely untouched.
func EnsureServer(logw io.Writer, s DefaultServer) {
	if logw == nil {
		logw = io.Discard
	}
	existing, err := serverNames()
	if err != nil {
		fmt.Fprintf(logw, "  ⚠ MCP defaults: cannot read opencode config: %v\n", err)
		return
	}
	for _, n := range existing {
		if strings.EqualFold(n, s.Name) {
			return
		}
	}
	sv := serverView{
		Name:        s.Name,
		Transport:   s.Transport,
		URL:         s.URL,
		Command:     s.Command,
		Args:        s.Args,
		Environment: s.Env,
		Headers:     s.Headers,
		Enabled:     s.Enabled, // opt-in by default; see DefaultServer.Enabled
	}
	if sv.Transport == "" {
		if sv.URL != "" {
			sv.Transport = "http"
		} else {
			sv.Transport = "stdio"
		}
	}
	if _, err := upsertServer(sv); err != nil {
		fmt.Fprintf(logw, "  ⚠ MCP defaults: failed to add %q: %v\n", s.Name, err)
		return
	}
	fmt.Fprintf(logw, "  ✓ MCP defaults: added %q\n", s.Name)
}
