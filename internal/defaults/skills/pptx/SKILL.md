---
name: pptx
description: >-
  Read and inspect PowerPoint (.pptx) files — extract slide text, speaker notes and tables, list a
  deck's structure, pull out embedded images, or answer questions about a deck someone sent. Load
  this skill whenever a task involves reading, reviewing, summarising, searching or auditing an
  existing .pptx. For *creating* branded slides, use a slide-authoring skill instead, if one is installed;
  this skill is for decks you did not build.
---

# PPTX — reading decks

Reading, inspecting and auditing existing PowerPoint files.

## Scope — which skill to load

| Task | Skill |
|---|---|
| Read, summarise, search or audit an existing deck | **this one** |
| Extract text, notes, tables or images from a deck | **this one** |
| Create or edit a branded deck | A slide-authoring skill, if one is installed |
| QA a deck **you just built** | The skill that built it |

A slide-authoring skill is large — brand system, fonts, build pipeline.
Loading it just to read someone's deck is unnecessary. Load this one instead.

## The one rule

**Never open a .pptx with the `read` tool.** Knowledge Worker Agent's `pdf-guard` plugin drops non-image
attachments before they reach the model, because the provider accepts `image/*` only. Extract
text, or extract the embedded images and read those.

## Setup — run once per config root

Only needs `python-pptx`. It shares the venv with the other document skills, so it may
already exist. Derive the path from this skill's own location — never hardcode it.

```bash
SKILL_DIR="<this skill's directory, from its loaded location>"
CONFIG_ROOT="$(cd "$SKILL_DIR/../.." && pwd)"
VENV="$CONFIG_ROOT/.venv"

[ -x "$VENV/bin/python" ] || python3 -m venv "$VENV"
"$VENV/bin/python" -c "import pptx" 2>/dev/null || "$VENV/bin/python" -m pip install --quiet python-pptx
```

## Reading a deck

```bash
X="$VENV/bin/python $SKILL_DIR/scripts/pptx_extract.py"

$X deck.pptx                      # outline: every slide's text + notes
$X deck.pptx --stats              # structure only — cheap, start here for a big deck
$X deck.pptx --slides 2,5-7       # just those slides
$X deck.pptx --notes              # speaker notes only
$X deck.pptx --tables             # tables as TSV
$X deck.pptx --images --out working   # embedded images -> PNG
```

**Start with `--stats`** on anything over ~15 slides. It reports shapes, text blocks, pictures,
tables and whether notes exist per slide, so you can extract only what matters.

Text from grouped shapes is included — groups are flattened automatically.

## When a slide has no text

Some decks are **flat images** — one picture per slide, no text at all. This is common: it is
what slide tools produce when they export slides as images, and what many exported decks look like.

The script detects this and tells you:

```
! slides [1, 2] have no text — they are flat images.
! Re-run with --images to extract them, then read the PNG to see them.
```

Do exactly that. `--images` writes the embedded PNGs (often full-resolution, e.g. 3840×2160),
and reading a PNG is allowed. **A flat-image deck is not an unreadable deck** — do not report
it as empty.

## Limits — be honest about these

- **A deck with real text but complex layout cannot be *seen*.** You can read every word, but
  not the visual arrangement, unless the slides happen to be images. Rendering arbitrary PPTX to
  images needs LibreOffice, which is not installed. Say so rather than guessing at layout.
- **Charts are not extracted as data.** A native chart's underlying numbers are not read by the
  outline; report the chart's presence and its title.
- **Animations, transitions and slide masters are not inspected.**

## Rules

1. **Never `read` a .pptx.** Extract text, or extract images and read those.
2. **Never say a deck is empty because text extraction returned nothing.** Check for pictures
   first — it is almost certainly a flat-image deck.
3. **Cite slide numbers** when answering from a deck, so the user can verify.
4. **Write extracted files to `working/`**, not the workspace root. They are intermediates.
5. **Do not describe layout you have not seen.** Reading text tells you content, not design.

## Files in this skill

- `SKILL.md` — this file.
- `scripts/pptx_extract.py` — outline, text, notes, tables, images, stats.

## Related skills

- **A slide-authoring skill**, if one is installed — authoring branded decks, and QA on decks you built.
- **`pdf`** — same pattern for PDF files.
