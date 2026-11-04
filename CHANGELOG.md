# Changelog

Each release published to this repository adds an entry here, newest first. Version numbers
come from the `VERSION` file. Entries before the first public release are reconstructed from
the project's history, starting after 1.1.2.

## Unreleased

- First public release.

## 2.0.2 (2026-11-04)

### Added

- **Questions from the assistant.** With opencode 2.x, when the assistant asks you something, it
  stops and waits for your answers, where it used to carry on with its own assumptions. Its
  questions are in its response and on a **Question** card, with the answers it suggests and
  room for your own. Your answers go back as your next message, and the assistant continues.
  A scheduled task's run doesn't stop at a question: no one is there to answer, so the
  assistant decides and says what it assumed.
- A Markdown file's preview in **Files** draws its Mermaid diagrams and SVG images, as the
  chat does, in the light or dark theme, with **Copy source** and **Download**. With changes
  highlighted, a changed diagram is drawn in its mark, and a removed one shows as its source.
- **Removed:** the **Approve the assistant's actions** switch in the **Share** window. Guests
  can no longer answer the assistant's requests for approval. The assistant asks for approval
  only with opencode 1.x in server mode, so with 2.x the switch did nothing.

### Changed

- **opencode 2.x by default.** Chats run on opencode 2.2.5 unless `KWA_OPENCODE_MAJOR=1` asks
  for 1.x, which is the way back. A workspace that hasn't moved its files into one folder yet
  runs 1.x until it has, because the move re-points chats through 1.x. The release image embeds
  2.x instead of 1.x, for workspaces that can't reach the npm registry; 1.x comes from GitHub.
  The copy of opencode's database from before 2.x is skipped when it would leave less than 1 GB
  free.

### Fixed

- With opencode 2.x, the assistant reads a file it needs inside Code Mode, such as a long text
  to send to Jira, through the app, instead of starting a web server of its own to serve it.

### Removed

- The **Approve the assistant's actions** switch in the **Share** window. Guests
  can no longer answer the assistant's requests for approval. The assistant asks for approval
  only with opencode 1.x in server mode, so with 2.x the switch did nothing.

## 2.0.1 (2026-11-02)

### Added

- **Setup guide and what's new.** Until the assistant has a model that works, a guide shows its
  two setup steps: connecting a model and connecting apps. Its buttons open **Accounts** and
  **Connectors**, and you come back to the guide when you're done there. When your administrator
  makes a model provider or connector available that you haven't set up, **New in your
  workspace** lists it. Each shows at most once per start of Knowledge Worker Agent. A guide you
  close comes back at the next start until you tick **Don't show this again**; what's new lists
  an item once, unless you choose **Remind me later**.
  Connector and model provider artifacts can say in one sentence what they're for, with
  `kwa.description`.
- **File links in replies.** A file in the chat's folder that a reply links to, or names in code
  such as `report.md`, opens in **Files** with a click. The assistant is asked to link the files
  it mentions.

### Fixed

- A connector your administrator stops assigning is removed at the next start or **Refresh
  configuration**, rather than staying in **Connectors** as a local server that doesn't work.
- In the light theme, a code block in a language the highlighter doesn't know, such as `text`,
  showed dark text on its dark background, in replies and in the Markdown preview. The preview's
  code blocks also lost their inner padding.

### Security

- A web page open in the same browser could act through the app: start the agent,
  add a connector that runs a command, write or delete files. The server now refuses a request
  that changes something when a browser sends it from another origin. It also listens on
  `127.0.0.1` and `::1` only by default, where the workspace platform connects; the images no
  longer set `KWA_HOST=0.0.0.0`, which reached it from the network.
- The app could open any file the server's user could, such as a key in `~/.ssh`.
  **Files**, the folder pickers, previews, earlier versions, sharing and git now open only what's
  in the home folder, apart from the hidden folders and files directly in it, such as `~/.ssh`
  and `~/.config`. A chat or scheduled task can't be given a folder outside it either. A chat
  whose folder is already outside the home folder keeps working, but **Files** can't open it.
- The list of connectors returned their headers and environment variables as they
  were saved, including access tokens such as the GitHub connector's. They now show as
  `••••••••`; editing a connector keeps a hidden value as long as its address or command
  stays the same.
- A file in the workspace opened on its own through the app, such as an SVG image,
  could run script in the app. Every file but a PDF is now served sandboxed, as shared chats
  already did.
- Markdown could run script in the app, from a reply or from a file: as a code
  block's language, shown above the block, or as an earlier version's content in the preview's
  change marks.

