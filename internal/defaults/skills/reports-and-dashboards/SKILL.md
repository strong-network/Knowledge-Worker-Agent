---
name: reports-and-dashboards
description: >-
  Use when the user asks for a report (including a Markdown report), dashboard, one-pager, brief,
  summary, status page, scorecard, tracker, timeline, chart or visualisation, or a calculator or
  other small interactive tool, written as an HTML or Markdown file, and when asked to update,
  restyle, fix or share one, or to make one self-contained. Builds it as one self-contained file that works in
  Knowledge Worker Agent's preview and can be shared with coworkers as it is: no chart library or
  web font from the internet, and no linked files.
---

# Reports and dashboards

Build them as **one self-contained file**. Knowledge Worker Agent shows HTML in its preview, and
shares files with coworkers, under a content policy that blocks every load from the internet or
from another file, and every connection. A dashboard that loads its chart library from a CDN is
blank in the preview and for everyone it's shared with. Built as below, it works in both, with no
changes.

## Pick the format

| The user wants | Write |
|---|---|
| Charts, filters, tabs, a dashboard: anything visual or interactive | One `.html` file |
| A text-first report: headings, paragraphs, tables, a few pictures | One `.md` file |
| A PDF | Use the `pdf` skill |

Markdown is rendered on the server and runs no scripts. Its pictures show only as `data:` URLs in
PNG, JPEG, GIF or WebP: an SVG picture isn't shown, and HTML written into the Markdown isn't
rendered.

Write the file at the root of the chat's folder, where finished deliverables go, named for what it
is: `q4-pipeline-dashboard.html`, not `output.html`. To change it later, edit the same file rather
than writing a new one: people it's shared with see the file at that path.

## Self-contained HTML

Everything the page needs is inside it:

- **Scripts and styles inline**, in `<script>` and `<style>` elements. Inline scripts run.
- **Data as JSON** in a `<script type="application/json" id="data">` element, read with
  `JSON.parse(document.getElementById('data').textContent)`.
- **Pictures** as `data:` URLs, such as `<img src="data:image/png;base64,…">`, or drawn as inline
  `<svg>`. For a picture in the folder, encode its bytes (`base64 -w0 logo.png`); never point at
  the file.
- **Fonts:** a system font stack (`system-ui, -apple-system, "Segoe UI", Roboto, sans-serif`), or
  a font as a `data:` URL in `@font-face`. Never a web font service.
- **Charts** drawn by the page's own code: inline SVG (bars, lines, pies and sparklines take a few
  dozen lines each), or a `<canvas>` drawn with its 2D API. Don't load a chart library: the
  policy blocks it, and pasting a large one in bloats the file.

A shareable file can't:

- **Load anything by URL or path:** `<script src>`; `<link>` as a style sheet, icon, preload or
  manifest; `<img>` `src` and `srcset`; `<source>`; `<video>` and its poster; `<audio>`;
  `<track>`; `<iframe>`; `<frame>`; `<embed>`; `<object>`; `<input type="image">`; SVG `<image>`
  and `<use>` pointing at a file; a `background` attribute; CSS `url(…)` and `@import`.
- **Send viewers elsewhere:** `<base href>`, or `<meta http-equiv="refresh">`.
- **Connect while it runs:** `fetch`, `XMLHttpRequest`, `WebSocket`, `EventSource`,
  `navigator.sendBeacon`, `importScripts`, `import()` or a module `import` from a URL, workers.
- **Keep state in the browser:** `localStorage`, `sessionStorage`, `indexedDB`.
- **Send a `<form>`.**
- **Link to another file in the folder** with `<a href>`: it won't open. Link to web pages
  instead.

What still works, so don't give it up:

- Interaction in the page's own script: filters, sorting, search, tabs, tooltips, toggles.
- Links to web pages, with `target="_blank" rel="noopener"` on each. They open in a new
  tab.
- Export buttons that build a file in the page, such as a CSV from a `Blob`.
- `alert`, `confirm` and `print()`.

## Only the data the report needs

Everyone the file is shared with sees all of it, including anything fetched with the user's
connectors. Put in the figures the report shows, not whole exports. Leave out personal data,
credentials and internal identifiers the report doesn't need, and say what you left out. Keep the
file well under 25 MB; a dashboard rarely needs more than a few hundred KB.

## Check it before you say it's done

When the file is written, ask Knowledge Worker Agent, from the chat's folder where you're working.
It runs the same check as **Share**:

```bash
curl -s --get "${KWA_URL:-http://127.0.0.1:8765}/api/artifacts/can-share" \
  --data-urlencode "folder=$PWD" --data-urlencode "file=q4-pipeline-dashboard.html"
```

The answer is JSON:

```json
{"can_share": true, "self_contained": false,
 "findings": [{"blocking": true, "what": "Loads a script from cdn.jsdelivr.net", "line": 7}]}
```

- **`self_contained` is `false`:** fix every finding with `"blocking": true`, then ask again.
  Findings with `"blocking": false` may not work for viewers: fix them when you can.
- **The check is for you.** Don't tell the user about it or its result; only mention a finding
  you couldn't fix.
- **The command fails, or doesn't answer in JSON:** read the file yourself for everything in the
  list above, and say nothing about sharing.

## Tell the user they can share it

Once, when the file is first made and passes the check, and only when `can_share` is `true`, add
one sentence to your reply, such as:

> You can share this with coworkers: open **Files**, point at `q4-pipeline-dashboard.html` and
> select **Share**. They'll always see its latest version.

- **Not again** when you change the file later.
- **Not when `can_share` is `false`.** Then your reply says nothing about sharing at all: not
  that sharing is off, unavailable or disabled, and not why. The `reason` (sharing is off, or
  this is a temporary chat) is for you, not for the user.
- **Not to a guest:** when your instructions say a guest wrote the message, only the owner can
  share.
- **Kept up to date:** if the user wants the report refreshed, suggest a scheduled task that
  rewrites the same file. The people it's shared with keep their link and see each new version.

Apart from that sentence, and a scheduled task when one is wanted, your reply says nothing about
sharing.
