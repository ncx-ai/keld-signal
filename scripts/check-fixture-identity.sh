#!/usr/bin/env bash
# Fast, CI-safe identity gate for sidecar/app/analysis/: extract the small, wholly-synthetic
# fixture corpus (sidecar/app/analysis/testdata/fixture-corpus/) and verify it fingerprints
# byte-identically to the committed baseline (fixture-identity-baseline.json).
#
# This is NOT the real gate. The real one (scripts/check_identity.py verify, run by hand) runs
# against ~/keld/refseries-context/frozen-corpus/: 531966 rows of real AI-coding-session
# transcripts that live on one laptop, outside the repo, and can never be committed (raw prompt
# text must never leave the device — AGENTS.md). That gate proves nothing to CI or to anyone
# else who touches app/analysis/; this one is the floor everyone else gets. See
# sidecar/app/analysis/testdata/build_fixture_corpus.py for exactly what the fixture does and
# does not cover.
#
# Requires: pandas, pyarrow, bashlex, wordfreq (no torch, no spaCy model — KELD_TERMS=0 below
# disables the spaCy NER pass entirely, which is what makes this reproducible across machines
# that may or may not have en_core_web_sm installed; see the fixture's own docstring for why).
#
#   PYTHON=~/.keld/study-venv/bin/python scripts/check-fixture-identity.sh
#   pip install pandas pyarrow bashlex wordfreq && scripts/check-fixture-identity.sh
set -euo pipefail
cd "$(dirname "$0")/.."

PY="${PYTHON:-python3}"
FIXTURE_ROOT="sidecar/app/analysis/testdata/fixture-corpus/projects"
BASELINE="sidecar/app/analysis/testdata/fixture-identity-baseline.json"
OUTDIR="$(mktemp -d)"
trap 'rm -rf "$OUTDIR"' EXIT

echo "extracting fixture corpus with $PY ..."
KELD_TERMS=0 "$PY" scripts/refseries.py extract \
  --roots "$FIXTURE_ROOT" \
  --repo-root /nonexistent/fixture-repo-root \
  --component-depth 3 \
  --workers 1 \
  --outdir "$OUTDIR"

"$PY" scripts/check_identity.py verify "$BASELINE" "$OUTDIR"
