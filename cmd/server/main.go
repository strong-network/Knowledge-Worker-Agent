// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/agents"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/daynotes"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/defaults"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/files"
	gitops "github.com/strong-network/Knowledge-Worker-Agent/internal/git"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/guest"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/materializer"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/mcp"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/mcpauth"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/migration"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/obsidianinstaller"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodeauth"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodeinstaller"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/projects"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/providers"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/recall"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/savedprompts"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/scheduledtasks"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/sessions"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/sharing"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/tidyup"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/vertexauth"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/voice"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/workspaceapps"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/workspaceheartbeat"
)

//go:embed html
var htmlFS embed.FS

func init() {
	// Make sure PWA file extensions get the right Content-Type when served
	// by the embedded http.FileServerFS. Some Linux mime.types files lack
	// .webmanifest entirely, which leaves it as application/octet-stream
	// and stops browsers from honoring the manifest.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
	_ = mime.AddExtensionType(".svg", "image/svg+xml")
}

// opencodeModels caches the opencode github-copilot model list (provider/model
// identifiers). It is populated by refreshOpencodeModels() after the opencode
// binary is verified, on a periodic ticker, and right after a successful
// opencode login. Served by /api/models.
var opencodeModels []string
var opencodeModelsMu sync.RWMutex

// opencodeReady reports whether the opencode binary has been verified. Chat
// endpoints gate on this before spawning an opencode process.
var opencodeReady atomic.Bool

// opencodeEnsured is closed once bootstrap has installed or verified opencode, or failed to.
var opencodeEnsured = make(chan struct{})

// opencodeModelRefreshInterval controls how often the opencode model cache is
// refreshed in the background.
const opencodeModelRefreshInterval = 30 * time.Minute

// vertexModelAllowlist is the exact set of google-vertex models actually served
// by our Vertex backend. `opencode models google-vertex` lists the full
// upstream catalog, which is broader than what we offer, so the discovered
// Vertex list is curated down to this allow-list before it reaches the model
// picker. Identifiers are provider-qualified, matching `opencode` output.
// Keep this in sync with the models the Vertex backend exposes.
var vertexModelAllowlist = map[string]bool{
	"google-vertex/claude-haiku-4-5@20251001":               true,
	"google-vertex/claude-opus-4-6@default":                 true,
	"google-vertex/claude-opus-4-7@default":                 true,
	"google-vertex/claude-opus-4-8@default":                 true,
	"google-vertex/claude-opus-5@default":                   true,
	"google-vertex/claude-sonnet-4-5@20250929":              true,
	"google-vertex/claude-sonnet-4-6@default":               true,
	"google-vertex/claude-sonnet-5@default":                 true,
	"google-vertex/gemini-2.5-flash-lite":                   true,
	"google-vertex/gemini-2.5-pro":                          true,
	"google-vertex/gemini-3-flash-preview":                  true,
	"google-vertex/gemini-3.1-flash-lite":                   true,
	"google-vertex/gemini-3.1-pro-preview":                  true,
	"google-vertex/gemini-3.1-pro-preview-customtools":      true,
	"google-vertex/gemini-3.5-flash":                        true,
	"google-vertex/gemini-flash-latest":                     true,
	"google-vertex/gemini-flash-lite-latest":                true,
	"google-vertex/deepseek-ai/deepseek-v3.2-maas":          true,
	"google-vertex/moonshotai/kimi-k2-thinking-maas":        true,
	"google-vertex/openai/gpt-oss-120b-maas":                true,
	"google-vertex/qwen/qwen3-235b-a22b-instruct-2507-maas": true,
	"google-vertex/zai-org/glm-4.7-maas":                    true,
	"google-vertex/zai-org/glm-5-maas":                      true,
}

// curateVertexModels keeps only the google-vertex models we actually serve
// (see vertexModelAllowlist), preserving input order. Non-vertex identifiers
// are passed through untouched, so callers can hand it a provider's raw list
// without special-casing. Returns a non-nil slice.
func curateVertexModels(list []string) []string {
	out := make([]string, 0, len(list))
	for _, m := range list {
		if strings.HasPrefix(m, vertexauth.Provider+"/") && !vertexModelAllowlist[m] {
			continue
		}
		out = append(out, m)
	}
	return out
}

var (
	version   = "dev"
	buildTime = "unknown"
)

// bootstrap status flags. The HTTP server starts before opencode is fully
// provisioned so the page paints quickly; chat endpoints gate on
// opencodeReady to avoid kicking off a process before the binary is verified.
var (
	bootstrapStatus atomic.Value // string: "starting" | "ready" | "degraded"
)

func setStatus(s string) { bootstrapStatus.Store(s) }
func getStatus() string {
	if v := bootstrapStatus.Load(); v != nil {
		return v.(string)
	}
	return "starting"
}

