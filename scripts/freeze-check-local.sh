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
