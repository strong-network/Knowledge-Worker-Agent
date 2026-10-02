#!/usr/bin/env python3
# Copyright 2026 Citrix Systems, Inc.
# SPDX-License-Identifier: Apache-2.0
"""Insert real Word margin comments into a .docx.

Produces comments Word shows in the review pane and margin -- not text
annotations. python-docx has no API for this, so the OOXML parts are written
directly: word/comments.xml, the content-type override, the document
relationship, and commentRangeStart/End + commentReference in the body.

Usage:
  docx_comment.py IN.docx OUT.docx COMMENTS.json [--author "Name"] [--initials XX]

COMMENTS.json is a list of objects:
  [
    {"anchor": "verbatim phrase from the document",
     "comment": "What to fix, and the concrete fix.",
     "category": "FACT",          # optional, prefixed to the comment
     "occurrence": 1}             # optional, 1-based; use when the phrase repeats
  ]

The anchor must be an exact substring of the document text. Anchors are matched
against each paragraph's full text, so a phrase split across formatting runs
(bold in the middle, a stray spell-check split) still matches -- the run is
split as needed.

Always regenerate the output from the original. Do not run this twice against
its own output to stack comments; pass all comments in one file.

Exit status is 0 even when some anchors do not match; the unmatched list is
printed so the caller can shorten them and re-run.
"""
import argparse
import copy
import json
import os
import re
import shutil
import sys
import zipfile
from xml.etree import ElementTree as ET

W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
NS = {"w": W}
ET.register_namespace("w", W)

CT = "http://schemas.openxmlformats.org/package/2006/content-types"
REL = "http://schemas.openxmlformats.org/package/2006/relationships"
COMMENTS_RT = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/comments"
COMMENTS_CT = ("application/vnd.openxmlformats-officedocument"
               ".wordprocessingml.comments+xml")


def q(tag):
    return f"{{{W}}}{tag}"


def runs_of(para):
    """Direct w:r children that carry text, with their text."""
    out = []
    for r in para.findall(q("r")):
        t = r.find(q("t"))
        if t is not None:
            out.append((r, t, t.text or ""))
    return out


def split_run(para, run, tnode, at):
    """Split a run so the text before `at` stays and the rest moves to a new run."""
    text = tnode.text or ""
    head, tail = text[:at], text[at:]
    tnode.text = head
    tnode.set("{http://www.w3.org/XML/1998/namespace}space", "preserve")
    new = copy.deepcopy(run)
    nt = new.find(q("t"))
    nt.text = tail
    nt.set("{http://www.w3.org/XML/1998/namespace}space", "preserve")
    para.insert(list(para).index(run) + 1, new)
    return new


def anchor_runs(para, anchor, occurrence):
    """Isolate `anchor` inside `para` into whole runs. Returns (first, last) or None."""
    rs = runs_of(para)
    if not rs:
        return None
    full = "".join(t for _, _, t in rs)
    start = -1
    idx = 0
    for _ in range(occurrence):
        start = full.find(anchor, idx)
        if start < 0:
            return None
        idx = start + 1
    end = start + len(anchor)

    # Walk runs, splitting at the anchor boundaries.
    pos = 0
    first = last = None
    i = 0
    while i < len(rs):
        run, tnode, text = rs[i]
        rstart, rend = pos, pos + len(text)
        if rend <= start or rstart >= end:
            pos = rend
            i += 1
            continue
        if rstart < start < rend:                       # split off the head
            split_run(para, run, tnode, start - rstart)
            rs = runs_of(para)
            pos = 0
            i = 0
            continue
        if rstart < end < rend:                         # split off the tail
            split_run(para, run, tnode, end - rstart)
            rs = runs_of(para)
            pos = 0
            i = 0
            continue
        if first is None:
            first = run
        last = run
        pos = rend
        i += 1
    return (first, last) if first is not None else None