func main() {
	// Subcommand dispatch: `knowledge-worker-agent health` performs an HTTP probe
	// against /healthz and exits. Useful for Docker HEALTHCHECK / k8s probes
	// without depending on wget or curl in the runtime image.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "health", "healthcheck":
			os.Exit(runHealthCheck(os.Args[2:]))
		case "version", "-v", "--version":
			fmt.Println(version)
			return
		case "mcp-recall":
			// Cross-session retrieval: serve the retrieval MCP server on stdio. opencode spawns
			// this; it must not start the HTTP server or write to stdout.
			os.Exit(runMCPRecall(os.Args[2:]))
		case "migrate":
			os.Exit(runMigrate(os.Args[2:]))
		case "-h", "--help", "help":
			fmt.Println("Usage: knowledge-worker-agent [serve|health|version|mcp-recall|migrate dry-run|on|undo]")
			fmt.Println("  serve       (default) start the HTTP server")
			fmt.Println("  health      hit /healthz on the configured host/port and exit 0/1")
			fmt.Println("  version     print the build commit and exit")
			fmt.Println("  mcp-recall  serve the cross-session retrieval MCP server on stdio")
			fmt.Println("  migrate dry-run  report what the one-folder migration, or once it has run")
			fmt.Println("                   its undo, would do, and exit")
			fmt.Println("  migrate on       carry the migration out, with the server stopped; at startup")
			fmt.Println("                   it runs unless KWA_MIGRATE=off, or an undo was the last run")
			fmt.Println("  migrate undo     move everything back, with the server stopped;")
			fmt.Println("                   KWA_MIGRATE=undo does it at startup, and =on migrates again")
			return
		case "serve":
			os.Args = append(os.Args[:1], os.Args[2:]...)
		}
	}
	opencodeauth.UserAgent = "KnowledgeWorkerAgent/" + version

	// Mirror all log output to a file (default /var/log/chat.log) as well as
	// stderr. Done before config.Init so startup logging is captured too.
	closeLog := setupFileLogging()
	defer closeLog()

	if old := env.OldInUse(); len(old) > 0 {
		fmt.Fprintf(os.Stderr, "  ⚠ Settings with old names, which a later release stops reading: %s\n", strings.Join(old, ", "))
	}

	// Before anything opens a file an interrupted migration may have moved.
	if err := migration.Recover(os.Stdout); err != nil {
		log.Fatalf("One-folder migration: %v", err)
	}
	if n, err := migration.Prune(time.Now()); err != nil {
		log.Printf("One-folder migration: removing backups older than 30 days: %v", err)
	} else if n > 0 {
		log.Printf("One-folder migration: removed the backups of %d runs older than 30 days", n)
	}
	if from, err := migration.MoveGitHubToken(); err != nil {
		log.Printf("One-folder migration: moving the saved GitHub token into %s: %v", layout.System(), err)
	} else if from != "" {
		log.Printf("One-folder migration: moved the saved GitHub token from %s into %s", from, layout.System())
	}

	config.Init()
	setStatus("starting")
	migrateMode := migrationAtStartup()
	if migrateMode == migration.On || migrateMode == migration.Undo {
		runMigrationAtStartup(migrateMode)
	}

	if err := db.Init(config.DBPath); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()
	if err := migration.HoldDatabase(config.DBPath); err != nil {
		log.Fatalf("Database: %v", err)
	}

	if migrateMode == migration.DryRun && !layout.Migrated() {
		// It only reads, so it needn't hold up the server.
		go func() {
			<-opencodeEnsured // a migration would install the pinned version first too
			r := migration.Assess(config.DBPath, config.OpencodeBin)
			r.Write(os.Stdout, migration.DryRun)
			migrationState.Store(viewOf(r))
		}()
	}
	if stray, ok := migration.StrayDatabase(); ok {
		fmt.Fprintf(os.Stderr, "  ⚠ One-folder migration: %s has appeared since the migration; an older release probably ran, and chats made there aren't shown here\n", stray)
		migrationState.Store(&migrationView{State: "stray", Path: stray})
	}

	stopHeartbeat := workspaceheartbeat.Start(context.Background(), chat.HasActiveTasks)
	defer stopHeartbeat()

	// Run all heavy initialization (opencode install/verify, obsidian install,
	// MCP defaults, model discovery) in the background so the HTTP listener
	// starts immediately. Chat endpoints gate on opencodeReady to avoid
	// spawning an opencode process before it's verified.
	go bootstrap()

	addr := fmt.Sprintf("%s:%d", config.Host, config.Port)
	fmt.Printf("  💾 Database: %s\n", config.DBPath)
	if config.OwnerFullName != "" || config.OwnerEmail != "" {
		fmt.Printf("  👤 Owner: %s <%s>\n", config.OwnerFullName, config.OwnerEmail)
	}
	fmt.Printf("  → http://%s (bootstrap running in background)\n\n", addr)

	// Dictation: checks the dictation engine in the background; the routes answer
	// 404 until it passes.
	stopVoice := voice.Start()
	defer stopVoice()

	mux := http.NewServeMux()
	registerRoutes(mux)

	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  0,
		WriteTimeout: 0,
	}

	// Serve in the background so we can react to OS signals and shut down
	// cleanly. A clean shutdown matters here because the process owns child
	// `opencode serve` processes that must be killed (otherwise they leak on
	// every restart/redeploy).
	serveErr := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serveErr <- err
		}
	}()

	// Chat sharing: the guest surface is its own listener, never a route on this one.
	stopGuest := func(context.Context) {}
	if guestFiles, err := fs.Sub(htmlFS, "html"); err != nil {
		log.Printf("[guest] embedded frontend unavailable: %v", err)
	} else {
		stopGuest = guest.Start(guestFiles, opencodeReady.Load)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serveErr:
		log.Printf("HTTP server error: %v", err)
	case sig := <-sigCh:
		log.Printf("received %s, shutting down", sig)
	}

	// Best-effort graceful shutdown of in-flight HTTP requests.
	stopHeartbeat()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	stopGuest(shutdownCtx)
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown error: %v", err)
	}
	cancel()

	// Terminate any managed `opencode serve` child processes. db.Close runs via
	// the deferred call above.
	chat.CloseSupervisor()
}

