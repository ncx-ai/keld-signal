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
  command -v pyarmor >/dev/null 2>&1 && "$PY" -c 'import python_minifier' >/dev/null 2>&1
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

"$PY" -m pip install --upgrade pip pyinstaller
"$PY" -m pip install -r sidecar/requirements.txt

if [ "$OBF" = "1" ]; then
  echo "obfuscating sidecar (locals-rename -> pyarmor)…"
  rm -rf build/obf build/obf_pyarmor
  mkdir -p build/obf/app
  # 1) locals-only rename. python-minifier renames locals by default and only
  #    renames globals with --rename-globals (omitted), so globals / Pydantic
  #    fields / spawn targets are preserved. Keep ALL annotations — Pydantic v2
  #    + FastAPI derive fields/DI from them (default minify would strip them).
  MIN_ARGS=(--no-remove-annotations --no-remove-variable-annotations \
            --no-remove-return-annotations --no-remove-argument-annotations)
  "$PY" -m python_minifier "${MIN_ARGS[@]}" -o build/obf/serve.py sidecar/serve.py
  for f in sidecar/app/*.py; do
    "$PY" -m python_minifier "${MIN_ARGS[@]}" -o "build/obf/app/$(basename "$f")" "$f"
  done
  # 2) PyArmor free-tier bytecode encryption over the renamed tree.
  pyarmor gen -O build/obf_pyarmor -r build/obf/app build/obf/serve.py
  # 3) overlay obfuscated app/ + serve.py + pyarmor_runtime onto the sidecar dir
  #    so the existing .spec freezes the obfuscated code unchanged. The CI/build
  #    checkout is disposable, so overwriting in place is fine.
  cp -R build/obf_pyarmor/. sidecar/
fi

pyinstaller --clean --noconfirm sidecar/keld-agent-sidecar.spec
echo "frozen -> dist/keld-agent-sidecar/ (obfuscated=$OBF)"
