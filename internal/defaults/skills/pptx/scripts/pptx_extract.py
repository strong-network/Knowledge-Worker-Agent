#!/usr/bin/env python3
# Copyright 2026 Citrix Systems, Inc.
# SPDX-License-Identifier: Apache-2.0
"""Read a PPTX: outline, text, speaker notes, tables, embedded images.

Returns TEXT. The deck is never attached to the conversation -- the
pdf-guard plugin drops non-image attachments, and a .pptx would be dropped anyway.
To SEE a slide, use --images and read the extracted PNG.

Usage:
  pptx_extract.py DECK.pptx                  # outline: per-slide text + notes
  pptx_extract.py DECK.pptx --slides 1,3-5   # only those slides (1-based)
  pptx_extract.py DECK.pptx --notes          # speaker notes only
  pptx_extract.py DECK.pptx --tables         # tables as TSV
  pptx_extract.py DECK.pptx --images --out working   # embedded images -> PNG
  pptx_extract.py DECK.pptx --stats          # structure summary only
"""
import argparse
import os
import sys

EMU_IN = 914400.0
PICTURE = 13
GROUP = 6


def parse_slides(spec, total):
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


def iter_shapes(shapes):
    """Flatten groups so nested text is not missed."""
    for sh in shapes:
        if sh.shape_type == GROUP:
            for inner in iter_shapes(sh.shapes):
                yield inner
        else:
            yield sh


def shape_text(sh):
    if not getattr(sh, "has_text_frame", False):
        return ""
    return sh.text_frame.text.strip()


def notes_of(slide):
    if not slide.has_notes_slide:
        return ""
    return slide.notes_slide.notes_text_frame.text.strip()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("file")
    ap.add_argument("--slides")
    ap.add_argument("--notes", action="store_true")
    ap.add_argument("--tables", action="store_true")
    ap.add_argument("--images", action="store_true")
    ap.add_argument("--stats", action="store_true")
    ap.add_argument("--out", default="working")
    a = ap.parse_args()

    from pptx import Presentation

    prs = Presentation(a.file)
    total = len(prs.slides)
    slides = list(prs.slides)
    sel = parse_slides(a.slides, total)

    print(f"# {os.path.basename(a.file)}")
    print(f"slides: {total}")
    print(f"size: {prs.slide_width / EMU_IN:.2f} x {prs.slide_height / EMU_IN:.2f} in")

    core = prs.core_properties
    for k in ("title", "author", "last_modified_by", "created", "modified"):
        v = getattr(core, k, None)
        if v:
            print(f"{k}: {v}")

    flat = []
    for i in sel:
        s = slides[i]
        texts = [t for t in (shape_text(sh) for sh in iter_shapes(s.shapes)) if t]
        pics = sum(1 for sh in iter_shapes(s.shapes) if sh.shape_type == PICTURE)
        if not texts and pics:
            flat.append(i + 1)

    if flat:
        print(f"\n! slides {flat} have no text — they are flat images.")
        print("! Re-run with --images to extract them, then read the PNG to see them.")

    if a.stats:
        print()
        for i in sel:
            s = slides[i]
            shp = list(iter_shapes(s.shapes))
            print(f"  slide {i + 1}: shapes={len(shp)} "
                  f"text={sum(1 for x in shp if shape_text(x))} "
                  f"pictures={sum(1 for x in shp if x.shape_type == PICTURE)} "
                  f"tables={sum(1 for x in shp if getattr(x, 'has_table', False))} "
                  f"notes={'yes' if notes_of(s) else 'no'}")
        return

    if a.images:
        os.makedirs(a.out, exist_ok=True)
        stem = os.path.splitext(os.path.basename(a.file))[0]
        n = 0
        for i in sel:
            for j, sh in enumerate(iter_shapes(slides[i].shapes), 1):
                if sh.shape_type != PICTURE:
                    continue
                img = sh.image
                path = os.path.join(a.out, f"{stem}-s{i + 1}-{j}.{img.ext}")
                with open(path, "wb") as fh:
                    fh.write(img.blob)
                print(f"{path}  {img.size[0]}x{img.size[1]}")
                n += 1
        if not n:
            print("no embedded images found")
        return

    if a.tables:
        found = 0
        for i in sel:
            for sh in iter_shapes(slides[i].shapes):
                if not getattr(sh, "has_table", False):
                    continue
                found += 1
                print(f"\n## slide {i + 1} table {found}")
                for row in sh.table.rows:
                    print("\t".join(c.text.strip().replace("\n", " ") for c in row.cells))
        if not found:
            print("\nno tables found")
        return

    for i in sel:
        s = slides[i]
        print(f"\n## slide {i + 1}")
        if not a.notes:
            texts = [t for t in (shape_text(sh) for sh in iter_shapes(s.shapes)) if t]
            if texts:
                for t in texts:
                    print(t)
            else:
                print("(no text — flat image or empty)")
        note = notes_of(s)
        if note:
            print(f"\n[notes] {note}")
        elif a.notes:
            print("(no notes)")


if __name__ == "__main__":
    main()
