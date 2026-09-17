#!/usr/bin/env bash
# Build the smallest venv that can run THIS WORKTREE's analysis service.
#
#   scripts/conformance/analysis-venv.sh <dir>     # prints the python path
#
# ⚠️ **WHY THIS EXISTS: A RELEASED SIDECAR CANNOT TEST A SIDECAR CHANGE.** The
# conformance chain downloads the frozen sidecar from a release, which is the
# right thing for proving a shipped artifact and useless for proving the branch
# under review. Measured twice in one day:
#
#   - `blocks` could never pass: v3.0.3's sidecar predates devblocks.py
#     entirely, so no env var could make it cut a block, and the one signal
#     Atlas RENDERS stayed untested;
#   - Codex's `store_rows` could never pass: the analysis reader that resolves a
#     Codex prompt id is new in this branch.
#
# Both were declared "not expected" against a frozen sidecar, which is honest
# and leaves the code unexercised. This runs the worktree's own.
#
# ⚠️ **KNOWN GAP: presidio-analyzer IS EXCLUDED, SO /pii ANSWERS 500 ON EVERY
# PROMPT AND `sensitivity`'s SCAN HALF IS NEVER EXERCISED BY CONFORMANCE.**
# Measured on a 3-tool chain A: 95 `pii scan failed: ModuleNotFoundError` lines
# in one run. A 5xx is TRANSIENT by `retry.IsTransient`, so every enrichment job
# RETRIES a call that can never succeed and pays the backoff — one job measured
# 87 seconds from the watcher's offer to the publish, which is what forced
# SETTLE up to 180s. On a real machine presidio IS installed and none of this
# applies, so it is a harness cost, not a product defect.
#
# Measured cost of closing it: `pip install presidio-analyzer` takes **16s** and
# brings the venv to **187 MB** (it pulls the spaCy LIBRARY; the sidecar needs no
# spaCy MODEL since the NER came out — see AGENTS.md). That is affordable, and
# the assertion at the bottom of this script would catch a broken install
# loudly at venv-build time rather than mid-chain. Deliberately NOT done in the
# same change as the block/Gemini fixes, so a green matrix means what it says.

# ⚠️ **IT NEEDS NO ML STACK, AND THAT IS WHAT MAKES IT AFFORDABLE.** The sidecar
# is an analysis service that loads GLiNER2 lazily on a first inference the
# conformance chain never issues, so torch, transformers, spacy, presidio and
# llama-cpp are all dead weight here: ~5 GB and minutes against six wheels and
# seconds. Verified by importing app.main with only these installed.
#
# The heavy set is EXCLUDED BY NAME rather than the light set listed, so a new
# lightweight dependency is picked up automatically and only a new HEAVY one
# needs a line here. The import check below is what catches either mistake.
set -euo pipefail

DIR=${1:?usage: analysis-venv.sh <dir>}
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
REQ="$ROOT/sidecar/requirements.txt"
[ -f "$REQ" ] || { echo "no $REQ" >&2; exit 2; }

PY=${KELD_CONFORM_PYTHON_BASE:-python3}
command -v "$PY" >/dev/null || { echo "no $PY on PATH" >&2; exit 2; }

# Anything that pulls a model runtime. Each is here because it is GIGABYTES, not
# because it is unused — the service simply never reaches the code that needs it
# during a conformance run.
HEAVY='gliner2|transformers|spacy|en_core_web_sm|presidio-analyzer|llama-cpp-python'

if [ ! -x "$DIR/bin/python" ]; then
  "$PY" -m venv "$DIR" >&2
fi
LIGHT=$(grep -vE "^\s*#|^\s*$" "$REQ" | grep -vE "^($HEAVY)")
# shellcheck disable=SC2086
"$DIR/bin/pip" -q install $LIGHT >&2

# ⚠️ ASSERTED, not assumed. If a light dependency starts importing a heavy one,
# or a new heavy one is added without a line above, this fails HERE with the
# missing module named — rather than three steps later as a sidecar that will
# not start.
( cd "$ROOT/sidecar" && PYTHONPATH=. "$DIR/bin/python" -c "import app.main" ) \
  || { echo "analysis-venv.sh: the worktree's app.main does not import with the light set — a heavy dep crept in" >&2; exit 1; }

printf '%s\n' "$DIR/bin/python"
