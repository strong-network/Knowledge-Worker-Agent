#!/usr/bin/env bash
# Copyright 2026 Citrix Systems, Inc.
# SPDX-License-Identifier: Apache-2.0
# Idempotent first-run setup for the pdf skill.
# Safe to run repeatedly. Creates (or reuses) the shared venv beside the
# skills/ directory and installs the PDF libraries.
#
#   bash scripts/setup.sh
#
# The venv must NEVER live inside the skill folder: that bloats the skill and
# breaks discovery. It is derived from the skill's own location so it works
# whether the skill was installed by a user or provisioned by the platform,
# and it is shared with the other document skills.
set -euo pipefail

SKILL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

case "$SKILL_DIR" in
  */.github/skills/*) ROOT="${SKILL_DIR%/.github/skills/*}" ;;
  */skills/*)         ROOT="${SKILL_DIR%/skills/*}" ;;
  *)                  ROOT="$PWD" ;;
esac

VENV="${VIRTUAL_ENV:-$ROOT/.venv}"

if [ ! -x "$VENV/bin/python" ]; then
  echo ">> Creating venv at $VENV"
  python3 -m venv "$VENV"
fi

echo ">> Using venv: $VENV"
"$VENV/bin/python" -m pip install --quiet --upgrade pip >/dev/null 2>&1 || true
"$VENV/bin/python" -m pip install --quiet pypdf pdfplumber pypdfium2 reportlab pillow

"$VENV/bin/python" - <<'PY'
import importlib.metadata as md
for p in ["pypdf", "pdfplumber", "pypdfium2", "reportlab", "pillow"]:
    print("   %-11s %s" % (p, md.version(p)))
PY

echo ">> pdf skill ready."
echo "   VENV=$VENV"
