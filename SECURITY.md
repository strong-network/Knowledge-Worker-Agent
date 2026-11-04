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

- **The HTTP API has no authentication or authorization.** Anyone who can reach its port can act
  as the owner. It listens on `127.0.0.1` and `::1` by default, where the workspace platform connects.
  A browser can't change anything from another origin: the server refuses such a request.
- **The agent acts without asking by default.** opencode runs with
  `--dangerously-skip-permissions`, or `--auto` on opencode 2.x.
- **The file manager opens only what's in the home folder,** apart from the hidden folders and
  files directly in it, such as `~/.ssh` and `~/.config`. The agent isn't limited this way: it
  runs as the server's user.
- **Chat sharing uses a separate listener,** on `127.0.0.1:8766`, which the platform exposes only
  to the coworkers the owner chose. It serves shared chats only.
- **Model provider credentials are stored by opencode,** in `~/.local/share/opencode/auth.json`.

Report anything that breaks these boundaries, such as a way to reach the API from outside the
workspace, or a way for a guest to reach a chat that isn't shared with them.

For the full model, see
[How Knowledge Worker Agent works](https://docs.citrix.com/en-us/securspaces/knowledge-worker-agent/about/how-knowledge-worker-agent-works.html).
