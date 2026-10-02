#!/usr/bin/env python3
# Copyright 2026 Citrix Systems, Inc.
# SPDX-License-Identifier: Apache-2.0
"""Render PDF pages to PNG so the model can actually SEE them.

This is the sanctioned way to look at a page: the pdf-guard plugin
drops non-image attachments, so a PDF can never be read directly, but PNGs
pass through normally.

Use when text extraction is not enough: scanned documents, charts, diagrams,
layout or design questions, or verifying a PDF you just produced.

Usage:
  pdf_render.py FILE.pdf                      # all pages -> ./working/
  pdf_render.py FILE.pdf --pages 1,4 --out DIR
  pdf_render.py FILE.pdf --scale 3            # higher DPI (default 2)
"""
import argparse
import os
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
    ap.add_argument("--out", default="working")
    ap.add_argument("--scale", type=float, default=2.0)
    ap.add_argument("--password")
    a = ap.parse_args()

    import pypdfium2 as pdfium

    doc = pdfium.PdfDocument(a.file, password=a.password)
    pages = parse_pages(a.pages, len(doc))
    os.makedirs(a.out, exist_ok=True)
    stem = os.path.splitext(os.path.basename(a.file))[0]

    if a.scale > 4:
        print("warning: scale > 4 produces very large images", file=sys.stderr)

    for i in pages:
        img = doc[i].render(scale=a.scale).to_pil()
        path = os.path.join(a.out, f"{stem}-p{i + 1}.png")
        img.save(path)
        print(f"{path}  {img.size[0]}x{img.size[1]}")


if __name__ == "__main__":
    main()
