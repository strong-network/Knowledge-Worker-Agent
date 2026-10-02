# AGENTS.md

Guidance for coding agents and automated security scanners working in this repository.
Every command, path, and version below was verified against the repo at authoring time. If
something here ever drifts from the code, trust the code.

---

## 1. Project overview

**Knowledge Worker Agent**: a chat web app that runs the open-source
[opencode](https://github.com/anomalyco/opencode) agent inside a Citrix SecurSpaces workspace.
A Go HTTP server runs opencode for each chat turn, translates its output into events, and streams
them to a Vue 3 single-page app over Server-Sent Events (SSE).

- **Ships as:** one static Go binary (`knowledge-worker-agent`) with the built frontend embedded via
  `//go:embed`. No CGO, no shared libraries. The release image installs it as
  `/usr/bin/knowledge-worker-agent`.
- **Go module:** `github.com/strong-network/Knowledge-Worker-Agent`, **Go 1.27** (`go.mod`), with a
  toolchain floor of `go1.27.1`.
- **Only direct Go dependency:** `modernc.org/sqlite v1.34.5` (pure-Go SQLite); everything else
  is the standard library. `NOTICE` lists what the binary contains.
- **Version:** the binary reports `<VERSION file>-<short git SHA>` (for example `1.2.0-a1b2c3d`),
  injected at build time (`-X main.version`); surfaced via `GET /api/version` (`app` field) and
  shown in the Settings menu. `frontend/package.json` stays at `1.0.0`.
- **Runtime prerequisite:** opencode. If it's missing, the server starts anyway and installs the
  pinned release in the background (see §4).
- **Documentation:** on docs.citrix.com, linked from `README.md`. This repository keeps no copy.
- **Licence:** Apache-2.0 (`LICENSE`). Source files carry a copyright and SPDX header.

---

## 2. Repository map

Single-module repo; **all paths are relative to the repository root**, which holds `go.mod` and the
whole product.

| Path | What it is | Notes |
|------|-----------|-------|
| `cmd/server/` | Main entry point: `main.go` (route registration, bootstrap, model discovery), `health.go` (probe subcommand) | Embeds `cmd/server/html/` |
| `cmd/server/html/` | **Generated** — Vite build output of the frontend | Do not hand-edit (see §9) |
| `internal/defaults/` | Embedded bundles installed at startup into `~/.config/opencode`, in **every** workspace (not gated on `KWA_CENTRAL_CONFIG`): `skills/` (the pdf/docx/pptx/xlsx document skills — the capability floor for reading an attached file) and `plugin/` (OpenCode plugins that keep chat working; `pdf-guard.js` drops attachments the provider can't accept, which would otherwise poison a session's history permanently). Both are platform-owned: always overwritten, never manifest-tracked. The old content bundle (agents, product context, `AGENTS.md`, product-specific skills) is **retired**: central configuration provides that now, and `RetireLegacyDefaults` removes it from workspaces that already have it, preserving user-edited files | `//go:embed all:skills` + `all:plugin` |
| `internal/mcp/` | MCP server management: the `mcp` block of opencode's `opencode.json`. opencode handles MCP OAuth itself (`internal/mcpauth` drives its commands) | **Security-relevant**: tokens, external IdPs |
| `internal/chat/` | Chat backend lifecycle (`RunStream`), opencode JSONL parser (`ParseOpencodeJSONL`), `opencode serve` event translation (`server_stream.go`), process registry, stream buffering | **Security-relevant**: process exec |
| `internal/opencodeserver/` | Server mode: supervises long-lived `opencode serve` processes, one per working folder, and adapts their HTTP/SSE events | **Security-relevant**: process exec |
| `internal/opencodeinstaller/`, `internal/opencodeembed/` | Installs the pinned opencode release, verified by SHA-256; the release binary embeds a fallback copy | **Security-relevant**: supply chain |
| `internal/opencodeauth/`, `internal/vertexauth/`, `internal/providers/` | Model provider sign-in (GitHub Copilot device flow, Google Cloud), API keys, and which providers and preset models the workspace offers | **Security-relevant**: credentials, written to opencode's credential store |
| `internal/sessions/` | Session & chat HTTP handlers, SSE dispatch loop, prompt queue, answer forwarding | |
| `internal/files/` | File-manager handlers: browse/view/save/upload/download/rename/move/delete | **Security-relevant**: filesystem access |
| `internal/workdirs/` | The only gate for deleting chat folders: session delete, tidy-up and sweeps all go through it | **Security-relevant**: deletes files |
| `internal/git/` | Git status/changes/pull/push/commit/clone over a client-supplied path | **Security-relevant**: exec `git` |
| `internal/mcpauth/` | Drives opencode's MCP OAuth commands (`opencode mcp auth`, `logout`, `list`) | **Security-relevant**: external identity providers |
| `internal/materializer/` | Central configuration: at startup, clones or reads the configuration repository and writes the project's agents, skills, context, connectors, providers and vocabularies into a platform-owned opencode config folder | **Security-relevant**: exec `git`, writes config |
| `internal/workspaceapps/` | Chat sharing: client for the workspace sidecar's Workspace App routes (`/var/strong-network/socks/sidecar-ipc`). Calls carry the owner's platform token, read per request from the `cloud_editor_user_token` header the platform proxy injects; `GET /api/share/platform` is a read-only probe of that path | **Security-relevant**: handles the owner's platform credential — never store, log or return it |
| `internal/guest/` | Chat sharing: the guest surface — a **separate listener** (`127.0.0.1:8766`) with its own mux, reached by coworkers through the Workspace App. Only its own routes exist; every session route 404s unless the chat is shared; POSTs require `Origin` to equal the share URL's origin; turn frames pass an allow-list (`Frame`). With the owner's per-share toggles on, guests also answer permission requests (once or deny) and reach the chat's folder through `os.Root` — no delete/rename/move, hidden paths refused, home folder never — and guest prompts have a per-chat budget | **Security-relevant**: the one surface exposed beyond the owner. Must never import `internal/workspaceapps` or read the owner token (a test enforces this) |
| `internal/obsidianinstaller/` | Installs the Obsidian MCP server via `npm install -g obsidian-mcp@<PinnedVersion>` | **Security-relevant**: supply chain |
| `internal/voice/` | Dictation: `/api/voice/dictations` routes (owner listener only), audio held in memory, and the `whisper-server` engine it runs on loopback from a fresh `0700` directory with none of Knowledge Worker Agent's environment, tied to Knowledge Worker Agent's lifetime (`Pdeathsig`) and force-stopped when stuck. Reads the centrally assigned `vocabulary` artifacts for the engine's hint and corrections | **Security-relevant**: process exec, audio. Never log or persist audio or transcripts; the engine loads CPU code from its working directory, so that directory must stay private and empty |
| `internal/config/` | Env-var config (`Init`), `SessionConfig`, `BuildOpencodeArgs`, agent discovery | Maps session config → opencode CLI flags |
| `internal/db/` | SQLite persistence (sessions, messages, stats), idempotent migrations, FTS5 message index (`fts.go`) and the read-only retrieval queries (`recall.go`) | Parameterized queries only |
| `internal/recall/` | Cross-session retrieval: `GET /api/recall/{sessions,search,read}`. SELECT-only | Shapes here are the durable contract, not the tables |
| `internal/mcprecall/` | The `recall` MCP server: JSON-RPC 2.0 over stdio (newline-delimited), bridged to the recall endpoints over loopback HTTP. Holds no DB handle and issues only GETs | Registered **enabled** (first-party); see `cmd/server/mcprecall.go` |
| `frontend/` | Vue 3 + TypeScript + Pinia SPA (Vite). About 60 `.vue` and 40 `.ts` files | Builds into `cmd/server/html/` |
| `examples/config-repo/` | Example central configuration repository | `internal/materializer` tests read it |
| `Assets/` | The robot icon (SVG, and 192 and 512 px PNGs), for documentation and other uses outside the app | Copies of the app's icons in `frontend/public/`: change both together |
| `Makefile`, `Dockerfile.release`, `.github/` | Build tooling, the release image, CI | |

The other packages hold features with no special exposure: `agents` and `agentbuilder` (the
user's own agents), `connectors` (which connectors are on per chat), `daynotes` and `titler`
(background summaries and chat titles), `durable` (the workspace folder convention),
`projects`, `savedprompts`, `scheduledtasks`, `sharing` (the owner's side of chat sharing),
`tidyup` (abandoned chat folders), and `workspaceheartbeat` (keep-alive reports to the platform).

Most security-relevant directories: `internal/files`, `internal/workdirs`, `internal/git`,
`internal/chat`, `internal/mcp`, `internal/guest`, `internal/opencodeinstaller`, `internal/voice`.

---

## 3. Build / test / lint / codegen

All commands run from **the repository root**. Verified against `Makefile`, `Dockerfile.release`,
`frontend/vite.config.ts`, and `frontend/package.json`.

```bash
# Build: builds frontend first, then a static linux/amd64 binary → build/knowledge-worker-agent
make build
# under the hood: cd frontend && npm ci --silent --no-audit --no-fund   (only when the
#                   lockfile changed) && npx vite build
#                 CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w -X main.version=…" \
#                   -trimpath -o build/knowledge-worker-agent ./cmd/server

# Frontend only → output to cmd/server/html/
make frontend                      # npm ci (only if lockfile changed) && npx vite build  (no type-check)
cd frontend && npm run build       # vue-tsc --noEmit && vite build      (type-checks first)
cd frontend && npm run dev         # Vite dev server on :5173, proxies /api → :8765

# Tests (pure Go; NO external services required — see §7)
make test                          # go test ./... -v -count=1
make test-short                    # go test ./... -count=1

# Single package:
go test ./internal/db/ -count=1
# Single test by name:
go test ./internal/chat/ -run TestParseOpencodeJSONL -v

# Lint (this is the entire lint surface — no golangci-lint, no eslint)
make lint                          # go vet ./...

# Run / package
make run                           # executes ./build/knowledge-worker-agent (no rebuild)
make build-and-run
make release                       # docker build -f Dockerfile.release → chat:<VERSION> (RELEASE_IMAGE= renames)
make clean                         # rm -rf build/ && go clean -cache -testcache

# Everything CI runs, in CI's order
make ci
```

**Codegen:** none. There is no protobuf, OpenAPI, or other generated source. The only "generated"
artifact is the Vite build output under `cmd/server/html/` (see §9).

**Frontend type-checking** happens via `vue-tsc --noEmit` inside `npm run build`. Note `make
frontend` / `make build` use `npx vite build` directly and therefore **skip** the type-check —
run `cd frontend && npm run build` (or `npx vue-tsc --noEmit`) to type-check.

**CI is the gate, and it is stricter than `make`.** `.github/workflows/ci.yml` runs `make ci` on
every pull request: frontend `npm ci` + `npm run build` (so it **does** type-check), then
`go vet ./...`, `go build ./...`, `go test ./... -count=1` and `govulncheck ./...`. A type error `make build` happily
skips will still fail the PR, so type-check locally before pushing frontend changes. CI does
not pass `-race`.

---

## 4. Architecture & runtime topology

Single process, plus child processes:

```
Browser (Vue SPA)  ──HTTP/SSE──>  Go server (cmd/server, default 127.0.0.1:8765; release image 0.0.0.0)
                                      │
                                      ├─ spawns: opencode run --format json … -- <prompt>  (per chat turn, default mode)
                                      ├─ or runs: opencode serve …           (server mode, one per working folder)
                                      ├─ spawns: opencode auth / models / mcp …  (sign-in, models, MCP OAuth)
                                      ├─ spawns: git <verb> …                (source control, cwd = client path)
                                      ├─ spawns: npm install …               (Obsidian MCP install, startup)
                                      ├─ spawns: whisper-server …            (dictation engine, loopback only)
                                      ├─ reads/writes: SQLite DB             (modernc.org/sqlite, WAL)
                                      └─ HTTP out: github.com / npm / model providers / MCP servers
Coworkers ──Workspace App──> guest listener (127.0.0.1:8766), shared chats only
```

- **Startup is non-blocking.** `main()` starts the HTTP listener immediately, then a background
  `bootstrap()` goroutine installs or verifies opencode, installs the Obsidian MCP and the bundled
  skills and plugins, applies central configuration, registers default MCP servers, and discovers
  models. Chat endpoints return **503** (via `requireOpencodeReady`) until opencode is verified.
- **Chat turn, default mode:** `POST /api/chat` → `sessions.HandleChat` → `chat.RunStream` runs
  `opencode run` in the session's folder, reads stdout line by line, parses each line with
  `chat.ParseOpencodeJSONL` into typed `Event`s, and fans them to the browser as SSE frames
  (`chunk`, `tool_call`, `tool_done`, `question`, `usage`, `error`, `done`, …). The opencode
  session ID is stored, so follow-ups pass `--session`.
- **Server mode** (`KWA_OPENCODE_USE_SERVER=true`): turns go to a long-lived `opencode serve` over
  HTTP/SSE (`internal/opencodeserver`), translated by `translateServerEvent`
  (`internal/chat/server_stream.go`). Only this mode can ask before the agent acts: a
  `permission` event, answered with `POST /api/sessions/{id}/permission`.
- **Questions:** `opencode run` gets `/dev/null` as stdin, so it can't take an answer mid-turn;
  the frontend sends the answer as the next message.
- **Persistence:** SQLite at `~/Knowledge_Worker_Agent/.system/knowledge-worker-agent.db`
  (`layout.Database`; `KWA_HOME` moves the folder), single open connection, WAL mode,
  `foreign_keys=ON`. On startup, before migrations run, the server writes a `VACUUM INTO`
  snapshot to `.system/backups/` (`layout.Backups`) and keeps the newest 5, at most one per
  24h (`internal/db/snapshot.go`). Snapshots sit on the same volume: they protect against a bad
  migration or an accidental delete, not against loss of the workspace.

---

## 5. SECURITY-RELEVANT CONTEXT (read this, scanner)

This is a **single-user, single-tenant workspace tool** intended to run inside one developer's
isolated cloud workspace/container and drive an AI agent on that user's behalf. Several behaviors
that look dangerous in a multi-tenant web app are **intentional** here. Prioritize findings that
show **untrusted input reaching a sink along a reachable path**, and flag **deviations** from the
patterns documented here rather than the documented patterns themselves.

### Trust boundaries
- **Browser → HTTP API (primary boundary).** The browser is the untrusted side. **There is no
  authentication, authorization, session, CSRF, or Origin checking on the HTTP API.** Every
  `/api/*` route is reachable by anyone who can reach the listening port. The server binds
  `127.0.0.1:8765` by default; the release image sets `KWA_HOST=0.0.0.0`, because
  the platform connects over `::1` as well. The security model assumes the port is reachable **only** by the
  single owning user (network isolation provided by the surrounding workspace platform), not by
  the open internet. `requireOpencodeReady` gates *readiness*, not identity.
- **Coworkers → guest listener.** Chat sharing adds a second, separate listener on
  `127.0.0.1:8766` (`internal/guest`), which the platform's Workspace App exposes to coworkers
  the owner picked. It serves shared chats only and requires a matching `Origin` on writes.
- **Server → opencode / git / npm / whisper-server (child processes).** All process execution
  uses `exec.Command` with **argument vectors — never a shell** (`sh -c`), so classic shell
  injection via prompt or arguments does not apply. The chat prompt is passed as the last
  argument, after `--`, so a prompt that starts with `-` can't become a flag.
- **Server → external HTTP** (GitHub, npm, model providers, MCP HTTP endpoints) — see
  integrations below.

### Where authN/authZ is enforced
- **It isn't, at the HTTP layer.** There is no auth choke point/middleware in
  `cmd/server/main.go`; routes are registered directly on a stdlib `http.ServeMux`. Treat the
  whole API as unauthenticated-by-design. *Any* finding that assumes per-request user identity is
  a false positive — but **the absence of auth on a network-exposed bind is itself the main thing
  to reason about** when assessing exposure.
- "Sign-in" in this codebase means **model provider sign-in** (`internal/opencodeauth`,
  `internal/vertexauth`): the GitHub Copilot device flow and Google Cloud sign-in, run through
  opencode. It authenticates opencode *to the provider*, not clients *to the server*.

### Secrets & crypto
- **Model provider credentials** (the Copilot token, API keys) are stored by opencode in
  `~/.local/share/opencode/auth.json`; `internal/opencodeauth` writes API keys there in
  opencode's format. A GitHub personal access token for the GitHub MCP server comes from
  `COPILOT_GITHUB_TOKEN`, `GH_TOKEN` or `GITHUB_TOKEN`, or one the user stores; the Copilot
  sign-in token is deliberately never used for it.
- **MCP OAuth tokens:** opencode runs the OAuth flow and stores the tokens; `internal/mcpauth`
  only drives `opencode mcp auth` and `opencode mcp logout`.
- **MCP personal API keys:** one file per server in `~/Knowledge_Worker_Agent/.system/mcp-keys/`
  (dir 0700, files 0600, written atomically), read by opencode through a `{file:}` header the
  materializer writes. Never in `opencode.json`, API responses or logs. A referenced key file
  must always exist (empty when no key): a missing one makes opencode reject its whole config.
- **Randomness:** `crypto/rand` for UUIDs (`chat.NewUUID`) and other identifiers — no
  `math/rand` for security-sensitive values. (`math/rand` appears only for non-security
  random-name shuffling in `internal/files`.)

### Sensitive data stores & external integrations
- **SQLite** (`layout.Database`): chat sessions, message history, usage stats. No credentials are
  stored in the DB.
- **GitHub:** release downloads of the pinned opencode (`internal/opencodeinstaller`) and the
  Copilot device flow (`internal/opencodeauth`).
- **Model providers:** reached by opencode, with the credentials above.
- **npm registry:** `npm install -g` for the Obsidian MCP server (`internal/obsidianinstaller`).
- **Arbitrary MCP servers:** the client can register stdio servers (a command + args that
  opencode will later run) or remote servers (a URL) via `POST /api/mcp/servers`.
  `POST /api/mcp/probe` checks one before it's added: it fetches a URL the browser chose, with
  the SSRF limits documented at the top of `internal/mcp/probe.go`, or runs a stdio server's
  command.

### Exec / filesystem / SQL / deserialization sinks
- **Process exec (`exec.Command`, arg-vector, no shell):**
  `internal/chat/chat.go` (`opencode run`), `internal/opencodeserver/supervisor.go`
  (`opencode serve`), `internal/titler` and `internal/daynotes` (`opencode run` for titles and
  notes), `internal/opencodeauth`, `internal/vertexauth` and `internal/mcpauth` (`opencode auth`,
  `models`, `mcp`), `cmd/server/providerprobe.go` (opencode), `internal/git/git.go` and
  `internal/materializer/repo.go` (`git`), `internal/mcp/probe.go` (a client-supplied stdio
  server command), `internal/obsidianinstaller` (`npm`, `node`), `internal/opencodeinstaller`
  (the binary's `--version`, `ldd`), and `internal/voice/engine.go` (`whisper-server` on
  `127.0.0.1`, and `--help` for the availability check; fixed arguments, absolute paths,
  minimal environment).
  - opencode runs with **`--dangerously-skip-permissions` by default**
    (`DefaultSessionConfig().Yolo == true` → all tools auto-approved, no per-action
    confirmation). **This is intentional** — the product's purpose is to run an autonomous agent
    in an isolated workspace. Server mode (`KWA_OPENCODE_USE_SERVER=true`) adds per-action approval. The `SessionConfig` allow/deny
    tool/URL/path knobs in `internal/config/config.go` are the intended control surface.
  - Git runs with fixed sub-command verbs; the commit message is passed as `-m <msg>` (separate
    arg, not interpolated). `cwd`/repo path comes from the client.
- **Filesystem (`internal/files`):** handlers operate on **client-supplied absolute paths with no
  workspace jail** — `filepath.Abs(path)` then read/write/delete/upload/download anywhere the
  process user can reach (browse falls back to the workspace only when the path is missing/invalid).
  This is **intentional** (it is a file manager for the user's own workspace), but it means
  path-traversal-style reachability is real: a reachable path here is bounded only by OS
  permissions of the server's user, not by the base folder (`layout.Workspace`). Worth confirming exposure
  assumptions; not worth flagging as a traversal "bug" in isolation.
- **SQL (`internal/db`):** all queries use parameterized placeholders (`?`). Migration
  `ALTER TABLE` statements are hardcoded string constants (not built from input). No string-built
  SQL — SQL injection findings here are false positives unless a new query interpolates input.
- **Deserialization:** only `encoding/json` into typed/`map[string]any` structures (HTTP bodies,
  opencode JSONL, config files). No gob/YAML/pickle/native deserialization.

### Supply chain
- `internal/opencodeinstaller` installs only `PinnedVersion` of opencode, and only a tarball whose
  **SHA-256** matches `pinnedSHA256` (taken from the GitHub release API's asset digests; opencode
  publishes no checksum files). A download that doesn't match is refused, and the embedded copy
  in the release binary is used only if it matches too. `Dockerfile.release` pins the same
  version and digests and checks them with `sha256sum -c`; a test keeps the two in agreement.
- `internal/obsidianinstaller` installs `obsidian-mcp@<PinnedVersion>` with npm, which checks the
  registry's integrity hash. opencode itself installs its plugin runtime (`@opencode-ai/plugin`)
  from npm the first time it starts; that download is opencode's, not ours.

### Existing security tooling
- **There is a CI workflow**, `.github/workflows/ci.yml`, which runs `make ci` on **every
  pull request** and on push to `master`: it builds the frontend
  (`npm ci` + `npm run build`, which type-checks via `vue-tsc`), then `go vet ./...`,
  `go build ./...`, `go test ./... -count=1` and `govulncheck ./...`. Note it builds the SPA *before* the Go
  steps, because `cmd/server/html/` is generated and `//go:embed` needs it to exist.
- **`govulncheck` is the only security tooling.** It fails on a known vulnerability the code
  reaches, in a module or in the Go standard library. There is no SAST, no secret scanning, no
  linter beyond `go vet`, and no `-race`. So a finding here other than a known Go vulnerability
  is **not** duplicated by the pipeline — only a compile error, a vet diagnostic, a Go test
  failure or a TypeScript type error would already have been caught.
- **Dependabot alerts are active** on this repository, and `.github/dependabot.yml` asks for
  weekly version-update pull requests, one grouped PR each for Go modules, npm (`frontend/`)
  and GitHub Actions. Treat third-party dependency-version findings as likely already known;
  everything else is not.

### What to prioritize / de-prioritize
- **Prioritize:** untrusted (browser/MCP/external-HTTP) input that reaches a sink via a reachable
  path; deviations from the documented exec/SQL/file patterns (e.g. a new `sh -c`, a query built
  with `fmt.Sprintf`, a newly added unauthenticated mutating endpoint that writes outside expected
  areas); unverified-download or token-handling regressions.
- **De-prioritize (intentional by design):** the unauthenticated API surface, auto-approved agent
  execution, the un-jailed file manager, and arbitrary MCP/command registration — these are the
  product's purpose in an isolated single-user workspace. Flag them only if the deployment/exposure
  assumption (single user, network-isolated port) appears violated.

---

## 6. Conventions

- **No CGO, ever.** Use `modernc.org/sqlite` (pure Go); do not introduce `mattn/go-sqlite3`.
- **Standard library only** (plus `modernc.org/sqlite`): no third-party HTTP router, UUID library,
  or test framework. Routing is stdlib `http.ServeMux` with method+pattern routes
  (e.g. `"POST /api/chat"`); path params via `r.PathValue(...)`.
- **`config.SessionConfig` is the canonical per-session settings struct.** A new opencode CLI flag
  means: add a field (with JSON tag) in `internal/config/config.go`, append it in
  `BuildOpencodeArgs()`, default it in `DefaultSessionConfig()` if needed, add a `config_test.go`
  case, then mirror the field in `frontend/src/api/index.ts` and the relevant settings component.
- **Backend output becomes internal `Event`s in two places:** `chat.ParseOpencodeJSONL()` for
  per-turn `opencode run`, and `translateServerEvent()` (`internal/chat/server_stream.go`) for
  `opencode serve`. New event types go in both *and* in the SSE dispatch loop
  (`internal/sessions`) *and* in the frontend store (`frontend/src/stores/chat.ts`).
- **A new API endpoint:** handler in the relevant `internal/<pkg>`, registered in
  `registerRoutes()` in `cmd/server/main.go`, client function in `frontend/src/api/index.ts`.
- **A schema change:** a migration in `migrate()` in `internal/db/db.go` (`CREATE TABLE IF NOT
  EXISTS` / hardcoded `ALTER TABLE`), CRUD with null-slice handling, a test in `db_test.go`.
  Missing JSON fields unmarshal to zero values, so old rows keep working.
- **Response helpers:** `writeSSE()` flushes after every frame; each package has its own
  `writeJSON()`. Updating a session's config merges the stored JSON with the new fields rather
  than replacing it.
- **Null-slice safety:** handlers/DB functions return `[]T{}` (never `nil`) so JSON encodes as `[]`.
- **Backend↔frontend name differences** (translated in `frontend/src/api/index.ts`):
  `workdir`↔`workspace`, `label`↔`name`, and the types `SessionListItem`↔`Session`,
  `ChatMessage`↔`Message`.
- **Frontend stack:** Vue 3 (composition API) + TypeScript (strict) + Pinia + Vite; rendered
  markdown is sanitized with **DOMPurify** (+ `marked`, `highlight.js`, CodeMirror 6).
- **The SPA has two roots, chosen in `frontend/src/main.ts` by the `?doc=` query parameter:**
  `App.vue` (the chat app) and `DocWindowApp.vue` (the pop-out document window). There is
  no router — a query parameter is used because the Go handler serves `index.html` only for `/`
  and 404s every other path. The document window deliberately skips the chat app's bootstrap
  (which loads models/agents/sessions and creates a session if none exist) and the service-worker
  registration (whose `controllerchange` reload would discard unsaved edits). The two windows
  coordinate over a `BroadcastChannel` in `frontend/src/composables/useDocWindow.ts`.
- **`DocumentView.vue` is the single viewer/editor** for an open file, shared by the docked Files
  panel and the document window. New file-type rendering goes there, not in `WorkspaceFilePanel.vue`
  (which owns browsing and *which* file is open, not how it is shown).
- **i18n:** none — there is no internationalization framework in this repo.
- **Dependency/build regen:** `make deps` (`go mod tidy && go mod download`); the frontend is
  rebuilt into `cmd/server/html/` by `make frontend`. There is no proto/codegen step.

---

## 7. Testing

- **Framework:** standard `testing` package only.
- **No external services.** Tests do **not** require Docker, a database container, or network
  access. DB tests open a temp-file SQLite database (in-process, pure Go); HTTP handler tests use
  `net/http/httptest`. Tests must **not** run the real opencode binary — point
  `config.OpencodeBin` at a fake script instead, as the `internal/chat` tests do.
- **Run:** `make test` (all), `go test ./internal/<pkg>/ -count=1` (one package),
  `go test ./internal/<pkg>/ -run <TestName> -v` (one test).
- **Ports:** none needed for tests. At runtime the server listens on `KWA_PORT`
  (default **8765**); the Vite dev server uses **5173** and proxies `/api` + `/healthz` to 8765.

### Environment variables

The [Environment variables](https://docs.citrix.com/en-us/securspaces/knowledge-worker-agent/reference/environment-variables.html)
reference lists every variable the server reads, with its default. When you add or change one,
update that article too. Four are for local development only: `KWA_GUEST_ORIGIN` and
`KWA_SIDECAR_SOCKET` (test chat sharing against a fake sidecar), and `KWA_VOICE_BIN` and
`KWA_VOICE_MODEL` (point dictation at a local engine and model).

Read a setting with `env.Get` (`internal/env`), under its `KWA_` name. A renamed setting lists
its old names there; they're still read, and the log names them at startup. Names that belong to
others keep theirs: `OPENCODE_CONFIG_DIR` is opencode's, and `STRONG_NETWORK_*` and `OWNER_*`
describe the workspace. In tests, clear a setting with `envtest.Clear`, which clears its old
names too.
`KWA_CENTRAL_CONFIG` is the one switch for central management; it's parsed once, in
`config.CentralConfigEnabled`, and `internal/defaults` doesn't read it.

---

## 8. Known gotchas

- **`make frontend` / `make build` skip type-checking** (they call `npx vite build`). Use
  `cd frontend && npm run build` (or `npx vue-tsc --noEmit`) to actually type-check the SPA.
- **Build target is fixed to `linux/amd64`, CGO off.** `make build` always cross-compiles for
  linux/amd64. To run the binary on another OS/arch (e.g. local macOS), build directly:
  `go build -o build/knowledge-worker-agent ./cmd/server`.
- **`make frontend` needs network only when dependencies changed.** The `npm ci` step is keyed on
  `frontend/node_modules/.package-lock.json` and re-runs only if `frontend/package-lock.json` or
  `frontend/package.json` is newer, or if `node_modules` was removed; otherwise the build is
  offline and goes straight to `vite build`. `make build` depends on `make frontend`. The
  embedded `cmd/server/html/` is **generated and git-ignored (not committed)**, so a pure
  `go build ./cmd/server` requires running `make frontend` first to populate it.
- **`make test` uses `go test ./... -v`** — `-v` makes output verbose; use `make test-short` for
  quiet runs.
- **`go.mod` sets a toolchain floor** (`toolchain go1.27.1`), below which Go has known
  standard-library vulnerabilities. With the default `GOTOOLCHAIN=auto`, an older local Go
  downloads and uses 1.27.1; `Dockerfile.release` sets `GOTOOLCHAIN=auto` because the `golang`
  images default to `local`. Keep `go 1.27.0` in `go.mod` unless intentionally bumping — `go mod
  tidy` writes the patch digit and will restore it — and raise
  the toolchain line when `govulncheck` reports a fixed Go release.
- **Background bootstrap:** right after start, `/api/chat` and a few endpoints return 503
  (`requireOpencodeReady`) until opencode is verified — expected, not a bug.
- **The release image** (`Dockerfile.release`) is based on `strongnetwork/cloud_editor_generic:2.3.9`
  and installs the binary as `/usr/bin/knowledge-worker-agent`, with `/usr/bin/chat`, its old name,
  as a link; `HEALTHCHECK` calls `/usr/bin/knowledge-worker-agent health` (which probes `/healthz`).

---

## 9. Generated / do-not-edit paths

- `cmd/server/html/` — **generated** by the Vite build (`frontend/vite.config.ts` `outDir`). Edit
  the source under `frontend/`, then rebuild with `make frontend`. (It is **git-ignored / not
  committed**; run `make frontend` before `go build ./cmd/server`.)
- `frontend/node_modules/`, `build/` — build artifacts (git-ignored).
- `internal/defaults/skills/**` and `internal/defaults/plugin/**` — the only content the binary
  still ships into `~/.config/opencode`. `skills/` holds the four document-handling skills
  (pdf, docx, pptx, xlsx) that let the workspace read an attached file; `plugin/` holds OpenCode
  plugins that run inside opencode and change its behaviour. Both install in every workspace
  (central-config or not), are always overwritten rather than preserved on edit, and are kept out
  of the defaults manifest so `RetireLegacyDefaults` can never remove them. The skills are
  versioned here alongside the application that installs them.
- Agents, product context and product-specific skills are **no longer shipped**. Central
  configuration assigns them; adding them back here would put the same content in every
  workspace on top of whatever an administrator assigned.
- `~/Knowledge_Worker_Agent/.system/` (the database, its backups and the MCP API keys),
  opencode's `opencode.json` and `~/.local/share/opencode/auth.json` —
  **runtime-managed** files written by the server or by opencode; never commit them.

---

## Recommended (not yet created) nested AGENTS.md

This repo is small enough that one root file suffices today. If it grows, consider:
- `frontend/AGENTS.md` — the Vue/TS/Vite toolchain and SSE-client conventions are self-contained
  and differ from the Go backend.
