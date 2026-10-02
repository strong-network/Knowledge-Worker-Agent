#!/usr/bin/env python3
# Copyright 2026 Citrix Systems, Inc.
# SPDX-License-Identifier: Apache-2.0
"""Manipulate PDFs: merge, split, rotate, encrypt, decrypt.

Every operation writes a NEW file and never modifies the source in place.

Usage:
  pdf_edit.py merge   OUT.pdf  A.pdf B.pdf [...]
  pdf_edit.py split   IN.pdf   OUT.pdf --pages 1,3-5
  pdf_edit.py rotate  IN.pdf   OUT.pdf --angle 90 [--pages 1,2]
  pdf_edit.py encrypt IN.pdf   OUT.pdf --password PW
  pdf_edit.py decrypt IN.pdf   OUT.pdf --password PW
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


def opened(path, password=None):
    from pypdf import PdfReader

    r = PdfReader(path)
    if r.is_encrypted:
        if not password:
            sys.exit(f"{path} is encrypted. Pass --password.")
        if r.decrypt(password) == 0:
            sys.exit("Wrong password.")
    return r


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("op", choices=["merge", "split", "rotate", "encrypt", "decrypt"])
    ap.add_argument("args", nargs="+")
    ap.add_argument("--pages")
    ap.add_argument("--angle", type=int, default=90)
    ap.add_argument("--password")
    a = ap.parse_args()

    from pypdf import PdfWriter

    w = PdfWriter()

    if a.op == "merge":
        out, sources = a.args[0], a.args[1:]
        if not sources:
            sys.exit("merge needs at least one source file")
        for src in sources:
            for p in opened(src, a.password).pages:
                w.add_page(p)

    else:
        if len(a.args) != 2:
            sys.exit(f"{a.op} takes IN.pdf OUT.pdf")
        src, out = a.args
        r = opened(src, a.password)

        if a.op == "split":
            for i in parse_pages(a.pages, len(r.pages)):
                w.add_page(r.pages[i])
        elif a.op == "rotate":
            sel = set(parse_pages(a.pages, len(r.pages)))
            for i, p in enumerate(r.pages):
                if i in sel:
                    p.rotate(a.angle)
                w.add_page(p)
        else:  # encrypt / decrypt both re-write the page set
            for p in r.pages:
                w.add_page(p)
            if a.op == "encrypt":
                if not a.password:
                    sys.exit("encrypt needs --password")
                w.encrypt(a.password)

    with open(out, "wb") as fh:
        w.write(fh)
    print(f"wrote {out} ({len(w.pages)} pages)")


if __name__ == "__main__":
    main()