// bootstrap performs all startup work that doesn't need to block the HTTP
// listener. It runs the opencode install/verify and the obsidian installer in
// parallel, then registers MCP defaults and discovers models. Idempotent —
// handlers gate on opencodeReady before spawning opencode processes.
func bootstrap() {
	// Auth change hooks are registered FIRST, before any of the install work
	// below, because the UI is already reachable: main() starts the HTTP
	// listener alongside this goroutine.
	//
	// On a net-new workspace the installs below take minutes (an npm install
	// and a binary download), and a user can store a GitHub PAT the whole time.
	// StoreGitHubPAT fires its change hook only if one is set, and an unset
	// hook is a silent no-op — so registering these after wg.Wait() meant the
	// PAT was saved, the GitHub MCP server was never written, and it appeared
	// only after the next restart. The "opencode not ready" early return below
	// made that permanent on a degraded workspace, where the hook was never
	// registered at all.
	//
	// The bearer must be a properly-scoped token (env PAT or a user-supplied
	// stored PAT). The github-copilot login token is deliberately NOT a fallback:
	// it connects and lists every tool, then fails "insufficient scopes" on any
	// repo call. ensureGitHubMCP centralizes this resolution.
	ensureGitHubMCP := func() {
		token, source := opencodeauth.GitHubMCPToken()
		log.Printf("[main] configuring GitHub MCP (token source=%s)", source)
		mcp.EnsureGitHub(os.Stderr, token)
	}
	opencodeauth.SetOnLoginSuccess(func() {
		refreshOpencodeModels()
		ensureGitHubMCP()
	})
	// Re-run when the user sets/clears their GitHub PAT.
	opencodeauth.SetOnGitHubTokenChange(ensureGitHubMCP)
	vertexauth.SetOnLoginSuccess(func() {
		refreshOpencodeModels()
	})

	var wg sync.WaitGroup
	wg.Add(3)

	// (1) Obsidian MCP install (independent of the opencode binary verify).
	var obsRes obsidianinstaller.Result
	go func() {
		defer wg.Done()
		obsidianAuto := strings.ToLower(strings.TrimSpace(env.Get("KWA_OBSIDIAN_AUTO_INSTALL")))
		if obsidianAuto == "0" || obsidianAuto == "false" || obsidianAuto == "no" || obsidianAuto == "off" {
			return
		}
		obsRes, _ = obsidianinstaller.Ensure(os.Stderr, obsidianinstaller.Options{
			InstallDir: obsidianinstaller.DefaultInstallDir(),
			VaultDir:   obsidianinstaller.DefaultVaultDir(),
		})
	}()

	// (2) Install what Knowledge Worker Agent still ships under ~/.config/opencode —
	// the opencode plugins and the document-handling skills — and retire the
	// content bundle earlier builds installed (agents, context, AGENTS.md,
	// product-specific skills), which the config repo now owns.
	//
	// None of this is gated on KWA_CENTRAL_CONFIG. The plugins and document
	// skills are platform capability every workspace needs, and the retirement
	// has to reach un-managed workspaces too: that is precisely where a stale
	// AGENTS.md would keep pointing the model at agents and context that no
	// longer exist.
	go func() {
		defer wg.Done()
		dir := opencodeConfigDir()
		if dir == "" {
			fmt.Fprintf(os.Stderr, "  ⚠ Cannot determine config dir for opencode defaults\n")
			return
		}
		if err := defaults.InstallPlugins(os.Stderr, dir); err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠ Installing opencode plugins: %v\n", err)
		}
		if err := defaults.InstallSkills(os.Stderr, dir); err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠ Installing document skills: %v\n", err)
		}
		// After the installs, so a workspace whose manifest still lists the
		// document skills keeps the copies just written. RetireLegacyDefaults
		// skips still-shipped paths, so the order is belt and braces rather
		// than load-bearing.
		if err := defaults.RetireLegacyDefaults(os.Stderr, dir); err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠ Retiring bundled default files: %v\n", err)
		}
		// The skills picker reads through a cache; this pass is the one moment
		// we know the enumeration may have changed underneath it.
		chat.InvalidateSkillCache()
		if config.CentralConfigEnabled() {
			warnCentralConfigUnprovisioned()
		}
	}()

	// (3) OpenCode binary verify / install / update. This is the only chat
	// backend, so a failure here flips the overall status to "degraded".
	go func() {
		defer wg.Done()
		defer close(opencodeEnsured)
		if err := ensureOpencode(); err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ OpenCode bootstrap failed: %v\n", err)
			setStatus("degraded")
			return
		}
		opencodeReady.Store(true)
	}()

	wg.Wait()

	if !opencodeReady.Load() {
		// OpenCode couldn't be provisioned; chat endpoints will keep
		// returning 503. Leave status as "degraded" for the UI.
		return
	}

	if v := opencodeinstaller.VersionString(config.OpencodeBin); v != "" {
		fmt.Printf("  ✓ OpenCode: %s\n", v)
	}

	// (4) Resolve the project's central configuration and materialize it into
	// the platform-owned OPENCODE_CONFIG_DIR, above the user's untouched Global.
	// This runs before model discovery / server pre-warm so any spawned or
	// served opencode process inherits the provisioned config. It is a clean
	// no-op until the project id and config repo are provisioned to the
	// workspace (KWA_PROJECT_ID / KWA_CONFIG_REPO_URL).
	// Connector API keys: an "api-key" server's header reads the user's key file, which
	// must exist before any opencode process loads the config referencing it.
	materializer.SetMCPKeyFile(mcp.EnsureKeyFile)
	mcp.SetConfigChangeHook(chat.ReloadServerAgents)
	runMaterializer()

	// Model providers: the provider registry reads what the materializer just wrote, so
	// it is initialised here — after materialization, before model discovery.
	initProviderRegistry()

	// Discover models, start a periodic refresh, and refresh again after each
	// successful opencode login.
	refreshOpencodeModels()
	startOpencodeModelRefresher()
	// Probe in the background: a provider only proves usable by answering, and
	// a first-run probe takes seconds. The reach-change hook rebuilds the model
	// list if any provider turns out to be unreachable.
	providers.ProbeAllInBackground()

	// Workspace hygiene. Deletes chats that contain nothing — no
	// messages, no draft, nothing running — once they have been idle past the
	// empty-chat window. Chats containing work are never touched here; they are
	// only ever removed by the user from the review surface.
	tidyup.StartSweeper()

	// Cross-session retrieval: day notes. Summarises closed days of long or multi-day
	// chats so recall can find what a chat became, not just what it opened
	// with. Started after model discovery above so the first pass has a model
	// to resolve; the pass declines rather than falling back if no small-class
	// model is available.
	daynotes.Start()

	// Default MCP servers (written to opencode.json). Under central management
	// this installs nothing and instead retires the built-in catalogue.
	mcp.EnsureDefaults(os.Stderr)
	// Central connectors: make every centrally provisioned MCP server available but OFF.
	// Runs after the materializer has exported OPENCODE_CONFIG_DIR, and after
	// EnsureDefaults so a provisioned name never gets a competing catalogue
	// entry written into the user's Global. Without this a provisioned server
	// would be connected the moment its artifact was assigned — provisioning
	// would mean enabling, and the user would be paying for tools nobody chose.
	mcp.EnsureProvisionedStubs(os.Stderr)
	// GitHub MCP has no sign-in flow we can drive: its auth server does not
	// support OAuth dynamic client registration, and the Copilot login token
	// lacks the scopes for repo work. A personal access token is the only path.
	// The change hooks were registered at the top of bootstrap; this is the
	// startup pass, for a workspace that already has a token in env or on disk.
	ensureGitHubMCP()

	// Vertex AI (google-vertex provider): seed GOOGLE_VERTEX_PROJECT/LOCATION so
	// child opencode processes light up the provider. The model-cache refresh
	// after a "Sign in with Google Cloud" (gcloud ADC) flow is hooked above.
	vertexauth.EnsureEnv()

	if obsRes.Bin != "" {
		mcp.EnsureOwnedServer(os.Stderr, mcp.DefaultServer{
			Name:      "obsidian",
			Transport: "stdio",
			Command:   obsRes.Bin,
			Args:      []string{obsRes.VaultDir},
			Auth:      mcp.AuthLocal,
		})
	}

	ensureRecallMCP()
	mcp.WarnMissingCommands(os.Stderr)

	// Cache MCP auth status (which server is signed in) so the UI can render it
	// instantly, and keep it fresh in the background. Checking status runs
	// `opencode mcp list`, which is slow, so we do it off the request path.
	// Also re-check right after a successful MCP OAuth sign-in.
	go mcpauth.RefreshAllStatuses()
	startMcpStatusRefresher()
	mcpauth.SetOnAuthSuccess(func() { mcpauth.RefreshAllStatuses() })

	// Background auto-sync for repo-backed projects: periodically fetch and
	// safely fast-forward-pull their shared workspaces.
	projects.StartAutoSync()

	// Background scheduler for unattended prompt runs. Ticks in-process,
	// so it only runs while the workspace is up; its first pass catches up
	// occurrences missed while the workspace was off.
	scheduledtasks.StartScheduler()

	// Server backend (KWA_OPENCODE_USE_SERVER): pre-warm a server at the workspace
	// root so the first chat turn is fast and any auth/model problems surface
	// during bootstrap rather than on the first prompt. Lazily-started
	// per-workdir servers still spin up on demand for other directories.
	if serverBackendEnabled() {
		if sup := chat.Supervisor(); sup != nil {
			warmCtx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
			if _, _, err := sup.Ensure(warmCtx, config.Workspace, config.Workspace); err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ OpenCode server pre-warm failed: %v\n", err)
			} else {
				fmt.Printf("  ✓ OpenCode server ready at %s\n", config.Workspace)
			}
			cancel()
		}
	}

	setStatus("ready")
	fmt.Println("  ✓ Bootstrap complete")
}

