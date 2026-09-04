#!/usr/bin/env bash
# Freeze the sidecar into dist/keld-agent-sidecar/. Run per-OS in CI (needs the
# target OS's Python 3.12). With KELD_OBFUSCATE=1 the shipped bytecode is
# locals-renamed (python-minifier) then encrypted (PyArmor free tier) before
# PyInstaller freezes it; unset builds plain. Hard-fails if obfuscation is
# requested but unavailable — never ships plain when asked to obfuscate.
#
# Debug the obfuscated Linux path locally with `make obfuscate-check`.
set -euo pipefail
cd "$(dirname "$0")/.."
OBF="${KELD_OBFUSCATE:-0}"
PY="${PYTHON:-python}"

_have_obf_tools() {
  [ -z "${PYARMOR_FORCE_MISSING:-}" ] || return 1   # test hook
  # Import-check under $PY (not `command -v`), so it works when the interpreter
  # is a venv whose bin/ isn't on PATH (e.g. the local `make obfuscate-check`).
  "$PY" -c 'import pyarmor, python_minifier' >/dev/null 2>&1
}

# Gate: if obfuscation is requested, the tools must be present. Fast + side-effect
# free so `--check` can test it without the heavy freeze.
if [ "$OBF" = "1" ] && ! _have_obf_tools; then
  echo "ERROR: KELD_OBFUSCATE=1 but python-minifier/pyarmor unavailable — refusing to ship plain code" >&2
  exit 1
fi
if [ "${1:-}" = "--check" ]; then
  echo "build-freeze gate ok (KELD_OBFUSCATE=$OBF)"; exit 0
fi

# --obf-only: run just the obfuscation transform (minify -> pyarmor) and stop —
# no requirements install, no PyInstaller freeze. Those two are the heavy part
# (torch + the ~1.9GB model + minutes of bundling) and need a provisioned
# sidecar venv; the transform itself needs only python-minifier/pyarmor, so a
# plain CI runner can use this to assert obfuscation *coverage* (every
# sidecar/app/**/*.py actually got transformed) without paying for a full
# freeze. See scripts/check-obfuscation-coverage.sh.
OBF_ONLY=0
if [ "${1:-}" = "--obf-only" ]; then
  [ "$OBF" = "1" ] || { echo "ERROR: --obf-only only makes sense with KELD_OBFUSCATE=1" >&2; exit 1; }
  OBF_ONLY=1
fi

if [ "$OBF_ONLY" != "1" ]; then
  "$PY" -m pip install --upgrade pip pyinstaller
  "$PY" -m pip install -r sidecar/requirements.txt
fi

SPEC="sidecar/keld-agent-sidecar.spec"
if [ "$OBF" = "1" ]; then
  echo "obfuscating sidecar (locals-rename -> pyarmor)…"
  rm -rf build/obf build/obf_pyarmor build/sidecar_obf
  mkdir -p build/obf/app
  # 1) locals-only rename. python-minifier renames locals by default and only
  #    renames globals with --rename-globals (omitted), so globals / Pydantic
  #    fields / spawn targets are preserved. Keep ALL annotations — Pydantic v2
  #    + FastAPI derive fields/DI from them (default minify would strip them).
  MIN_ARGS=(--no-remove-annotations --no-remove-variable-annotations \
            --no-remove-return-annotations --no-remove-argument-annotations)
  "$PY" -m python_minifier "${MIN_ARGS[@]}" -o build/obf/serve.py sidecar/serve.py
  # Recurse: app/ has subpackages (e.g. app/analysis/) whose .py files must be
  # obfuscated too — a plain `app/*.py` glob here previously missed them
  # entirely, so an entire subpackage shipped as plain source (the .spec's
  # hiddenimports walk is already recursive; this loop wasn't).
  #
  # ⚠️ test_*.py IS EXCLUDED, and processing it was a hard build failure rather
  # than the harmless waste this comment used to claim. Tests are 42 of the 53
  # files in app/, they never reach the frozen bundle (the .spec skips them in
  # hiddenimports) and the coverage gate already excludes them
  # (scripts/check-obfuscation-coverage.sh) — so obfuscating them bought nothing
  # and spent PyArmor's free-tier licence budget. Once the suite grew past the
  # cap, `ERROR out of license` failed the freeze on ALL THREE OSes, blocking the
  # release. This is now the same file set the .spec and the coverage gate use.
  while IFS= read -r -d '' f; do
    rel="${f#sidecar/app/}"
    mkdir -p "build/obf/app/$(dirname "$rel")"
    "$PY" -m python_minifier "${MIN_ARGS[@]}" -o "build/obf/app/$rel" "$f"
  done < <(find sidecar/app -name '*.py' -not -name 'test_*.py' -print0)
  # 2) PyArmor free-tier bytecode encryption over the renamed tree. pyarmor has
  #    no `python -m` entry, so use the console script beside $PY (venv case),
  #    falling back to PATH (CI, where pip put `pyarmor` on PATH).
  PYARMOR="$(dirname "$PY")/pyarmor"; [ -x "$PYARMOR" ] || PYARMOR="pyarmor"
  "$PYARMOR" gen -O build/obf_pyarmor -r build/obf/app build/obf/serve.py
  if [ "$OBF_ONLY" = "1" ]; then
    echo "obf-only: obfuscated tree at build/obf_pyarmor (skipping PyInstaller freeze)"
    exit 0
  fi
  # 3) freeze from a COPY so the working tree is never clobbered (matters for
  #    local runs; CI checkouts are disposable but this is correct either way).
  #    Overlay the obfuscated app/ + serve.py + pyarmor_runtime onto the copy and
  #    freeze the copied spec (its _here resolves to the copy).
  cp -R sidecar build/sidecar_obf
  cp -R build/obf_pyarmor/. build/sidecar_obf/
  SPEC="build/sidecar_obf/keld-agent-sidecar.spec"
fi

# `python -m PyInstaller` (not the bare console script) so it works whether $PY
# is a venv off PATH (local) or the CI python.
"$PY" -m PyInstaller --clean --noconfirm "$SPEC"

# ⚠️ STAMP THE TREE WITH ITS VERSION. The two halves of an install — keld-agent
# and this frozen sidecar — ship as separate artifacts on separate cadences, and
# until this file neither half nor a human could see that they disagreed. A
# 2.3.0 daemon ran ~3 weeks against an Aug 11 sidecar with no /blocks route,
# which 404'd, which the emitter read as "no blocks closed": zero blocks
# published, doctor green. See
# docs/superpowers/specs/2026-09-04-sidecar-version-skew-discovery.md.
#
# Written HERE rather than handed to PyInstaller as `datas`, and it must stay
# that way: `datas` land under _internal/, and the OTHER reader of this file is
# installers/macos/onboard.command — a shell script, comparing against the pkg's
# own VERSION to decide whether to re-fetch. Putting a PyInstaller layout detail
# in that comparison means a future PyInstaller moving _internal/ silently turns
# every comparison into "no version", which is the failure this ends.
#
# `dev` when nothing sets KELD_VERSION — a local freeze, `make freeze-check`, a
# fork's dry run. Both readers treat `dev` as "cannot tell" rather than as skew,
# so a developer machine is never nagged (internal/version.Skew, AC-9).
printf '%s\n' "${KELD_VERSION:-dev}" > dist/keld-agent-sidecar/VERSION
echo "frozen -> dist/keld-agent-sidecar/ (obfuscated=$OBF, version=${KELD_VERSION:-dev})"