## 2.0.0 (2026-10-31)

### Added

- **opencode 2.x, as a trial.** `KWA_OPENCODE_MAJOR=2` installs opencode 2.2.5 beside the
  pinned 1.x and runs chats with it; without the setting nothing changes. Chats, agents, skills,
  connectors, model sign-ins and keys carry over, and connector sign-ins made with 1.x are copied
  across once. Before 2.x first opens opencode's database, Knowledge Worker Agent keeps a copy in
  `.system/opencode-db-before-2.x.db`. To go back, remove the setting: 1.x still has the chats as
  they were before the switch, but not what was said in them with 2.x, and a chat started on 1.x
  after the switch starts without its history on 2.x. Server mode (`KWA_OPENCODE_USE_SERVER`) and
  the one-folder migration need 1.x and don't run with 2.x.
- **Search chats.** **Search chats**, under **New chat** in the sidebar, opens a page that
  searches the titles and messages of all your chats, temporary chats included. It replaces the
  sidebar's search box, which matched titles only. There's one result per chat, best first, with
  the passage that matched, who wrote it and when, and how many more matches the chat has. Words
  match the start of longer words and similar spellings, which come last under **Close
  matches**; put a phrase in quotes to find it exactly. A time range narrows the search to the
  past week, month or year, or to dates you choose. Selecting a result opens the chat at that
  message, with the words highlighted.
- **Diagrams and images in replies.** A reply can draw a Mermaid diagram (a ` ```mermaid ` code
  block) or show an SVG image (a ` ```svg ` block), and each has **Copy source** and
  **Download**, which saves it as SVG. The agent is told the chat shows them, and uses one when it
  explains something better than words. When the workspace has a skill that draws diagrams or
  charts as images, the agent uses it for one worth keeping, such as an architecture diagram, a
  data chart or an infographic, and shows the picture in the reply; quick sketches stay Mermaid.
  Images in a reply show when they're files in the chat's folder, with **Download**; an image from
  the web shows only when you select **Show image**, so opening a chat doesn't tell another site
  you read it. An SVG can't run scripts or load other files. Shared chats show them the same way.
  Diagrams that ask for Mermaid's ELK layout show their source instead: its library isn't
  included.

### Fixed

- Signing in to a connector that comes back to its own address works. A connector can set
  `oauth.redirectUri` in its configuration, and the address you paste after signing in must now
  match the one that sign-in uses, rather than always ending in `/oauth/callback`. Figma needs
  this: it lets OpenCode sign in only with `http://127.0.0.1:19876/callback`.
- A connector whose configuration sets OAuth options, such as Figma, shows as connected after
  you sign in, rather than **Sign-in needed**. **Sign in** on a connector that still has valid
  credentials starts a new sign-in rather than waiting until it times out, and the sign-in's
  technical details show opencode's output as lines of text rather than one character a line.

### Security

- DOMPurify, which sanitizes rendered Markdown, is 3.4.17, fixing three advisories
  (GHSA-cg2g-xh64-v4q7, GHSA-mrv2-3pqr-j5jr, GHSA-c6gr-qvv4-5v3f) in options the app doesn't
  use. `golang.org/x/net` is v0.62.0, fixing two vulnerabilities in its HTML package
  (GO-2026-6866, GO-2026-6860) that the server doesn't reach. Vite, which only builds the
  frontend, is 8.3.5, fixing six advisories in its development server.

## 1.6.0 (2026-10-26)

### Added

- **Temporary chats.** Turn on **Temporary chat** on the New chat page to start a chat that
  isn't saved to your history, or make a chat temporary in its Session settings. A temporary
  chat is deleted, with its folder, 24 hours after the last message from you or the agent, and
  never while the agent is working. Its header and the sidebar's Temporary group show how long
  it has left; select **Keep chat** to save it. Temporary chats and their files can't be
  shared, and a chat that's shared, or whose files are, can't be made temporary. The chats
  that make a shared file self-contained were already temporary, so they're now deleted the
  same way.