// serverBackendEnabled mirrors chat.useServerBackend for the bootstrap path:
// reports whether KWA_OPENCODE_USE_SERVER selects the HTTP server backend.
func serverBackendEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(env.Get("KWA_OPENCODE_USE_SERVER"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// warnCentralConfigUnprovisioned flags the one combination that leaves a
// workspace with nothing: central config mode turned on while the inputs the
// materializer needs are missing. The flag is still honoured — it was set
// deliberately — but silently ending up with no agents or skills would look
// like a bug rather than a misconfiguration, so say so at startup.
func warnCentralConfigUnprovisioned() {
	var missing []string
	for _, name := range []string{materializer.EnvProjectID, materializer.EnvConfigRepo} {
		if strings.TrimSpace(env.Get(name)) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "  ⚠ %s is on but %s unset — no central config to materialize, so this workspace will have no platform agents, skills or context\n",
		config.EnvCentralConfig, strings.Join(missing, " and "))
}

// runMaterializer resolves and applies the central workspace
// configuration. It reads its inputs from the environment (KWA_PROJECT_ID,
// KWA_CONFIG_REPO_URL, and optional overrides) and, on success, exports
// OPENCODE_CONFIG_DIR so every subsequently spawned/served opencode process
// layers the platform-owned config above the user's Global. Failures are
// logged but non-fatal: chat still works from the user's own config.
func runMaterializer() {
	opts := materializer.Options{Log: os.Stdout}.FromEnv(opencodeConfigDir())
	res, err := materializer.Run(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ⚠ Central configuration: %v\n", err)
		return
	}
	if res.Skipped || res.ConfigDir == "" {
		return
	}
	// Export the platform-owned dir so child opencode processes pick it up.
	if err := os.Setenv(materializer.EnvConfigDir, res.ConfigDir); err != nil {
		fmt.Fprintf(os.Stderr, "  ⚠ Central configuration: set %s: %v\n", materializer.EnvConfigDir, err)
	}
}

// opencodeConfigDir returns OpenCode's global config directory, where it
// reads global agents, skills, and AGENTS.md. It mirrors OpenCode's own
// resolution: $XDG_CONFIG_HOME/opencode when set, otherwise
// ~/.config/opencode. Returns "" if the home dir can't be determined.
func opencodeConfigDir() string {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, "opencode")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "opencode")
}

// ensureOpencode verifies / installs / updates the opencode binary, honoring
// KWA_OPENCODE_AUTO_UPDATE and KWA_OPENCODE_INSTALL_DIR. On success
// config.OpencodeBin points at a working
// binary. opencode is the only chat backend, so a returned error is fatal to
// chat readiness.
func ensureOpencode() error {
	autoUpdate := strings.ToLower(strings.TrimSpace(env.Get("KWA_OPENCODE_AUTO_UPDATE")))
	if autoUpdate == "" {
		autoUpdate = "true"
	}
	skipUpdate := autoUpdate == "0" || autoUpdate == "false" || autoUpdate == "no" || autoUpdate == "off"

	installDir := config.EnvOrDefault("KWA_OPENCODE_INSTALL_DIR", opencodeinstaller.DefaultInstallDir)

	if skipUpdate {
		if !opencodeinstaller.VerifyBin(config.OpencodeBin) {
			fmt.Fprintf(os.Stderr, "  ⤓ OpenCode binary not usable at: %s — installing ...\n", config.OpencodeBin)
			installed, ierr := opencodeinstaller.Install(os.Stderr, opencodeinstaller.InstallOptions{InstallDir: installDir})
			if ierr != nil {
				return fmt.Errorf("auto-install failed: %w", ierr)
			}
			config.OpencodeBin = installed
		}
		return verifyOpencodeRuns()
	}

	res, err := opencodeinstaller.Ensure(os.Stderr, opencodeinstaller.EnsureOptions{
		CurrentBin: config.OpencodeBin,
		InstallDir: installDir,
	})
	if err != nil {
		return fmt.Errorf("install/update failed: %w", err)
	}
	if res.Bin != "" {
		config.OpencodeBin = res.Bin
	}
	return verifyOpencodeRuns()
}

func verifyOpencodeRuns() error {
	if !opencodeinstaller.VerifyBin(config.OpencodeBin) {
		return fmt.Errorf("opencode binary not usable at %s", config.OpencodeBin)
	}
	return nil
}

// refreshOpencodeModels re-queries opencode for the model list of every
// provider the user is authenticated with and atomically swaps the cache. Safe
// to call repeatedly (ticker, post-login). On error the previous cache is kept.
func refreshOpencodeModels() {
	// Which providers is the user ACTUALLY signed in to? `opencode models
	// <provider>` lists a provider's catalog once it is merely *configured*
	// (e.g. Vertex env vars are seeded by EnsureEnv), not once it is
	// authenticated — so merging on "the command returned rows" leaks models
	// the user cannot actually run. A Copilot-only user would then be offered
	// (and, via the Default/Thinking presets, auto-assigned) a Vertex model
	// that fails at submit time. Gate the merge on real auth state instead.
	//
	// The registry resolves this once for every provider the workspace offers,
	// built-in or config-declared, and is reused for the default-model
	// preference below — so a refresh costs at most one auth probe per provider.
	//
	// Every stage is timed. Model discovery has been seen taking ~60s on a cold
	// workspace where a warm `opencode models` returns in ~2s, and the cost was
	// invisible: a provider that exceeds the deadline is skipped with a warning,
	// so the boot completes and only the wall-clock time betrays it. The timings
	// are always on rather than behind a debug flag because the expensive case is
	// a cold start — it happens once, on the first boot after an install, which
	// is precisely when nobody has had the chance to turn a flag on.
	refreshStart := time.Now()
	reg := providers.Resolve()
	resolveTook := time.Since(refreshStart)
	// Holding a credential is not the same as being able to reach the provider:
	// an org network policy can refuse one we are perfectly signed in to.
	// Offering its models anyway is what made "Default" arm a model that could
	// only ever fail on submit.
	authed := providers.UsableProviders()
	if withheld := len(reg.Authenticated()) - len(authed); withheld > 0 {
		fmt.Fprintf(os.Stderr, "  ⚠ OpenCode model refresh: %d authenticated provider(s) withheld as unreachable\n", withheld)
	}
	fmt.Fprintf(os.Stderr, "  · OpenCode model refresh: %d/%d provider(s) usable, resolved in %s\n",
		len(authed), len(reg.Providers), resolveTook.Round(time.Millisecond))

	var merged []string
	seen := make(map[string]bool)
	anyOK := false
	for _, provider := range authed {
		fetchStart := time.Now()
		list, err := opencodeauth.FetchModelsFor(provider)
		fetchTook := time.Since(fetchStart)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠ OpenCode model refresh (%s) failed after %s: %v\n",
				provider, fetchTook.Round(time.Millisecond), err)
			continue
		}
		fmt.Fprintf(os.Stderr, "  · OpenCode models (%s): %d in %s\n",
			provider, len(list), fetchTook.Round(time.Millisecond))
		anyOK = true
		// A provider may serve less than its vendor's published catalogue.
		// The registry knows which ones do; this loop no longer needs to.
		list = providers.CurateModels(provider, list)
		for _, m := range list {
			if !seen[m] {
				seen[m] = true
				merged = append(merged, m)
			}
		}
	}
	if !anyOK {
		// No provider is signed in, or every signed-in provider errored — keep
		// the previously cached list rather than emptying the picker. A login
		// callback re-runs this refresh as soon as the user signs in.
		return
	}
	sort.Strings(merged)
	models := merged
	if models == nil {
		models = []string{}
	}
	opencodeModelsMu.Lock()
	opencodeModels = models
	opencodeModelsMu.Unlock()
	// Let the config layer know what is actually runnable, so a session pinned
	// to a model from a provider the user isn't signed in to falls back to the
	// default instead of failing the turn (see config.ResolveModel).
	config.SetAvailableModels(models)
	// Publish the presets the assigned providers nominate, so Default/Thinking
	// resolve to admin-chosen models instead of the Claude-family heuristic
	// (which matches nothing on a non-Claude provider and would leave both
	// presets resolving to nothing).
	config.SetPresetModels(registryPresets(reg, models))
	// Record a server-side default so sessions that carry no explicit model
	// are still launched with `--model` (opencode has no built-in default
	// provider on a fresh machine — without this the run has "no providers").
	// Bias the default toward whichever assistant the user is actually signed
	// in to, so a Vertex login defaults to a Vertex model and a Copilot login
	// to a Copilot model (see preferredDefaultProvider).
	config.SetDefaultModelForProvider(models, preferredDefaultProvider(authed))
	fmt.Printf("  ✓ OpenCode models: %d available in %s (default: %s)\n",
		len(models), time.Since(refreshStart).Round(time.Millisecond), config.DefaultModel())
}

