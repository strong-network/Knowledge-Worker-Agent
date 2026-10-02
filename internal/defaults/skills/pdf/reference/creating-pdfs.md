# Creating PDFs

Two routes. Pick by whether the output needs to look designed.

| Need | Route |
|---|---|
| Branded, or anything a customer sees | **HTML → PDF via Chromium** |
| Generated reports, data tables, invoices, many pages | **reportlab** |

---

## Route 1 — HTML → PDF via Chromium (preferred for anything visual)

Author HTML and CSS, then print it to PDF with the Chromium that Playwright already installs.
Layout, fonts and colour all behave the way they do in a browser, which is far easier to control
than a drawing API.

**For branded output, load your organization's brand skill first, if one is installed** — it owns the brand
tokens, fonts and asset paths. Use its stylesheets rather than inventing styling.

```bash
CHROME=$(find "$HOME/.cache/ms-playwright" -path '*chromium-*' -name chrome -type f | head -1)

"$VENV/bin/python" - <<PY
from playwright.sync_api import sync_playwright
with sync_playwright() as p:
    b = p.chromium.launch(executable_path="$CHROME", args=['--no-sandbox'])
    pg = b.new_page()
    pg.goto("file://$PWD/working/document.html")
    pg.pdf(path="$PWD/Report.pdf", format="A4", print_background=True,
           margin={"top":"18mm","bottom":"18mm","left":"16mm","right":"16mm"})
    b.close()
PY
```

Notes that save time:

- Use the **full `chromium-*/chrome-linux64/chrome`** binary. The `chromium_headless_shell-*`
  build does not reliably expose a `chrome` executable.
- `print_background=True` is required or every background colour disappears.
- `~` is not expanded by the browser — pass **absolute** paths in `file://` URLs and in `src`
  attributes.
- Page breaks: `page-break-after: always` (or `break-after: page`) on a block element.
- Repeating headers on long tables: `<thead>` repeats automatically when a table spans pages.
- For a fixed-size canvas (a one-page slide-style PDF) set `width`/`height` in `pg.pdf()` instead
  of `format`, matching your HTML canvas exactly.

---

## Route 2 — reportlab (programmatic documents)

Better when content is generated from data and length is unpredictable. `SimpleDocTemplate` with
flowables handles pagination for you.

```python
from reportlab.lib.pagesizes import A4
from reportlab.lib.styles import getSampleStyleSheet
from reportlab.lib import colors
from reportlab.platypus import (SimpleDocTemplate, Paragraph, Spacer,
                                Table, TableStyle, PageBreak)

doc = SimpleDocTemplate("Report.pdf", pagesize=A4,
                        title="Account Review", author="Acme Corp")
s = getSampleStyleSheet()
story = [
    Paragraph("Account Review", s["Title"]),
    Paragraph("Prepared 2026-08-14.", s["BodyText"]),
    Spacer(1, 12),
]

rows = [["Product", "Seats", "Cost"],
        ["Acme Workspace", "1200", "$450k"]]
t = Table(rows, repeatRows=1)
t.setStyle(TableStyle([
    ("GRID", (0, 0), (-1, -1), 0.5, colors.grey),
    ("BACKGROUND", (0, 0), (-1, 0), colors.lightgrey),
]))
story += [t, PageBreak(), Paragraph("Appendix", s["Heading1"])]

doc.build(story)
```

- `repeatRows=1` repeats the header row across page breaks.
- Set `title`/`author` on the document — they land in PDF metadata and are visible to anyone who
  inspects the file.
- For page numbers, pass `onPage` callbacks to `doc.build(...)` and draw into the canvas.

---

## Always verify before delivering

A PDF that was written is not a PDF that is correct. Check it:

```bash
# Did the text land as intended?
"$VENV/bin/python" "$SKILL_DIR/scripts/pdf_extract.py" Report.pdf --pages 1

# Does it look right?
"$VENV/bin/python" "$SKILL_DIR/scripts/pdf_render.py" Report.pdf --pages 1 --out working
```

Then read the PNG and actually look at it. Text extraction confirms content; only the image
catches clipping, overflow, missing fonts and broken layout.