- When less than 5% of the workspace's disk is free, a banner at the top says so, with how much
  is left. **Get more space** explains how to increase the disk in the SecurSpaces console
  (Knowledge Worker Agent can't change it itself), links to the console, and offers
  Diagnostics and Tidy up to free up space. The banner goes once there's room again.
- A **Files** button in a chat's header, once the chat has files, shows and hides the Files
  widget. Hidden, it stays hidden in other chats and after a reload, and the chat takes the
  width. A dot on the button says that files are new or updated. On a phone it opens the files
  over the chat, as before.
- **File versions.** A text file in a chat's folder (Markdown, CSV, code) keeps its earlier
  versions: one for each turn in which the agent changes it, one when you save or upload it,
  and one for a change made elsewhere. When a file has more than one, its viewer shows which
  version it is, when and by whom it was made, and how many lines changed since the one before
  ("Since v1: 3 lines added, 2 removed or changed"). Pick another version from the version
  button at the top to read it as it was or download it; **Go to latest** comes back. The 50
  newest versions of a file are kept, for files up to 1 MB, and they go with the file or with
  the chat's folder. **Highlight changes**, on by default, marks what was added or changed since
  the version before.
- **Highlight changes** marks the words that changed in a Markdown preview's paragraphs,
  headings, quotes, list items and table cells, in the lines of a text file or source, and
  whole rows in a table. Added words are tinted and underlined, with a bar beside their
  paragraph or line. A paragraph or line that's new, rewritten, or only formatted differently
  is marked whole. A list item renumbered because one above it was removed isn't marked.
- **Show deletions**, next to **Highlight changes** and off by default, shows what was removed
  since the version before, struck through where it was: words within a line or paragraph,
  and whole paragraphs, list items, table rows and lines. Removed text isn't part of the file:
  it's left out when you copy from the preview or save. Your choice is remembered.
- **Side chats.** **Ask on the side** in a chat's ⋯ menu opens a side chat for a question that
  shouldn't interrupt the chat. The agent there knows everything in the chat so far and works
  in the same folder, so it can read and change the same files, but nothing you ask comes back
  to the chat. A side chat is a temporary chat: the sidebar lists it under its chat, its header
  links back to the chat, and **Keep chat** saves it. A chat's folder stays while a side chat
  of it works there, so deleting the chat leaves the side chat's files.

### Changed

- The agent's avatar has a small pixel display for a face, which shows what the agent is
  doing. Its eyes glance around while it thinks, and they turn to ^ ^ when a reply is ready.
  While the agent works, the display follows its latest step: a scanner while it updates its
  plan, a magnifier while it searches, arrows while it reads a web page or uses a connector,
  lines of text while it writes, and a spinner otherwise. While it waits it blinks and, every
  30 seconds, plays a short animation (snooze, pong, twinkle or a glider), but not while
  you're typing. Avatars in a chat are larger, 32 px (28 px on phones), so the display
  reads. Only the newest reply's face moves, and each state shows a still picture when your
  system asks for reduced motion.
- **One ⋯ menu per chat.** A chat in the sidebar has a ⋯ button in place of its row of icons,
  with Star, Rename, Share, Make temporary and Delete. It shows when you point at the chat or
  reach it with the keyboard, and always on touch screens. The menus work from the keyboard: the
  arrow keys, Home and End move through them, and Escape closes them.
- The ⋯ (**More actions**) at the right of a chat's header opens the chat's actions: Share,
  Star, Rename, Make temporary, **Chat settings**, **Open folder in VS Code** and **Delete chat**.
  It replaces the header's VS Code and Session settings buttons, and puts Share, Star and Delete
  within reach on a phone, where the sidebar is a drawer. A starred chat shows a star after its
  title.
- A file in the Files widget has a ⋯ button in place of its row of icons, with **Open in new
  window** and **Copy contents** (for text files) as well as Download, Share, Rename, Duplicate
  and Delete. In a temporary chat, Share says to keep the chat to share the file.
- The Files widget takes the right side of the page, top to bottom, beside the chat rather than
  floating as a card below its header. Its header, or an open file's, sits in line with the
  chat's header, whose **Files** and ⋯ buttons move left to make room. The list no longer
  collapses in place: the **Files** button shows and hides the widget.
- Diagnostics shows the disk's free space even before the files have moved into one folder.

### Fixed

- A chat no longer gets stuck on "messages: text content blocks must contain non-whitespace
  text". A reply of only blank lines left a blank block in the chat's history, and because every
  message replays the history, the Claude models rejected every later message, whichever model or
  agent was chosen. Such blocks are now left out of each request, so affected chats work again
  with their context. If the error still comes up, the chat starts a fresh conversation and says
  so, as it does for an unreadable attachment.

### Security

- opencode is pinned to v1.19.4, the latest 1.x release. It updates opencode's bundled runtime
  with security fixes.

## 1.5.5 (2026-10-21)

### Added

- **Regional endpoints for a model provider.** A provider artifact in central configuration
  can list the regions a provider is served from, under `kwa.endpoints`, and read the one in
  use from `{env:KWA_ENDPOINT_<PROVIDER ID>}` in its options. The workspace uses the fastest
  region it reaches, unless the user picks one under **Accounts & connections**, which shows
  each region's response time. The choice is kept across restarts.
- **Vertex models are found automatically.** The workspace asks Vertex, or an AI gateway in
  front of it, about each chat model in opencode's catalogue, with a request that is refused
  before any text is generated, and offers the ones it may use. This replaces the hand-kept
  Vertex model list, which still offered a model an organization policy blocked and lacked
  newer ones such as Claude Sonnet 5.5. It runs at startup and with every model refresh. A
  check that gets no answer keeps the model's last result, and a provider artifact's
  `whitelist` or `blacklist` still narrows what is offered.

### Changed

- The agent's avatar in a chat is the product's robot, and it shows what the agent is doing:
  it glances around while it thinks, reads along while it works and squints happily when a
  reply is ready. While it waits it blinks and, every 30 seconds, ducks, peeks or leans in,
  but not while you're typing. Only the newest reply's robot moves, and nothing moves when
  your system asks for reduced motion.
- The agent's plan is at the top of its reply, under its name, instead of in a panel beside
  the chat. It stays open and updates as the agent works, then folds to one line, such as
  **Plan: 4 of 5 done**, when the reply is finished; select it to open the plan again. Only the
  newest reply shows a plan. Phones and the shared-chat page show it the same way.
- The Vertex connection test calls the address and headers central configuration gives
  opencode, so it tests an AI gateway in front of Vertex rather than Vertex itself.
- In server mode (`KWA_OPENCODE_USE_SERVER`), each `opencode serve` starts with the server's
  current environment, so it sees a newly chosen region.
- The Go toolchain floor is 1.27.3, which fixes a standard-library vulnerability in 1.27.2
  (GO-2026-6809: compromised intermediate certificates were trusted).

## 1.5.4 (2026-10-16)

### Added

- **Share a file from a chat.** Share a report, a dashboard, a small tool, a Markdown file, a
  PDF or an image with chosen people in the project, or with everyone in it. Each shared file
  gets a workspace app and a port of its own (`KWA_ARTIFACT_PORTS`, default `8770-8799`).
  People see its current version in a small viewer page, with an **Updated — Reload** notice
  and, if you allow it, **Download**.
- Shared files can't load or send anything: they're served under a content policy that blocks
  every load and connection. Before sharing, a check lists what wouldn't work, such as scripts,
  styles, images or fonts from the internet or from other files.
- Shared Markdown files are rendered on the server, in the GitHub flavour, without the HTML
  written into them.
- **Make self-contained:** a background chat rewrites a copy of a file that loads things from
  outside. The result replaces the file only if it passes the check and the file hasn't
  changed meanwhile; the original is kept in `.system/artifact-backups`.
- The **Artifacts** page lists every shared file with its status (Shared, Needs attention,
  Fixing, Missing, Unavailable, Removed or Turned off) and **Copy link**, **Open**, **Change
  sharing**, **Repair** and **Stop sharing**, with guidance while nothing is shared.
- In the **Files** panel: **Share** on a file's row and a mark on shared files. A shared file
  follows renames and moves, and deleting it asks first.
- HTML files opened in the app run their scripts, on a preview origin of their own
  (`KWA_PREVIEW_PORT`, default `8769`), and offer a new tab when the preview can't load.
- A bundled `reports-and-dashboards` skill builds reports and dashboards as one self-contained
  file, and tells you when you can share it. It asks `GET /api/artifacts/can-share`, which the
  agent reaches through `KWA_URL`.
- Tooltips on every composer control, shown after a short delay on hover and at once on
  keyboard focus.

### Changed

- Links to web pages in a shared HTML file always open in a new tab.
- With sharing files turned off (`KWA_ARTIFACT_PORTS=off`), files already shared aren't served
  until it's turned back on.
- After you copy a share link, the Share dialogs suggest telling people to paste it into their
  browser's address bar: a link clicked in another app or site leads to the SecurSpaces sign-in
  page.
- The composer leaves as much space above the text as below it.
- Sharing a chat uses the same Share icon as sharing a file.

## 1.5.3 (2026-10-15)

### Added

- **Settings > Diagnostics:** versions, status, central configuration and logs, with **Copy
  report** and **Refresh configuration**, which re-applies the central configuration without a
  restart.
- Resize the chat sidebar by dragging its edge. Its collapse button moved to the right of its
  header.
- **Files** panel: duplicate a file, and drag files between folders.

### Changed

- opencode is pinned to v1.19.0.
- Central configuration builds its folder beside the current one and swaps it in, so a failed
  run leaves the configuration in use as it was.

## 1.5.2 (2026-10-13)

### Added

- Previews of PowerPoint, Word and Excel files, converted to PDF with LibreOffice. The preview
  offers to install LibreOffice, and the install survives restarts.
- Markdown and text files wrap long lines in the code view.

### Fixed

- Vertex turns no longer wait about 15 seconds for Google's metadata server:
  `GOOGLE_CLOUD_PROJECT` is set from the Vertex project.
- Messages the app shows are no longer sent with status 503, whose body the SecurSpaces proxy
  drops: a missing LibreOffice, sharing being unavailable, and the writing assistant being
  unavailable.

### Security

- The Go toolchain floor is 1.27.2, which fixes 11 standard-library vulnerabilities in 1.27.1.

## 1.5.1 (2026-10-02)

### Changed

- Go 1.27, and dependency upgrades: `modernc.org/sqlite` 1.60, Pinia 4, marked 18, vue-tsc 3
  and the GitHub Actions used by CI and publishing.
- Published images are tagged with their version as well as `latest`, and the README shows how
  to pull them.

## 1.5.0 (2026-10-02)

### Added

- A workflow that publishes the release image, and `.github/RELEASING.md`, which describes it.
- `Assets/` with the app's icon.
- The README explains how to give users access through a SecurSpaces custom access item.

### Changed

- The release image is based on `strongnetwork/cloud_editor_generic` 2.3.9.

## 1.2.0 (2026-10-02)

### Added

- **Dictation:** speak into the composer, transcribed inside the workspace with whisper.cpp
  and Whisper. Words appear while you speak, and nothing is sent until you send it. A
  configuration repository can provide a vocabulary of product names.
- **One folder:** everything Knowledge Worker Agent keeps lives under `~/Knowledge_Worker_Agent`
  (`KWA_HOME`). An existing install moves there at its next start: the move is journaled, rolls
  back on failure, shows a status page while it runs, and keeps backups for 30 days.
  `KWA_MIGRATE` turns it off, runs a dry run, or undoes it, and
  `knowledge-worker-agent migrate dry-run` reports what it would do.
- Connect a repository by cloning a new one from its URL.
- Apache-2.0 licence, `NOTICE`, SPDX headers, `CONTRIBUTING.md`, `SECURITY.md` and this
  changelog.

### Changed

- The product is named Knowledge Worker Agent in the app and its messages.
- Settings are read under `KWA_` names, such as `KWA_PORT`, `KWA_GUEST_PORT` and
  `KWA_AUTO_TITLE`. The old names (`COPILOT_WEB_PORT` and others) still work, and the log
  names the replacement for each one in use.
- The binary is `knowledge-worker-agent`, and the Go module is
  `github.com/strong-network/Knowledge-Worker-Agent`.
- The server listens on `127.0.0.1` by default. The release image sets `KWA_HOST=0.0.0.0`.
- The built-in connector catalogue is a starter set, Atlassian and Microsoft Learn. Untouched
  entries of the others are removed at startup.
- A configuration repository's Knowledge Worker Agent block is named `kwa`; `sds` is still
  read.
- Faster start: the provider sign-in check no longer runs gcloud or opencode, and the
  **Files** column shows without waiting for it.
- The log no longer contains chat text: prompts are replaced by their length.
- Chat backup's commit messages and fallback identity name Knowledge Worker Agent.
- Vite 8. Supported browsers start at Chrome and Edge 111, Firefox 114 and Safari 16.4.

### Fixed

- Cloning reports a failure when git exits without cloning, and never waits for a credential
  prompt.
- The Default and Thinking presets match their model exactly, so Thinking no longer picks a
  newer, untested model. A failed turn's error stays visible in the chat.
- File previews and downloads check for a newer version, so a rewritten file no longer shows
  its old content.
- Dictation drops the sound labels Whisper writes for noise before you stop.
- Knowledge Worker Agent's own connectors repair their paths at every start, and can't be
  edited or removed.

### Security

- opencode and the Obsidian connector are pinned to exact versions, and opencode is checked
  against its SHA-256. CI runs govulncheck.
- Rendered answers and Markdown previews can't contain forms.

### Removed

- The answer route and `COPILOT_AGENTS_DIR`, both unused, and other dead code.
- The local documentation: it's on [docs.citrix.com](https://docs.citrix.com/en-us/securspaces/knowledge-worker-agent/).
- The unused `/data` volume in the release image.
