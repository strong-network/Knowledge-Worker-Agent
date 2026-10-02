---
name: docx
description: >-
  Read and inspect Word (.docx) documents — extract text with heading structure, tables, existing
  review comments, tracked changes and embedded images. Load this skill whenever a task involves
  reading, reviewing, summarising, searching or auditing a .docx: a playbook, contract, report,
  proposal or specification someone sent. Also load it when asked what a Word document contains or
  what changed in it, or when asked to leave review comments in a Word document.
---

# DOCX — reading Word documents

## The one rule

**Never open a .docx with the `read` tool.** Knowledge Worker Agent's `pdf-guard` plugin drops non-image
attachments before they reach the model. Extract text instead; extract embedded images if you
need to see a figure.

## Setup — run once per config root

Needs `python-docx`. The venv is shared with `pdf`, `pptx` and `xlsx`, so it may
already exist. Derive the path from this skill's own location — never hardcode it.

```bash
SKILL_DIR="<this skill's directory, from its loaded location>"
CONFIG_ROOT="$(cd "$SKILL_DIR/../.." && pwd)"
VENV="$CONFIG_ROOT/.venv"

[ -x "$VENV/bin/python" ] || python3 -m venv "$VENV"
"$VENV/bin/python" -c "import docx" 2>/dev/null || "$VENV/bin/python" -m pip install --quiet python-docx
```

## Reading

```bash
X="$VENV/bin/python $SKILL_DIR/scripts/docx_extract.py"

$X doc.docx --stats        # structure only — cheapest
$X doc.docx --outline      # headings only; start here on anything long
$X doc.docx                # full body text, headings marked
$X doc.docx --tables       # tables as TSV
$X doc.docx --comments     # existing reviewer comments
$X doc.docx --revisions    # tracked insertions and deletions
$X doc.docx --images --out working
```

**Read the outline first.** A playbook or contract can be tens of thousands of words; dumping it
whole wastes the context you need to actually reason. Get the headings, then extract what matters.

Tables are skipped in the body output with a marker (`[table 2: 5x3 — use --tables]`) so the prose
stays readable.

## Comments and tracked changes matter

`--comments` and `--revisions` read the raw OOXML, because `python-docx` exposes neither. If a
document has been reviewed, these hold the actual disagreement — often the most useful content in
the file. Check them before summarising a document as though it were settled.

## Writing margin comments

`scripts/docx_comment.py` inserts **real Word comments** — the kind that appear in the review
pane and margin, not text annotations. The document body is not modified.

```bash
"$VENV/bin/python" "$SKILL_DIR/scripts/docx_comment.py" \
  in.docx out_reviewed.docx working/comments.json \
  --author "Alex Example" --initials AE
```

`comments.json` is a list:

```json
[{"anchor": "verbatim phrase from the document",
  "comment": "What is wrong, and the concrete fix.",
  "category": "SECURITY",
  "occurrence": 1}]
```

- **`anchor` must be an exact substring** of the document text. Copy it from the extract; do not
  retype or normalise punctuation. Anchors are matched against each paragraph's full text, so a
  phrase broken across formatting runs (bold in the middle, a stray editor split) still matches —
  the runs are split automatically.
- **`category`** is optional and is prefixed to the comment (`[SECURITY] ...`).
- **`occurrence`** is 1-based; use it only when the phrase genuinely repeats.

The script prints `applied` and `unmatched`. For anything unmatched, **shorten the anchor to a
shorter exact substring and re-run against the ORIGINAL file.** Never run it against its own
output to stack comments — pass every comment in one file, one run.

## Limits

- **Layout is not visible.** You get text and structure, not how the page looks. Do not describe
  formatting you have not seen.
- **Headers, footers and footnotes** are not extracted by the body walk.
- **`.doc` (legacy binary) is not supported** — only `.docx`.

## Rules

1. **Never `read` a .docx.** Extract text; extract images if you need to see something.
2. **Start with `--outline` or `--stats`** on any substantial document.
3. **Check `--comments` and `--revisions`** before treating a document as final.
4. **Cite headings or sections** when answering, so the user can verify.
5. **Write extracted files to `working/`.**

## Files in this skill

- `SKILL.md` — this file.
- `scripts/docx_extract.py` — outline, text, tables, comments, revisions, images, stats.
- `scripts/docx_comment.py` — insert real Word margin comments.

## Related

- **`pdf`, `pptx`, `xlsx`** — the same read-first pattern for those formats.
- A document-review agent can use this skill to review documents against a source of truth
  and leave margin comments.
