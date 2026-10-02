---
name: pdf
description: >-
  Read, search, extract from, create and manipulate PDF files. Load this skill whenever a task
  involves a .pdf — extracting text or tables, searching a document, reading a report or contract,
  looking at a scanned page or chart, merging or splitting files, rotating pages, encrypting or
  decrypting, converting pages to images, or producing a PDF from HTML or from scratch. Also load
  it when a PDF attachment could not be shown in chat, or when the user asks what a PDF contains.
---

# PDF

Working with PDFs in Knowledge Worker Agent. Text and table extraction, page rendering, editing, and creation.

## The one rule that matters

**Never open a PDF with the `read` tool.** Knowledge Worker Agent's `pdf-guard` plugin strips non-image
attachments before they reach the model, because the provider behind every model here accepts
`image/*` only. Without the guard, a single PDF read would poison the session history and every
later turn in that chat would fail permanently.

So the guard is protecting you, not blocking you. Work with PDFs like this instead:

| You need | Do this |
|---|---|
| The words, tables, or a search | `scripts/pdf_extract.py` — returns **text** |
| To actually *see* a page | `scripts/pdf_render.py` — writes **PNG**, then `read` the PNG |
| To change the file | `scripts/pdf_edit.py` — writes a new file |
| To produce a PDF | See `reference/creating-pdfs.md` |

PNGs pass the guard normally, so rendering is the sanctioned way to look at a page.

## Setup — run once per config root

The scripts need a Python venv beside the `skills/` directory. It does not exist on first use.
Derive the path from this skill's own location rather than hardcoding it — it differs between a
user install and a platform-provisioned one.

```bash
SKILL_DIR="<this skill's directory, from its loaded location>"
CONFIG_ROOT="$(cd "$SKILL_DIR/../.." && pwd)"
VENV="$CONFIG_ROOT/.venv"

[ -x "$VENV/bin/python" ] && "$VENV/bin/python" -c "import pypdf" 2>/dev/null \
  || bash "$SKILL_DIR/scripts/setup.sh"
```

First run takes a minute or two while the venv is built. It is shared with
the other document skills, so it may already exist — the check above covers both cases.

Run every script with `"$VENV/bin/python"`, never bare `python3`.

## Reading a PDF

```bash
# What is this document? Metadata and page count first — cheap.
"$VENV/bin/python" "$SKILL_DIR/scripts/pdf_extract.py" report.pdf --meta

# Full text, or just some pages (1-based; ranges allowed)
"$VENV/bin/python" "$SKILL_DIR/scripts/pdf_extract.py" report.pdf
"$VENV/bin/python" "$SKILL_DIR/scripts/pdf_extract.py" report.pdf --pages 1,4-6

# Tables as TSV
"$VENV/bin/python" "$SKILL_DIR/scripts/pdf_extract.py" report.pdf --tables

# Which pages mention something
"$VENV/bin/python" "$SKILL_DIR/scripts/pdf_extract.py" report.pdf --search "ISO 27001"

# Encrypted source
"$VENV/bin/python" "$SKILL_DIR/scripts/pdf_extract.py" report.pdf --password PW
```

**Start with `--meta`, then `--search`, then extract only the pages you need.** A long PDF will
swamp the conversation if you dump it whole. For anything over ~20 pages, search first.

If a page returns `(no extractable text)` it is almost certainly **scanned**. Do not conclude the
document is empty — render it and look at it.

## Seeing a page

For scanned documents, charts, diagrams, layout or design questions, or to check a PDF you just
produced:

```bash
"$VENV/bin/python" "$SKILL_DIR/scripts/pdf_render.py" report.pdf --pages 1 --out working
# -> working/report-p1.png ... then read that PNG
```

Default scale 2 (~150 dpi) is right for reading text. Use `--scale 3` for fine print. Render only
the pages you need — each image costs real context.

## Editing

Every operation writes a new file; the source is never modified.

```bash
E="$VENV/bin/python $SKILL_DIR/scripts/pdf_edit.py"
$E merge   out.pdf a.pdf b.pdf
$E split   in.pdf  out.pdf --pages 1,3-5
$E rotate  in.pdf  out.pdf --angle 90
$E encrypt in.pdf  out.pdf --password PW
$E decrypt in.pdf  out.pdf --password PW
```

## Creating

See `reference/creating-pdfs.md`. Two routes:

- **HTML → PDF via Chromium** — preferred for anything that should look designed, and the only
  route for branded output (reuses a brand skill's stylesheets, if one is installed).
- **`reportlab`** — for programmatic documents: generated reports, tables, invoices.

## Rules

1. **Never `read` a PDF.** Extract text, or render to PNG and read that. This is not a style
   preference — it protects the chat session.
2. **Write output where it belongs.** Finished PDFs to the workspace root; PNGs, intermediate
   files and build scratch to `working/`.
3. **Never modify a source PDF in place.** All scripts write new files; keep it that way.
4. **Do not guess at contents.** If extraction returns nothing and rendering is not possible, say
   so. A PDF you could not read is not a PDF you can summarise.
5. **Say which pages you used.** When answering from a long document, cite the page numbers so the
   user can verify.
6. **Check before claiming success.** After producing a PDF, extract its text or render page 1 and
   look at it. Do not hand over an unverified file.

## Files in this skill

- `SKILL.md` — this router.
- `scripts/setup.sh` — idempotent venv bootstrap.
- `scripts/pdf_extract.py` — text, tables, metadata, search.
- `scripts/pdf_render.py` — pages to PNG.
- `scripts/pdf_edit.py` — merge, split, rotate, encrypt, decrypt.
- `reference/creating-pdfs.md` — producing PDFs, branded and programmatic.

## Related skills

- **A brand skill**, if one is installed, owns your organization's design system. Use it
  when the PDF must be on-brand.