// registryPresets resolves the preset nominations for the providers currently
// offered, filtered against the models actually discovered.
//
// A nomination naming a model that is not there is skipped rather than fatal,
// so a typo or a retired model degrades to the next provider and ultimately to
// the built-in heuristic, instead of leaving the preset resolving to nothing.
func registryPresets(reg providers.Registry, models []string) map[string]string {
	available := make(map[string]bool, len(models))
	for _, m := range models {
		available[strings.ToLower(m)] = true
	}
	isAvailable := func(m string) bool { return available[strings.ToLower(strings.TrimSpace(m))] }

	out := map[string]string{}
	for _, name := range []string{config.PresetDefault, config.PresetThinking, config.PresetSmall} {
		if m, ok := reg.Preset(name, isAvailable); ok {
			out[name] = m
		}
	}
	return out
}

// preferredDefaultProvider returns the provider whose model should back the
// server-side default: the first authenticated provider in registry order.
//
// Gating on authentication (rather than mere configuration) is the point — a
// provider can be configured without being usable, e.g. Vertex env vars present
// but no gcloud login yet, and defaulting to it produces a submit-time failure.
//
// Registry order carries the preference. For the built-ins that is the order
// they are registered at startup, which keeps the previous Vertex-first
// behaviour; for config-declared providers it is the assignment order in the
// config repo, so an admin expresses the preference by ordering the array.
// Returns "" when nothing is authenticated, so the picker falls back to the
// full merged list.
func preferredDefaultProvider(authenticated []string) string {
	if len(authenticated) == 0 {
		return ""
	}
	return authenticated[0]
}

// initProviderRegistry wires up the provider registry: the built-in
// providers and their auth probes, the credential store backing user-key
// providers, and the hook that re-discovers models after a key changes.
//
// Registration order is the preference order. Vertex is registered first
// because it wins the both-signed-in tiebreak for the server-side default
// (Vertex-first product direction) — behaviour preserved from the two-branch
// preferredDefaultProvider this replaces.
//
// Called after runMaterializer(), because in central-models mode the registry
// reads what the materializer just wrote.
func initProviderRegistry() {
	providers.RegisterBuiltin(providers.Builtin{
		ID:        vertexauth.Provider,
		Label:     "Google Cloud (Vertex AI)",
		AuthType:  providers.AuthGcloudADC,
		CheckAuth: func() bool { return vertexauth.CheckStatus().Authenticated },
		// `opencode models google-vertex` lists the full upstream catalogue,
		// which is wider than what our Vertex backend actually serves.
		CurateModels: curateVertexModels,
	})
	providers.RegisterBuiltin(providers.Builtin{
		ID:        opencodeauth.Provider,
		Label:     "GitHub Copilot",
		AuthType:  providers.AuthOAuthDevice,
		CheckAuth: func() bool { return opencodeauth.CheckStatus().Authenticated },
	})

	providers.SetCredentialChecker(opencodeauth.HasCredential)
	providers.SetKeyStore(opencodeauth.SetAPIKey, opencodeauth.RemoveAPIKey)
	// The browser needs the resolved Default/Thinking nominations to label the
	// composer chips and to arm the right model. Serve what was already
	// resolved here rather than having the frontend re-derive it.
	providers.SetResolvedPresets(config.PresetModels)
	// A newly-stored key makes that provider's models runnable immediately;
	// without this the picker stays empty until the next refresh tick.
	providers.SetKeyChangeHook(refreshOpencodeModels)
	installProviderProbes()

	if reg := providers.Resolve(); reg.Mode == providers.ModeFallback {
		// Central management is on but nothing usable resolved — the flag was
		// set before the assignment landed, the config repo was unreachable, or
		// the artifact failed validation. Falling back keeps the workspace
		// usable; saying so loudly is what stops it looking like the assignment
		// silently worked. Mirrors warnCentralConfigUnprovisioned() for the
		// agents/skills/context side.
		fmt.Fprintf(os.Stderr,
			"  ⚠ %s is on but no config-declared provider resolved (%s).\n"+
				"    Falling back to the built-in providers so this workspace keeps working.\n",
			config.EnvCentralConfig, reg.FallbackReason)
	}
}

// startOpencodeModelRefresher launches a background ticker that periodically
// refreshes the opencode model cache.
func startOpencodeModelRefresher() {
	go func() {
		ticker := time.NewTicker(opencodeModelRefreshInterval)
		defer ticker.Stop()
		for range ticker.C {
			refreshOpencodeModels()
		}
	}()
}

// mcpStatusRefreshInterval controls how often the cached MCP auth status is
// re-checked in the background (via `opencode mcp list`).
const mcpStatusRefreshInterval = 5 * time.Minute

// startMcpStatusRefresher periodically refreshes the cached MCP sign-in status.
func startMcpStatusRefresher() {
	go func() {
		ticker := time.NewTicker(mcpStatusRefreshInterval)
		defer ticker.Stop()
		for range ticker.C {
			mcpauth.RefreshAllStatuses()
		}
	}()
}

