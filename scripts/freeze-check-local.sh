#!/usr/bin/env bash
# Local mirror of the CI freeze + worker-spawn acceptance gate (Linux). Freezes
# the sidecar (plain by default; obfuscated when KELD_OBFUSCATE=1), starts the
# frozen binary, and POSTs a real /classify — which spawns the worker child that
# must import the (possibly obfuscated) modules from inside the frozen bundle.
#
# This is the ONLY test that exercises frozen-distribution worker spawn: unit
# tests run under a normal interpreter and never freeze, and a /health-only smoke
# never spawns a worker. Both `make freeze-check` (plain) and `make
# obfuscate-check` (obfuscated) route here.
#
# Heavy: freezes (~bundles torch) + loads the model. Minutes, CPU-bound. Reuses
# the sidecar venv (already has fastapi/torch/gliner2) so it doesn't reinstall the
# ~GB runtime deps; build-freeze.sh freezes from a COPY so the tree is never
# clobbered.
set -euo pipefail
cd "$(dirname "$0")/.."
VENV="${SIDECAR_VENV:-$HOME/.keld/sidecar-venv}"
PY="$VENV/bin/python"
OBF="${KELD_OBFUSCATE:-0}"
PORT="${PORT:-8408}"
MODEL="${KELD_GLINER2_DIR:-$HOME/.keld/models/gliner2-large-v1}"
LABEL="plain"; [ "$OBF" = "1" ] && LABEL="obfuscated"

echo "== [$LABEL] install build tools into $VENV =="
"$PY" -m pip install --quiet pyinstaller
[ "$OBF" = "1" ] && "$PY" -m pip install --quiet python-minifier pyarmor

echo "== [$LABEL] freeze (KELD_OBFUSCATE=$OBF) =="
KELD_OBFUSCATE="$OBF" PYTHON="$PY" bash sidecar/build-freeze.sh

BIN="dist/keld-agent-sidecar/keld-agent-sidecar"
[ -x "$BIN" ] || { echo "FAIL: frozen binary not found at $BIN"; exit 1; }

echo "== [$LABEL] spawn acceptance gate: run the frozen sidecar + real /classify =="
KELD_GLINER2_DIR="$MODEL" "$BIN" --port "$PORT" --host 127.0.0.1 >/tmp/freeze-check-sidecar.log 2>&1 &
SPID=$!
trap 'kill $SPID 2>/dev/null || true' EXIT
for i in $(seq 1 120); do curl -sf "http://127.0.0.1:$PORT/health" | grep -q '"ok"' && break; sleep 2; done
resp=$(curl -sf -m 90 -X POST "http://127.0.0.1:$PORT/classify" -H 'Content-Type: application/json' \
  -d '{"text":"debug the login bug","tasks":{"task_type":["debug","other"]}}') \
  || { echo "FAIL [$LABEL]: frozen worker classify failed (spawn/import/bundle broke?)"; echo "--- sidecar log ---"; tail -30 /tmp/freeze-check-sidecar.log; exit 1; }
echo "$resp" | grep -q '"task_type"' \
  || { echo "FAIL [$LABEL]: classify returned no result: $resp"; exit 1; }
echo "PASS [$LABEL]: frozen sidecar spawns a worker and returns: $resp"

# /pii is the second import path this gate has to cover, and /classify cannot
# stand in for it: presidio and phonenumbers are pulled in lazily from inside
# app/pii.py's engine builder, so they are invisible to PyInstaller's analysis
# and only a REAL scan through the frozen bundle proves the spec collected them.
# Same failure class as the worker spawn above — green everywhere except the
# thing we ship. The fixtures are synthetic: an SSN valid under every SSA rule
# and on no published example list (a documentation constant would be
# suppressed by app/wellknown.py and prove nothing).
echo "== [$LABEL] frozen /pii gate: presidio + phonenumbers must import from the bundle =="
pii=$(curl -sf -m 120 -X POST "http://127.0.0.1:$PORT/pii" -H 'Content-Type: application/json' \
  -d '{"text":"update the record, ssn 321-54-9876, and call (415) 682-4470"}') \
  || { echo "FAIL [$LABEL]: frozen /pii failed (presidio/phonenumbers not collected?)"; echo "--- sidecar log ---"; tail -30 /tmp/freeze-check-sidecar.log; exit 1; }
echo "$pii" | grep -q '"ssn"' \
  || { echo "FAIL [$LABEL]: /pii answered but found no ssn — the analyzer built without its recognizers: $pii"; exit 1; }
echo "PASS [$LABEL]: frozen sidecar scans for PII and returns: $pii"

# THE VERIFIER ARM. The third import path this gate has to cover, and the reason it
# exists at all: the attribution verifier shipped for a whole branch with `llama_cpp`
# absent from the PyInstaller spec, and nothing could see it. llama_cpp is a ctypes
# binding — its ~10 native ggml/llama shared libraries are opened by a path computed at
# import time, so PyInstaller's binary analysis cannot follow them — and the one import
# statement that reaches it sits inside `Verifier.__init__`, which under KELD_OBFUSCATE=1
# is PyArmor-encrypted bytecode. So the module and its libraries are BOTH invisible, unit
# tests never freeze, and /classify and /pii above do not touch this path: the binary
# starts, is healthy, classifies, scans, and fails every verifier verdict.
#
# `--selftest verifier` (serve.py) spawns the real verifier worker child through
# multiprocessing-spawn — i.e. by re-execing THIS frozen binary — and takes one real
# verdict. Nothing is stubbed.
#
# It needs the GGUF, and it FAILS rather than skips without one: a gate that passes
# quietly on the machines that lack the model is the same as no gate. Point
# KELD_VERIFIER_GGUF at a copy, or provision ~/.keld/models/gemma-4-e2b/model.gguf.
# KELD_FREEZE_CHECK_VERIFIER=0 waives it deliberately (and says so).
if [ "${KELD_FREEZE_CHECK_VERIFIER:-1}" = "0" ]; then
  echo "SKIP [$LABEL]: verifier arm waived by KELD_FREEZE_CHECK_VERIFIER=0 — this run does NOT prove llama_cpp was frozen"
else
  echo "== [$LABEL] frozen verifier gate: llama_cpp + its native libs must load in the worker child =="
  "$BIN" --selftest verifier \
    || { echo "FAIL [$LABEL]: the frozen verifier child could not return a verdict (llama_cpp/ggml libs not collected?)"; exit 1; }
  echo "PASS [$LABEL]: frozen sidecar spawns the verifier child and returns a verdict"
fi
