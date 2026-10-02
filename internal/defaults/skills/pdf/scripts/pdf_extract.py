#!/usr/bin/env python3
# Copyright 2026 Citrix Systems, Inc.
# SPDX-License-Identifier: Apache-2.0
"""Extract text, tables and metadata from a PDF as TEXT.

Never attaches the PDF itself to the conversation -- see the skill's
"Never read a PDF directly" rule. Output is plain text on stdout.

Usage:
  pdf_extract.py FILE.pdf                    # metadata + all text
  pdf_extract.py FILE.pdf --pages 1,3-5      # only those pages (1-based)
  pdf_extract.py FILE.pdf --tables           # tables as TSV
  pdf_extract.py FILE.pdf --search TERM      # pages containing TERM
  pdf_extract.py FILE.pdf --meta             # metadata only
  pdf_extract.py FILE.pdf --password PW      # encrypted source
"""
import argparse
import sys


def parse_pages(spec, total):
    if not spec:
        return list(range(total))
    out = []
    for part in spec.split(","):
        part = part.strip()
        if "-" in part:
            a, b = part.split("-", 1)
            out.extend(range(int(a) - 1, int(b)))
        else:
            out.append(int(part) - 1)
    return [p for p in out if 0 <= p < total]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("file")
    ap.add_argument("--pages")
    ap.add_argument("--tables", action="store_true")
    ap.add_argument("--search")
    ap.add_argument("--meta", action="store_true")
    ap.add_argument("--password")
    a = ap.parse_args()

    from pypdf import PdfReader

    reader = PdfReader(a.file)
    if reader.is_encrypted:
        if not a.password:
            sys.exit("PDF is encrypted. Re-run with --password.")
        if reader.decrypt(a.password) == 0:
            sys.exit("Wrong password.")

    total = len(reader.pages)
    meta = {k.lstrip("/"): str(v) for k, v in (reader.metadata or {}).items()}
    print(f"# {a.file}")
    print(f"pages: {total}")
    for k, v in meta.items():
        print(f"{k.lower()}: {v}")
    if a.meta:
        return

    pages = parse_pages(a.pages, total)

    if a.search:
        hits = []
        for i in pages:
            if a.search.lower() in (reader.pages[i].extract_text() or "").lower():
                hits.append(i + 1)
        print(f"\n## search: {a.search!r}")
        print("pages: " + (", ".join(map(str, hits)) if hits else "no matches"))
        return

    if a.tables:
        import pdfplumber

        with pdfplumber.open(a.file, password=a.password or "") as pdf:
            for i in pages:
                for n, tbl in enumerate(pdf.pages[i].extract_tables(), 1):
                    print(f"\n## page {i + 1} table {n}")
                    for row in tbl:
                        print("\t".join("" if c is None else str(c).replace("\n", " ") for c in row))
        return

    for i in pages:
        print(f"\n## page {i + 1}")
        print((reader.pages[i].extract_text() or "").strip() or "(no extractable text)")


if __name__ == "__main__":
    main()