// requireOpencodeReady wraps a handler so it returns 503 until the opencode
// binary has been verified. Used for endpoints that spawn opencode processes.
func requireOpencodeReady(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !opencodeReady.Load() {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, `{"error":"opencode binary still initializing","status":%q}`, getStatus())
			return
		}
		h(w, r)
	}
}

func registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"name":            config.OwnerFullName,
			"email":           config.OwnerEmail,
			"workspace_id":    os.Getenv("STRONG_NETWORK_WORKSPACE_ID"),
			"apps_domain":     os.Getenv("STRONG_NETWORK_WORKSPACE_APPS_DOMAIN"),
			"default_backend": config.DefaultBackend,
			// Where new chats, clones and the file view start.
			"base_dir": config.Workspace,
			// Chat sharing: the Share action is absent, not disabled, without it.
			"sharing_available": guest.Available(),
			// Dictation: likewise the Dictate button.
			"voice_available": voice.Available(),
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	// Core API
	mux.HandleFunc("GET /api/version", handleVersion)
	voice.RegisterRoutes(mux)
	mux.HandleFunc("POST /api/activity", workspaceheartbeat.HandleActivity)
	mux.HandleFunc("GET /api/models", handleModels)
	mux.HandleFunc("GET /api/agents", handleAgents)
	mux.HandleFunc("GET /api/skills", handleSkills)
	registerAgentBuilderRoutes(mux)
	(&scheduledtasks.Handlers{}).RegisterRoutes(mux)
	mux.HandleFunc("GET /api/model-stats", handleModelStats)
	mux.HandleFunc("POST /api/chat", requireOpencodeReady(sessions.HandleChat))
	mux.HandleFunc("GET /api/sessions/{session_id}/stream", sessions.HandleAttachStream)
	mux.HandleFunc("GET /api/sessions/{session_id}/events", sessions.HandleSessionEvents)
	mux.HandleFunc("GET /api/sessions/{session_id}/share", sharing.HandleGet)
	mux.HandleFunc("PUT /api/sessions/{session_id}/share", sharing.HandleStart)
	mux.HandleFunc("DELETE /api/sessions/{session_id}/share", sharing.HandleStop)
	mux.HandleFunc("GET /api/share/audience", sharing.HandleGetAudience)
	mux.HandleFunc("PUT /api/share/audience", sharing.HandlePutAudience)
	mux.HandleFunc("GET /api/share/activity", sharing.HandleActivity)
	mux.HandleFunc("GET /api/share/platform", workspaceapps.HandlePlatformCheck)
	mux.HandleFunc("GET /api/sessions/active", sessions.HandleActiveStreams)
	mux.HandleFunc("GET /api/history", sessions.HandleHistory)
	mux.HandleFunc("GET /api/sessions", sessions.HandleSessions)
	mux.HandleFunc("POST /api/sessions/new", sessions.HandleNewSession)
	mux.HandleFunc("GET /api/sessions/{session_id}/config", sessions.HandleSessionConfig)
	mux.HandleFunc("PATCH /api/sessions/{session_id}/config", sessions.HandleUpdateSessionConfig)
	mux.HandleFunc("PUT /api/sessions/{session_id}/rename", sessions.HandleRenameSession)
	mux.HandleFunc("DELETE /api/sessions/{session_id}/last", sessions.HandleUndoSession)
	mux.HandleFunc("POST /api/sessions/{session_id}/stop", sessions.HandleStopSession)
	mux.HandleFunc("GET /api/sessions/{session_id}/export", sessions.HandleExportSession)
	mux.HandleFunc("DELETE /api/sessions/{session_id}", sharing.RetireAppAfter(sessions.HandleDeleteSession))
	mux.HandleFunc("POST /api/sessions/{session_id}/permission", sessions.HandlePermission)
	mux.HandleFunc("GET /api/sessions/{session_id}/queue", sessions.HandleQueueList)
	mux.HandleFunc("POST /api/sessions/{session_id}/queue", requireOpencodeReady(sessions.HandleQueueAdd))
	mux.HandleFunc("DELETE /api/sessions/{session_id}/queue", sessions.HandleQueueClear)
	mux.HandleFunc("DELETE /api/sessions/{session_id}/queue/{id}", sessions.HandleQueueDelete)
	mux.HandleFunc("POST /api/sessions/{session_id}/pin", sessions.HandlePinSession)
	mux.HandleFunc("POST /api/sessions/{session_id}/favorite", sessions.HandleFavoriteSession)
	mux.HandleFunc("GET /api/sessions/{session_id}/draft", sessions.HandleGetSessionDraft)
	mux.HandleFunc("PUT /api/sessions/{session_id}/draft", sessions.HandleSetSessionDraft)
	mux.HandleFunc("GET /api/sessions/{session_id}/mcp", sessions.HandleGetSessionMcp)
	mux.HandleFunc("PUT /api/sessions/{session_id}/mcp", sessions.HandleSetSessionMcp)
	mux.HandleFunc("POST /api/sessions/{session_id}/project-star", projects.HandleStarChat)
	// Durable-storage backup (chat). Not gated on opencode readiness —
	// backup only needs git, which is always available.
	mux.HandleFunc("GET /api/sessions/{session_id}/backup", sessions.HandleBackupState)
	mux.HandleFunc("POST /api/sessions/{session_id}/backup/enable", sessions.HandleEnableBackup)

	// Projects
	mux.HandleFunc("GET /api/projects", projects.HandleList)
	mux.HandleFunc("POST /api/projects", projects.HandleCreate)
	mux.HandleFunc("GET /api/projects/{id}", projects.HandleGet)
	mux.HandleFunc("PATCH /api/projects/{id}", projects.HandleUpdate)
	mux.HandleFunc("DELETE /api/projects/{id}", sharing.RetireAppAfter(projects.HandleDelete))
	mux.HandleFunc("GET /api/projects/{id}/mcp", projects.HandleGetProjectMcp)
	mux.HandleFunc("PUT /api/projects/{id}/mcp", projects.HandleSetProjectMcp)
	mux.HandleFunc("GET /api/projects/{id}/chats", projects.HandleListChats)
	mux.HandleFunc("POST /api/projects/{id}/chats", requireOpencodeReady(projects.HandleNewChat))
	mux.HandleFunc("GET /api/projects/{id}/summarize/candidates", projects.HandleSummarizeCandidates)
	mux.HandleFunc("POST /api/projects/{id}/summarize", requireOpencodeReady(projects.HandleSummarize))
	// Durable-storage backup (project) + Keep.
	mux.HandleFunc("GET /api/projects/{id}/backup", projects.HandleBackupState)
	mux.HandleFunc("POST /api/projects/{id}/backup/enable", projects.HandleEnableBackup)
	mux.HandleFunc("POST /api/projects/{id}/keep", projects.HandleSetKeep)

	// Saved prompts: user-owned starter chips for the New Chat surface.
	mux.HandleFunc("GET /api/saved-prompts", savedprompts.HandleList)
	mux.HandleFunc("POST /api/saved-prompts", savedprompts.HandleCreate)
	mux.HandleFunc("PUT /api/saved-prompts/reorder", savedprompts.HandleReorder)
	mux.HandleFunc("PATCH /api/saved-prompts/{id}", savedprompts.HandleUpdate)
	mux.HandleFunc("DELETE /api/saved-prompts/{id}", savedprompts.HandleDelete)

	// Consent-gated cleanup ("tidy up").
	mux.HandleFunc("GET /api/tidyup", tidyup.HandleList)
	mux.HandleFunc("GET /api/tidyup/count", tidyup.HandleCount)
	mux.HandleFunc("POST /api/tidyup/cleanup", tidyup.HandleCleanup)
	mux.HandleFunc("POST /api/tidyup/keep", tidyup.HandleKeep)

	// File manager
	mux.HandleFunc("GET /api/browse", files.HandleBrowse)
	mux.HandleFunc("GET /api/files/view", files.HandleFileView)
	mux.HandleFunc("POST /api/files/save", files.HandleFileSave)
	mux.HandleFunc("POST /api/files/directory", files.HandleCreateDirectory)
	mux.HandleFunc("POST /api/files/delete", files.HandleFileDelete)
	mux.HandleFunc("POST /api/files/rename", files.HandleFileRename)
	mux.HandleFunc("POST /api/files/move", files.HandleFileMove)
	mux.HandleFunc("POST /api/files/duplicate", files.HandleFileDuplicate)
	mux.HandleFunc("POST /api/files/upload", files.HandleFileUpload)
	mux.HandleFunc("GET /api/files/download", files.HandleFileDownload)
	mux.HandleFunc("GET /api/files/raw", files.HandleFileRaw)

	// Git sync (status/pull/push). All three operate on a path supplied by
	// the client; status is best-effort and returns is_repo=false outside a
	// repo, pull/push 400 if the path isn't inside a repo.
	mux.HandleFunc("GET /api/git/status", gitops.HandleStatus)
	mux.HandleFunc("GET /api/git/changes", gitops.HandleChanges)
	mux.HandleFunc("POST /api/git/pull", gitops.HandlePull)
	mux.HandleFunc("POST /api/git/push", gitops.HandlePush)
	mux.HandleFunc("POST /api/git/fetch", gitops.HandleFetch)
	mux.HandleFunc("POST /api/git/commit", gitops.HandleCommit)
	mux.HandleFunc("POST /api/git/clone", gitops.HandleClone)
	mux.HandleFunc("GET /api/git/repos", gitops.HandleDiscoverRepos)

	// Model providers. One registry endpoint replaces the per-provider
	// status routes for discovery purposes: the Accounts modal, the model
	// picker and the connected-state banner all read this list, so a
	// config-declared provider surfaces everywhere without new routes. The
	// built-ins keep their bespoke sign-in routes below.
	mux.HandleFunc("GET /api/providers", providers.HandleList)
	mux.HandleFunc("POST /api/providers/probe", providers.HandleRefreshReachability)
	mux.HandleFunc("POST /api/providers/{id}/probe", providers.HandleProbe)
	mux.HandleFunc("POST /api/providers/{id}/signout", providers.HandleSignOut)
	mux.HandleFunc("POST /api/providers/{id}/key", providers.HandleSetKey)
	mux.HandleFunc("DELETE /api/providers/{id}/key", providers.HandleRemoveKey)

	// OpenCode (github-copilot provider) sign-in — drives `opencode auth
	// login --provider github-copilot` over a PTY (device flow). This is the
	// only sign-in flow; it authenticates opencode's github-copilot provider.
	mux.HandleFunc("GET /api/opencode/auth/status", opencodeauth.HandleStatus)
	mux.HandleFunc("POST /api/opencode/auth/login/start", opencodeauth.HandleLoginStart)
	mux.HandleFunc("GET /api/opencode/auth/login/info", opencodeauth.HandleLoginInfo)
	mux.HandleFunc("POST /api/opencode/auth/login/cancel", opencodeauth.HandleLoginCancel)

	// GitHub MCP token management. Lets the user supply a properly-scoped
	// Personal Access Token so the GitHub MCP server can read/write PRs, issues,
	// and repos (the login token only carries read:user).
	mux.HandleFunc("GET /api/github/pat/status", opencodeauth.HandleGitHubPATStatus)
	mux.HandleFunc("POST /api/github/pat", opencodeauth.HandleSetGitHubPAT)
	mux.HandleFunc("DELETE /api/github/pat", opencodeauth.HandleClearGitHubPAT)

	// Vertex AI (google-vertex provider) sign-in — "Sign in with Google Cloud".
	// Drives `gcloud auth application-default login --no-launch-browser` over
	// stdin/stdout pipes: surfaces the consent URL and forwards the verification
	// code the user pastes back, producing Application Default Credentials.
	mux.HandleFunc("GET /api/vertex/auth/status", vertexauth.HandleStatus)
	mux.HandleFunc("POST /api/vertex/auth/login/start", vertexauth.HandleLoginStart)
	mux.HandleFunc("GET /api/vertex/auth/login/info", vertexauth.HandleLoginInfo)
	mux.HandleFunc("POST /api/vertex/auth/login/code", vertexauth.HandleLoginCode)
	mux.HandleFunc("POST /api/vertex/auth/login/cancel", vertexauth.HandleLoginCancel)

	// Cross-session retrieval. Read-only over the local chat database:
	// list past chats, full-text search their messages, read one back. These
	// are the HTTP side of the `recall` MCP server (see ensureRecallMCP), which
	// is a thin JSON-RPC bridge onto exactly these three endpoints -- the agent
	// and the browser therefore see the same data through the same code.
	mux.HandleFunc("GET /api/recall/sessions", recall.HandleList)
	mux.HandleFunc("GET /api/recall/search", recall.HandleSearch)
	mux.HandleFunc("GET /api/recall/read", recall.HandleRead)

	// MCP server management (stored in opencode.json). opencode handles MCP
	// OAuth automatically, so there is no custom OAuth flow here.
	mux.HandleFunc("GET /api/mcp/servers", mcp.HandleList)
	mux.HandleFunc("GET /api/mcp/servers/{name}", mcp.HandleGet)
	mux.HandleFunc("POST /api/mcp/servers", mcp.HandleAdd)
	mux.HandleFunc("PUT /api/mcp/servers/{name}", mcp.HandleUpdate)
	mux.HandleFunc("DELETE /api/mcp/servers/{name}", mcp.HandleRemove)
	mux.HandleFunc("PUT /api/mcp/servers/{name}/enabled", mcp.HandleSetEnabled)
	mux.HandleFunc("PUT /api/mcp/servers/{name}/key", mcp.HandleSetKey)
	mux.HandleFunc("DELETE /api/mcp/servers/{name}/key", mcp.HandleRemoveKey)
	// Check a server before adding it. Never mutates; see the SSRF
	// constraints documented at the top of internal/mcp/probe.go.
	mux.HandleFunc("POST /api/mcp/probe", mcp.HandleProbe)
	// Batched auth status for every server in one response, so the
	// connectors modal needs one request rather than one per server.
	mux.HandleFunc("GET /api/mcp/status", mcpauth.HandleAllStatus)
	// MCP OAuth sign-in/out (drives `opencode mcp auth|logout <name>`).
	mux.HandleFunc("GET /api/mcp/servers/{name}/status", mcpauth.HandleStatus)
	mux.HandleFunc("POST /api/mcp/servers/{name}/auth/start", mcpauth.HandleAuthStart)
	mux.HandleFunc("GET /api/mcp/servers/{name}/auth/info", mcpauth.HandleAuthInfo)
	mux.HandleFunc("POST /api/mcp/servers/{name}/auth/cancel", mcpauth.HandleAuthCancel)
	mux.HandleFunc("POST /api/mcp/servers/{name}/auth/callback", mcpauth.HandleAuthCallback)
	mux.HandleFunc("POST /api/mcp/servers/{name}/logout", mcpauth.HandleLogout)

	// Health
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"status":         getStatus(),
			"opencode_ready": opencodeReady.Load(),
		}
		if m := migrationState.Load(); m != nil {
			resp["migration"] = m
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	// Embedded HTML
	mux.HandleFunc("GET /", handleIndex)
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		// PWA service worker: never cache the worker script itself so updates
		// are picked up promptly, and explicitly allow control over the root.
		if r.URL.Path == "/sw.js" {
			w.Header().Set("Service-Worker-Allowed", "/")
			w.Header().Set("Cache-Control", "no-cache")
		}
		// Manifest: we want the manifest itself to be refetched on app
		// updates, but it's cheap to revalidate.
		if r.URL.Path == "/manifest.webmanifest" {
			w.Header().Set("Cache-Control", "no-cache")
		}
		// Serve static files from embedded html/ directory
		r.URL.Path = "/html" + r.URL.Path
		http.FileServerFS(htmlFS).ServeHTTP(w, r)
		return
	}
	data, err := htmlFS.ReadFile("html/index.html")
	if err != nil {
		http.Error(w, "index.html not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func handleVersion(w http.ResponseWriter, r *http.Request) {
	opencodeVersion := opencodeinstaller.VersionString(config.OpencodeBin)
	if opencodeVersion == "" {
		opencodeVersion = "opencode (not installed)"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"app":       version,
		"buildTime": buildTime,
		"version":   opencodeVersion,
	})
}

func handleModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// OpenCode is the only backend; /api/models returns the opencode
	// github-copilot model list (provider/model identifiers).
	opencodeModelsMu.RLock()
	models := make([]string, len(opencodeModels))
	copy(models, opencodeModels)
	opencodeModelsMu.RUnlock()

	fmt.Fprintf(w, "[")
	for i, m := range models {
		if i > 0 {
			fmt.Fprintf(w, ",")
		}
		fmt.Fprintf(w, "%q", m)
	}
	fmt.Fprintf(w, "]")
}

func handleAgents(w http.ResponseWriter, r *http.Request) {
	list := config.ListAgents()
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(list)
	w.Write(b)
}

// handleSkills lists the skills available to a session's workspace, for the
// composer's skill picker. The enumeration is opencode's own, proxied
// rather than reimplemented: opencode already scans every config tier it
// resolves, so the list matches what the model is told about in
// <available_skills> — including the platform-owned tier that a Go-side scanner
// would be liable to miss.
//
// A failure to reach opencode is reported as `available: false` rather than an
// error status: the picker must still open, but "we could not ask" and "there
// are none" are different things and the browser has to be able to tell them
// apart.
func handleSkills(w http.ResponseWriter, r *http.Request) {
	workdir := skillsWorkdir(
		strings.TrimSpace(r.URL.Query().Get("session_id")),
		strings.TrimSpace(r.URL.Query().Get("workdir")),
	)
	list, available := chat.ListSkills(r.Context(), workdir)
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(map[string]any{"skills": list, "available": available})
	w.Write(b)
}