def make_comment_el(cid, author, initials, date, text):
    c = ET.Element(q("comment"), {
        q("id"): str(cid), q("author"): author,
        q("initials"): initials, q("date"): date})
    p = ET.SubElement(c, q("p"))
    r = ET.SubElement(p, q("r"))
    t = ET.SubElement(r, q("t"))
    t.text = text
    t.set("{http://www.w3.org/XML/1998/namespace}space", "preserve")
    return c


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("infile")
    ap.add_argument("outfile")
    ap.add_argument("comments")
    ap.add_argument("--author", default="Reviewer")
    ap.add_argument("--initials", default="RV")
    a = ap.parse_args()

    with open(a.comments, encoding="utf-8") as fh:
        items = json.load(fh)
    if not isinstance(items, list):
        sys.exit("comments file must be a JSON list")

    zin = zipfile.ZipFile(a.infile)
    names = zin.namelist()
    doc = ET.fromstring(zin.read("word/document.xml"))

    # Reuse the existing comments part if the document already has one.
    if "word/comments.xml" in names:
        comments_root = ET.fromstring(zin.read("word/comments.xml"))
        existing = [int(c.get(q("id"), "0")) for c in comments_root.findall(q("comment"))]
        next_id = max(existing) + 1 if existing else 1
    else:
        comments_root = ET.Element(q("comments"))
        next_id = 1

    date = "2026-01-01T00:00:00Z"
    try:
        import datetime
        date = datetime.datetime.now().strftime("%Y-%m-%dT%H:%M:%SZ")
    except Exception:
        pass

    paras = doc.iter(q("p"))
    para_list = list(paras)
    applied, unmatched = 0, []

    for item in items:
        anchor = (item.get("anchor") or "").strip()
        body = (item.get("comment") or "").strip()
        if not anchor or not body:
            unmatched.append(anchor or "(empty anchor)")
            continue
        cat = (item.get("category") or "").strip()
        if cat:
            body = f"[{cat}] {body}"
        occ = int(item.get("occurrence") or 1)

        hit = None
        for para in para_list:
            hit = anchor_runs(para, anchor, occ)
            if hit:
                target = para
                break
        if not hit:
            unmatched.append(anchor)
            continue

        first, last = hit
        cid = next_id
        next_id += 1
        kids = list(target)
        start_el = ET.Element(q("commentRangeStart"), {q("id"): str(cid)})
        end_el = ET.Element(q("commentRangeEnd"), {q("id"): str(cid)})
        ref_run = ET.Element(q("r"))
        rpr = ET.SubElement(ref_run, q("rPr"))
        ET.SubElement(rpr, q("rStyle"), {q("val"): "CommentReference"})
        ET.SubElement(ref_run, q("commentReference"), {q("id"): str(cid)})

        target.insert(kids.index(first), start_el)
        kids = list(target)
        pos = kids.index(last) + 1
        target.insert(pos, end_el)
        target.insert(pos + 1, ref_run)

        comments_root.append(
            make_comment_el(cid, a.author, a.initials, date, body))
        applied += 1

    # ---- repackage -------------------------------------------------------
    ct = zin.read("[Content_Types].xml").decode("utf-8")
    if "comments+xml" not in ct:
        ct = ct.replace("</Types>",
                        f'<Override PartName="/word/comments.xml" '
                        f'ContentType="{COMMENTS_CT}"/></Types>')

    rels = zin.read("word/_rels/document.xml.rels").decode("utf-8")
    if COMMENTS_RT not in rels:
        rid = "rIdComments%d" % (len(re.findall(r'Id="', rels)) + 1)
        rels = rels.replace("</Relationships>",
                            f'<Relationship Id="{rid}" Type="{COMMENTS_RT}" '
                            f'Target="comments.xml"/></Relationships>')

    doc_xml = ET.tostring(doc, encoding="UTF-8", xml_declaration=True)
    com_xml = ET.tostring(comments_root, encoding="UTF-8", xml_declaration=True)

    tmp = a.outfile + ".tmp"
    with zipfile.ZipFile(tmp, "w", zipfile.ZIP_DEFLATED) as zout:
        for item in zin.infolist():
            if item.filename in ("word/document.xml", "[Content_Types].xml",
                                 "word/_rels/document.xml.rels", "word/comments.xml"):
                continue
            zout.writestr(item, zin.read(item.filename))
        zout.writestr("[Content_Types].xml", ct)
        zout.writestr("word/_rels/document.xml.rels", rels)
        zout.writestr("word/document.xml", doc_xml)
        zout.writestr("word/comments.xml", com_xml)
    zin.close()
    shutil.move(tmp, a.outfile)

    print(f"applied: {applied}")
    print(f"unmatched: {len(unmatched)}")
    for u in unmatched:
        print(f"  ! {u[:70]}")
    if unmatched:
        print("Shorten each unmatched anchor to an exact verbatim substring "
              "and re-run against the ORIGINAL file.")
    print(f"wrote {a.outfile}")


if __name__ == "__main__":
    main()
