#!/usr/bin/env python3
# Copyright 2026 Citrix Systems, Inc.
# SPDX-License-Identifier: Apache-2.0
"""Read a .docx: text with heading structure, tables, comments, tracked changes.

Returns TEXT. The document is never attached to the conversation -- the
pdf-guard plugin drops non-image attachments, and a .docx would be dropped anyway.

Usage:
  docx_extract.py FILE.docx                 # outline + body text
  docx_extract.py FILE.docx --outline       # headings only (start here on a long doc)
  docx_extract.py FILE.docx --tables        # tables as TSV
  docx_extract.py FILE.docx --comments      # existing review comments
  docx_extract.py FILE.docx --revisions     # tracked insertions/deletions
  docx_extract.py FILE.docx --stats         # structure summary
  docx_extract.py FILE.docx --images --out working
"""
import argparse
import os
import re
import zipfile

W = "{http://schemas.openxmlformats.org/wordprocessingml/2006/main}"


def style_of(p):
    try:
        return (p.style.name or "").strip()
    except Exception:
        return ""


def heading_level(p):
    s = style_of(p)
    m = re.match(r"Heading (\d+)", s)
    if m:
        return int(m.group(1))
    if s == "Title":
        return 0
    return None


def iter_block_items(doc):
    """Body paragraphs and tables, in document order."""
    from docx.table import Table
    from docx.text.paragraph import Paragraph

    body = doc.element.body
    for child in body.iterchildren():
        if child.tag == W + "p":
            yield Paragraph(child, doc)
        elif child.tag == W + "tbl":
            yield Table(child, doc)


def xml_comments(path):
    """python-docx has no comments API; read word/comments.xml directly."""
    import xml.etree.ElementTree as ET

    out = []
    with zipfile.ZipFile(path) as z:
        if "word/comments.xml" not in z.namelist():
            return out
        root = ET.fromstring(z.read("word/comments.xml"))
        for c in root.findall(W + "comment"):
            author = c.get(W + "author", "?")
            date = c.get(W + "date", "")
            text = "".join(t.text or "" for t in c.iter(W + "t")).strip()
            out.append((author, date, text))
    return out


def xml_revisions(path):
    """Tracked changes: w:ins (insertions) and w:del (deletions)."""
    import xml.etree.ElementTree as ET

    ins, dele = [], []
    with zipfile.ZipFile(path) as z:
        root = ET.fromstring(z.read("word/document.xml"))
        for e in root.iter(W + "ins"):
            t = "".join(x.text or "" for x in e.iter(W + "t")).strip()
            if t:
                ins.append((e.get(W + "author", "?"), t))
        for e in root.iter(W + "del"):
            t = "".join(x.text or "" for x in e.iter(W + "delText")).strip()
            if t:
                dele.append((e.get(W + "author", "?"), t))
    return ins, dele


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("file")
    ap.add_argument("--outline", action="store_true")
    ap.add_argument("--tables", action="store_true")
    ap.add_argument("--comments", action="store_true")
    ap.add_argument("--revisions", action="store_true")
    ap.add_argument("--stats", action="store_true")
    ap.add_argument("--images", action="store_true")
    ap.add_argument("--out", default="working")
    a = ap.parse_args()

    from docx import Document
    from docx.table import Table

    doc = Document(a.file)
    print(f"# {os.path.basename(a.file)}")

    cp = doc.core_properties
    for k in ("title", "author", "last_modified_by", "created", "modified"):
        v = getattr(cp, k, None)
        if v:
            print(f"{k}: {v}")

    blocks = list(iter_block_items(doc))
    paras = [b for b in blocks if not isinstance(b, Table)]
    tables = [b for b in blocks if isinstance(b, Table)]
    comments = xml_comments(a.file)
    ins, dele = xml_revisions(a.file)
    words = sum(len(p.text.split()) for p in paras)

    print(f"paragraphs: {len(paras)}  tables: {len(tables)}  words: ~{words}")
    if comments:
        print(f"comments: {len(comments)}")
    if ins or dele:
        print(f"tracked changes: {len(ins)} insertion(s), {len(dele)} deletion(s)")

    if a.stats:
        return

    if a.images:
        os.makedirs(a.out, exist_ok=True)
        stem = os.path.splitext(os.path.basename(a.file))[0]
        n = 0
        with zipfile.ZipFile(a.file) as z:
            for item in z.namelist():
                if item.startswith("word/media/"):
                    n += 1
                    dest = os.path.join(a.out, f"{stem}-{os.path.basename(item)}")
                    with open(dest, "wb") as fh:
                        fh.write(z.read(item))
                    print(dest)
        if not n:
            print("no embedded images")
        return

    if a.comments:
        print()
        if not comments:
            print("no comments in this document")
        for author, date, text in comments:
            print(f"[{author} {date[:10]}] {text}")
        return

    if a.revisions:
        print()
        if not (ins or dele):
            print("no tracked changes")
        for author, t in ins:
            print(f"+ [{author}] {t}")
        for author, t in dele:
            print(f"- [{author}] {t}")
        return

    if a.tables:
        for n, tbl in enumerate(tables, 1):
            print(f"\n## table {n}")
            for row in tbl.rows:
                print("\t".join(c.text.strip().replace("\n", " ") for c in row.cells))
        if not tables:
            print("\nno tables")
        return

    print()
    t_i = 0
    for b in blocks:
        if isinstance(b, Table):
            t_i += 1
            if not a.outline:
                print(f"\n[table {t_i}: {len(b.rows)}x{len(b.columns)} — use --tables]")
            continue
        text = b.text.strip()
        lvl = heading_level(b)
        if lvl is not None:
            print(f"\n{'#' * (lvl + 1)} {text}")
        elif text and not a.outline:
            print(text)


if __name__ == "__main__":
    main()
