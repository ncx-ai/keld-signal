#!/usr/bin/env bash
# Fast CI gate: assert every sidecar/app/**/*.py (recursively, matching the same
# `find` build-freeze.sh uses) actually comes out PyArmor-processed, not plain
# source. This targets one specific regression class: a source file — usually a
# new subpackage — silently missing from the obfuscation pass while
# `make obfuscate-check`/`make freeze-check` still pass, because those exercise
# whether the frozen binary RUNS, not whether everything shipped in it is
# actually obfuscated. That's exactly how sidecar/app/analysis/ shipped as plain
# source: a non-recursive `app/*.py` glob silently missed a whole subpackage.
#
# Deliberately cheap: only python-minifier + pyarmor (no torch, no ~1.9GB model,
# no PyInstaller freeze), via `build-freeze.sh --obf-only`. This is NOT a
# substitute for `make obfuscate-check` — it never proves the frozen binary
# spawns/runs — only that obfuscation coverage over source files is complete.
set -euo pipefail
cd "$(dirname "$0")/.."
PY="${PYTHON:-python}"

rm -rf build/obf build/obf_pyarmor
KELD_OBFUSCATE=1 PYTHON="$PY" bash sidecar/build-freeze.sh --obf-only

fail=0

# serve.py
if ! grep -q '__pyarmor__' build/obf_pyarmor/serve.py 2>/dev/null; then
  echo "MISSING/NOT OBFUSCATED: serve.py"
  fail=1
fi

while IFS= read -r -d '' f; do
  rel="${f#sidecar/app/}"
  out="build/obf_pyarmor/app/$rel"
  if [ ! -f "$out" ]; then
    echo "MISSING FROM OBFUSCATED OUTPUT: app/$rel"
    fail=1
    continue
  fi
  if ! grep -q '__pyarmor__' "$out"; then
    echo "NOT OBFUSCATED (no pyarmor marker): app/$rel"
    fail=1
    continue
  fi
  if diff -q "$f" "$out" >/dev/null 2>&1; then
    echo "NOT OBFUSCATED (byte-identical to source): app/$rel"
    fail=1
  fi
done < <(find sidecar/app -name '*.py' -not -name 'test_*.py' -print0)

if [ "$fail" != "0" ]; then
  echo "FAIL: obfuscation coverage gap — see above" >&2
  exit 1
fi
echo "PASS: every sidecar/app/**/*.py (excl. test_*.py) is obfuscated in the shipped tree"