// skillsWorkdir decides which directory a skills request is about. A session's
// own workspace wins when it has one, because skills resolve per directory and
// the picker must show what this chat would actually get; an explicit workdir
// is the fallback for a chat that does not exist yet, and the default
// workspace the last resort.
func skillsWorkdir(sessionID, workdir string) string {
	if sessionID != "" {
		if cfg, err := db.GetSessionConfig(sessionID); err == nil && strings.TrimSpace(cfg.Workdir) != "" {
			return cfg.Workdir
		}
	}
	if workdir != "" {
		return workdir
	}
	return config.Workspace
}

// registerAgentBuilderRoutes wires the Agent Builder management API. The
// store writes to OpenCode's user agent dir; shipped defaults (tracked by the
// defaults installer's SHA-256 manifest) are excluded from the user's list and
// protected from edit/delete.
func registerAgentBuilderRoutes(mux *http.ServeMux) {
	store := &agents.Store{
		Dir: config.OpencodeAgentsDir,
		ShippedFn: func() map[string]bool {
			return defaults.ShippedAgentFilenames(opencodeConfigDir())
		},
	}
	(&agents.Handlers{
		Store: store,
		ModelsFn: func() []string {
			opencodeModelsMu.RLock()
			defer opencodeModelsMu.RUnlock()
			out := make([]string, len(opencodeModels))
			copy(out, opencodeModels)
			return out
		},
		AssistFn: runAgentAssist,
		ReloadFn: chat.ReloadServerAgents,
	}).RegisterRoutes(mux)
}

// agentAssistTimeout bounds a single Agent Builder writing-assist turn.
const agentAssistTimeout = 90 * time.Second

// runAgentAssist executes one synchronous, tool-free model turn for the Agent
// Builder writing assists (prompt polish, description improve). It runs a fresh,
// unpersisted opencode turn, accumulates the assistant's text, and returns it.
func runAgentAssist(ctx context.Context, prompt string) (string, error) {
	cfg := config.DefaultSessionConfig()
	cfg.Backend = config.NormalizeBackend(cfg.Backend)
	cfg.Yolo = true
	cfg.Label = "Agent Builder assist"

	ctx, cancel := context.WithTimeout(ctx, agentAssistTimeout)
	defer cancel()

	ch, proc := chat.RunStream(chat.NewUUID(), prompt, cfg)
	defer func() {
		if proc != nil {
			proc.Kill()
		}
	}()

	var text strings.Builder
	var firstErr string
	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("assist turn timed out")
		case ev, ok := <-ch:
			if !ok {
				if text.Len() == 0 && firstErr != "" {
					return "", fmt.Errorf("assist failed: %s", firstErr)
				}
				return text.String(), nil
			}
			switch ev.Kind {
			case "chunk":
				text.WriteString(ev.Text)
			case "error":
				if firstErr == "" {
					firstErr = ev.Text
				}
			}
		}
	}
}

func handleModelStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"models":     db.GetModelStats(),
		"summary":    db.GetUsageSummary(),
		"daily_cost": db.GetDailyCost(7),
	})
}
