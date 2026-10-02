# Security

## Report a vulnerability

Don't report security vulnerabilities through public GitHub issues, pull requests, or
discussions. Report them to Citrix, following
[Citrix's process for reporting security issues (CTX081743)](https://support.citrix.com/article/CTX081743).

Knowledge Worker Agent isn't a supported Citrix product, so no version is supported. Reports are
still welcome, and any fix is published in a new release.

## Security model

Knowledge Worker Agent is built for one user, in a Citrix SecurSpaces workspace that isolates it.
It relies on the platform to decide who can reach it:

- **The HTTP API has no authentication, authorization, CSRF, or Origin check.** Anyone who can
  reach its port can act as the owner. It listens on `127.0.0.1` by default; the release image
  sets `0.0.0.0` so the workspace platform can reach it.
- **The agent acts without asking by default.** opencode runs with
  `--dangerously-skip-permissions`. Server mode (`KWA_OPENCODE_USE_SERVER=true`) asks before each
  action.
- **The file manager has no folder restriction.** It can open any file the server's user can.
- **Chat sharing uses a separate listener,** on `127.0.0.1:8766`, which the platform exposes only
  to the coworkers the owner chose. It serves shared chats only.
- **Model provider credentials are stored by opencode,** in `~/.local/share/opencode/auth.json`.

Report anything that breaks these boundaries, such as a way to reach the API from outside the
workspace, or a way for a guest to reach a chat that isn't shared with them.

For the full model, see
[How Knowledge Worker Agent works](https://docs.citrix.com/en-us/securspaces/knowledge-worker-agent/about/how-knowledge-worker-agent-works.html).
