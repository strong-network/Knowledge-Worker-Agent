---
name: xlsx
description: >-
  Read and inspect Excel (.xlsx) workbooks — list sheets, read cell ranges as TSV, show formulas
  rather than values, search across sheets, and summarise structure. Load this skill whenever a
  task involves reading, analysing, searching or auditing a spreadsheet: a pipeline export, pricing
  model, seat count, budget or data extract. Also load it when asked what a workbook contains or
  where a number comes from.
---

# XLSX — reading spreadsheets

## The one rule

**Never open a .xlsx with the `read` tool.** Knowledge Worker Agent's `pdf-guard` plugin drops non-image
attachments before they reach the model. Extract the cells you need as text.

## Setup — run once per config root

Needs `openpyxl`. The venv is shared with `pdf`, `pptx` and `docx`.
Derive the path from this skill's own location — never hardcode it.

```bash
SKILL_DIR="<this skill's directory, from its loaded location>"
CONFIG_ROOT="$(cd "$SKILL_DIR/../.." && pwd)"
VENV="$CONFIG_ROOT/.venv"

[ -x "$VENV/bin/python" ] || python3 -m venv "$VENV"
"$VENV/bin/python" -c "import openpyxl" 2>/dev/null || "$VENV/bin/python" -m pip install --quiet openpyxl
```

## Reading

```bash
X="$VENV/bin/python $SKILL_DIR/scripts/xlsx_extract.py"

$X book.xlsx --stats                  # sheets, dimensions, header rows — ALWAYS start here
$X book.xlsx --sheet Pipeline         # one sheet as TSV
$X book.xlsx --range A1:D20           # a specific range
$X book.xlsx --search "ISO 27001"     # find a value across every sheet
$X book.xlsx --formulas               # show formulas instead of computed values
$X book.xlsx --max-rows 500           # raise the 200-row cap
```

**Always run `--stats` first.** A workbook can hold hundreds of thousands of cells; dumping it
would swamp the conversation and tell you nothing. Stats gives sheet names, dimensions and header
rows, which is normally enough to decide what to read.

Output is capped at 200 rows per sheet and says so:

```
... 59 more row(s) — use --range or --max-rows to see them
```

Never treat that message as the end of the data.

## Values versus formulas

By default you get **cached computed values**. `--formulas` shows the formula text instead.

Use `--formulas` when the question is *where a number comes from* — a total that looks wrong, a
hardcoded override in a column of calculations, a reference to another workbook. That is usually
the interesting question about a spreadsheet.

Cached values come from the last time Excel calculated the file. If a workbook was generated
programmatically and never opened in Excel, computed values may be missing entirely — in that case
`--formulas` is the only way to see what the cells hold.

## Limits

- **Charts, pivot tables and conditional formatting are not extracted.**
- **Macros are not read or executed.**
- **`.xls` (legacy binary) and `.csv` are not handled here** — read a `.csv` directly, it is text.
- **Formatting is not visible.** A red cell reads the same as a black one.

## Rules

1. **Never `read` a .xlsx.** Extract the cells you need.
2. **`--stats` before anything else.** Do not dump a workbook blind.
3. **Check `--formulas`** before trusting a number you are asked to explain.
4. **Cite sheet and cell references** (`Pipeline!C4`), so the user can verify.
5. **Say when output was capped.** Never present a truncated read as the whole picture.

## Files in this skill

- `SKILL.md` — this file.
- `scripts/xlsx_extract.py` — stats, sheets, ranges, formulas, search.

## Related

- **`pdf`, `pptx`, `docx`** — the same read-first pattern for those formats.
