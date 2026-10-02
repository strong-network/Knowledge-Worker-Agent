# Knowledge Worker Agent for Citrix SecurSpaces

Knowledge Worker Agent gives people outside engineering an AI agent that does real work, inside a
[Citrix SecurSpaces](https://docs.citrix.com/en-us/securspaces/knowledge-worker-agent/) workspace your organization controls. It reads and writes documents,
uses the business tools you connect, and carries out multistep tasks, such as drafting a report
from source documents, preparing a customer briefing, or analyzing a spreadsheet. People work with
it in a chat in the browser, not in a code editor.

It ships as a single Go binary with a Vue 3 frontend embedded, and drives the open-source
[opencode](https://github.com/anomalyco/opencode) agent in the workspace.

> **Not a supported Citrix product.** Knowledge Worker Agent is an open-source example of what you
> can build on SecurSpaces. Citrix doesn't provide support for it. Review it before you use it
> with your own data.

## Documentation

The product documentation is on docs.citrix.com:

| If you want to… | Go to |
| --- | --- |
| Understand what it is and why you'd use it | [Knowledge Worker Agent](https://docs.citrix.com/en-us/securspaces/knowledge-worker-agent/) |
| Give users access in a SecurSpaces workspace | [Give users access to Knowledge Worker Agent](https://docs.citrix.com/en-us/securspaces/knowledge-worker-agent/administer/give-users-access.html) |
| Try it for the first time | [Your first chat](https://docs.citrix.com/en-us/securspaces/knowledge-worker-agent/get-started/your-first-chat.html) |
| Use it day to day | [Use Knowledge Worker Agent](https://docs.citrix.com/en-us/securspaces/knowledge-worker-agent/use/) |
| Set it up for a team, including central configuration | [Administer Knowledge Worker Agent](https://docs.citrix.com/en-us/securspaces/knowledge-worker-agent/administer/) |
| Look up environment variables, slash commands, and storage locations | [Reference](https://docs.citrix.com/en-us/securspaces/knowledge-worker-agent/reference/) |
| Understand the security model | [How Knowledge Worker Agent works](https://docs.citrix.com/en-us/securspaces/knowledge-worker-agent/about/how-knowledge-worker-agent-works.html) |

This README covers building and changing the code. This repository doesn't accept contributions;
see [Contributing](#contributing).

## Requirements

- **A Citrix SecurSpaces workspace.** Knowledge Worker Agent has no sign-in of its own. It relies
  on the platform to decide who can reach the workspace, and on its workspace apps for sharing.
  See [Why it needs SecurSpaces](https://docs.citrix.com/en-us/securspaces/knowledge-worker-agent/about/how-knowledge-worker-agent-works.html#why-it-needs-securspaces)
  and [Workspace requirements](https://docs.citrix.com/en-us/securspaces/knowledge-worker-agent/reference/workspace-requirements.html).
- **opencode.** Installed automatically at a pinned version, checked against its SHA-256, if it
  isn't on `PATH` or set with `KWA_OPENCODE_BIN`.
- **git**, for source control features.
- **Go 1.26 and Node.js**, to build from source.

## Give users access in SecurSpaces

SecurSpaces starts Knowledge Worker Agent and makes it reachable through a **custom access item**
on the workspace, or on the workspace template so every workspace created from it has one. In the
workspace or template, go to **Basic Info** > **Workspace Access**, add a custom access item, and
fill it in:

| Field | Value |
| --- | --- |
| **Name** | `Chat`, or another name |
| **Icon** | [`Assets/icon-512.png`](Assets/icon-512.png). Select **Upload**. `icon-192.png` and `icon.svg` are also in [`Assets`](Assets) |
| **Command** | `/usr/bin/knowledge-worker-agent` |
| **Open Port** | `8765`, or the value of `KWA_PORT` |
| **Health Probe** | Leave empty |
| **Health Timeout** | `0` |

Images built before the binary was renamed have it at `/usr/bin/chat`, which current images keep
as a link. For the full steps, see
[Give users access to Knowledge Worker Agent](https://docs.citrix.com/en-us/securspaces/knowledge-worker-agent/administer/give-users-access.html).

## Container images

The release image is published to GitHub Container Registry and to Docker Hub. The two are the
same image: Docker Hub is a copy of the GHCR build, by digest.

```bash
# Latest release
docker pull ghcr.io/strong-network/knowledge-worker-agent:latest
docker pull strongnetwork/knowledge-worker-agent:latest

# A specific version
docker pull ghcr.io/strong-network/knowledge-worker-agent:1.5.0
docker pull strongnetwork/knowledge-worker-agent:1.5.0
```

The latest version is **1.5.0**. Every published image is tagged with its version, taken from the
[`VERSION`](VERSION) file, and `latest` moves to the most recently published one. Pin the version
tag for anything repeatable, and the digest where you need the exact image. Images are
`linux/amd64`.

## Features at a glance

- Chat with markdown, tool-call details, questions and approvals, a message queue, and attachments
- A **Files** panel with an editor, markdown preview, and a separate document window
- Source control for chats that work in a Git repository
- Connectors (MCP servers), including a built-in catalogue that's registered switched off
- Projects, scheduled tasks, saved prompts, chat sharing, and local dictation
- **Recall**: the assistant can search the user's own past chats, read-only, with day notes
- **Central configuration**: agents, skills, context, connectors, model providers, and dictation
  vocabularies assigned per project from one Git repository. See [`examples/config-repo`](examples/config-repo)
  and [Set up central configuration](https://docs.citrix.com/en-us/securspaces/knowledge-worker-agent/administer/set-up-central-configuration.html).

For how each feature works, see the [documentation](https://docs.citrix.com/en-us/securspaces/knowledge-worker-agent/).

## Quick start for development

```bash
make build
./build/knowledge-worker-agent
```

Open <http://localhost:8765>. Settings are environment variables named `KWA_*`; their older
names still work, with a warning in the log. opencode is the only chat backend.

Running outside a SecurSpaces workspace is for development only. There is no authentication on
the HTTP API, so the port must be reachable only by you. See [Security](#security).

## Build and test

```bash
# Install Go deps + tidy
make deps

# Build the Vue frontend → cmd/server/html (embedded via //go:embed)
# This bundle is generated and NOT committed, so run this before `go build`.
make frontend

# Build the Go binary
make build

# Full pipeline: deps → frontend → test → build
make all

# Run all tests verbose, no cache
make test

# Single test
go test ./internal/git -run TestParsePorcelainZ_RenameConsumesOldPath -v

# Single package
go test ./internal/db/... -v -count=1

# Lint (go vet only — no golangci-lint)
make lint

# Frontend dev server (HMR on port 5173, talks to the Go backend on 8765)
cd frontend && npm install && npm run dev
```

> **Type-checking:** `make frontend` / `make build` run `npx vite build` and **skip** type-checking. Run `cd frontend && npm run build` (or `npx vue-tsc --noEmit`) to type-check the SPA.

### Subcommands

The binary serves the web UI when run with no arguments. It also has two subcommands, both meant to be invoked by something else rather than by hand:

```bash
chat health        # Probe /healthz and exit non-zero if unhealthy (used by Docker HEALTHCHECK)
chat mcp-recall    # Serve the recall MCP server on stdio (spawned by opencode; see Recall above)
```

`mcp-recall` is a subcommand of this binary rather than a separate executable because there is nothing to install — the binary is already on disk. It takes `--url` to point at the running server (defaults to the configured port).

## Configuration

Knowledge Worker Agent is configured with environment variables. For every variable and its
default, see [Environment variables](https://docs.citrix.com/en-us/securspaces/knowledge-worker-agent/reference/environment-variables.html).

## Architecture

```
cmd/server/                Entry point, route registration, model discovery, health probe
  └── html/                Embedded frontend bundle (//go:embed; generated by `make frontend`, not committed)
internal/
  ├── config/              Env-var config, SessionConfig, opencode CLI arg builder
  ├── db/                  SQLite persistence (sessions, messages, queue, stats, full-text index)
  ├── chat/                OpenCode process lifecycle, JSONL parser, prompt queue, backend selection
  ├── opencodeserver/      Long-lived `opencode serve` client/adapter/supervisor (server mode)
  ├── opencodeauth/        GitHub Copilot device-flow sign-in for opencode + model discovery
  ├── opencodeinstaller/   OpenCode CLI auto-install / update
  ├── sessions/            Session / chat HTTP handlers, SSE streaming, permission forwarding
  ├── files/               File manager HTTP handlers
  ├── git/                 git status / changes / pull / push / commit / clone / repos
  ├── mcp/                 MCP server management (opencode.json) + the built-in connector catalogue, registered switched off
  ├── recall/              Cross-session retrieval: list / search / read HTTP handlers
  ├── daynotes/            Day notes: per-(chat, day) summaries and the capped backlog job
  ├── mcprecall/           The `recall` MCP server — JSON-RPC on stdio, bridged to the recall endpoints
  ├── obsidianinstaller/   Obsidian MCP auto-install + vault bootstrap
  └── defaults/            Embedded document skills + opencode plugins, installed at startup
frontend/                  Vue 3 + TypeScript + Pinia SPA
```

### How it works

1. Server boots and starts listening immediately; a background `bootstrap()` goroutine does the rest.
2. In parallel, `opencodeinstaller.Ensure()` installs the pinned OpenCode CLI release from GitHub (verified against its SHA-256) into `~/.opencode/bin`, `obsidianinstaller.Ensure()` installs the pinned Obsidian MCP, the bundled document skills and opencode plugins are installed into `~/.config/opencode` (and any retired content bundle from an older version is removed), and default MCP servers (the built-in catalogue, switched off, and `recall`, switched on) are registered.
3. Chat endpoints return **503** (via `requireOpencodeReady`) until the opencode binary is verified.
4. Browser sends a chat prompt via `POST /api/chat`.
5. The server runs opencode in one of two modes:
   - **CLI mode (default):** spawns `opencode run --format json …` per turn (with `--session <id>` to resume, `--agent`, `--model`, `--variant`, `--dir`, and `--dangerously-skip-permissions` when Yolo is on).
   - **Server mode (`KWA_OPENCODE_USE_SERVER=1`):** drives a long-lived `opencode serve` over HTTP/SSE, which additionally supports interactive permission approval.
6. opencode's JSONL/event stream is parsed into typed events (`chunk`, `tool_call`, `tool_done`, `question`, `permission`, `usage`, `result`, …).
7. Events stream to the browser as Server-Sent Events (SSE).
8. The opencode session ID is stored so follow-ups resume the same session.
9. If a new prompt arrives while one is streaming it goes onto a backend-side queue and is dispatched automatically when the current run finishes.

## API surface (selected)

| Method & path | Purpose |
|---|---|
| `GET /api/me` | Owner identity + workspace info + default backend |
| `GET /api/version` | Installed OpenCode CLI version |
| `GET /api/models` / `GET /api/agents` | Model list / agent list |
| `POST /api/chat` | Send a prompt; SSE response |
| `GET /api/sessions` / `POST /api/sessions/new` | List / create sessions |
| `GET /api/sessions/{id}/config` / `PATCH …/config` / `DELETE /api/sessions/{id}` | Read / update / delete a session |
| `POST /api/sessions/{id}/permission` | Approve / deny a tool permission (server mode) |
| `GET /api/sessions/{id}/stream` / `GET /api/sessions/active` | Attach to a session's SSE / list active streams |
| `GET/POST/DELETE /api/sessions/{id}/queue` | Prompt queue management |
| `GET /api/history` | History |
| `GET /api/recall/sessions` / `GET /api/recall/search` / `GET /api/recall/read` | Cross-session retrieval (read-only; backs the `recall` MCP server) |
| `GET /api/browse` / `POST /api/files/save` / `GET /api/files/download` | File manager |
| `GET /api/git/status` / `changes` — `POST /api/git/pull` / `push` / `commit` / `clone` | Source control |
| `GET/POST/DELETE /api/mcp/servers` | MCP server management (stored in `opencode.json`) |
| `GET /api/opencode/auth/status` — `POST /api/opencode/auth/login/start` / `cancel` — `GET …/login/info` | OpenCode (github-copilot) sign-in |
| `GET /healthz` / `GET /api/status` | Liveness / readiness |

## Security

Knowledge Worker Agent is built for one user in an isolated workspace. Read these before you run
it anywhere else:

- **No authentication, authorization, CSRF, or Origin checking on the HTTP API.** Anyone who can
  reach the port can use it as the owner. The server binds `127.0.0.1:8765` by default. The images
  set `KWA_HOST=0.0.0.0` so the workspace platform can reach it.
- **The agent acts without asking by default** (`--dangerously-skip-permissions`). Set
  `KWA_OPENCODE_USE_SERVER=1` for per-action approval.
- **The file manager has no workspace jail.** It can open any file the server user can.
- **Recall is read-only** and talks to `127.0.0.1` only.
- **Git never prompts for credentials** (`GIT_TERMINAL_PROMPT=0`, `GIT_ASKPASS=true`), so missing
  credentials show as errors instead of hanging the server.
- **Credentials** are stored by opencode in `~/.local/share/opencode/auth.json`.

For the full security model, see
[How Knowledge Worker Agent works](https://docs.citrix.com/en-us/securspaces/knowledge-worker-agent/about/how-knowledge-worker-agent-works.html).

To report a security vulnerability, follow [SECURITY.md](SECURITY.md). Don't open a public issue.

## Contributing

Knowledge Worker Agent is developed in an internal Citrix repository and published here at
intervals, so this repository doesn't accept pull requests. See [CONTRIBUTING.md](CONTRIBUTING.md).
Each release is listed in [CHANGELOG.md](CHANGELOG.md).

It's maintained by the Citrix SecurSpaces team. It isn't a supported Citrix product, and Citrix
doesn't provide support for it.

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
