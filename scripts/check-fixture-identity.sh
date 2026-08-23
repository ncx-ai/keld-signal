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
# Requires (derived from what scripts/refseries.py and pandas' parquet I/O actually import/need
# — do not guess this list, re-derive it with `grep -n '^import\|^from' scripts/refseries.py`
# plus whatever pandas needs for to_parquet/read_parquet if that ever changes):
#   pandas    — the frame the extraction pipeline builds
#   numpy     — refseries.py: unconditional `import numpy as np`
#   pyyaml    — refseries.py: unconditional `import yaml` (module name `yaml`), used for a
#               Dumper representer at MODULE LOAD time — its absence crashes on import, before
#               a single transcript is read. Missing from this list once already (see git log);
#               that is exactly the failure mode the preflight below now catches by name instead
#               of letting it surface as a bare ModuleNotFoundError traceback.
#   pyarrow   — not a `refseries.py` import, but pandas' parquet engine: extract() calls
#               ev.to_parquet(...) and check_identity.py calls pd.read_parquet(...); without an
#               engine installed those raise at CALL time, not at import time.
#   bashlex   — sidecar/app/analysis/shell.py imports it in a try/except (optional at the
#               PACKAGE level: without it, `exe`/`verb`/`toolchain` silently fall back to a
#               naive parse instead of failing). This fixture's docker+entrypoint-sh turn only
#               proves what it's meant to prove WITH bashlex installed — required HERE even
#               though the package itself tolerates its absence.
#   wordfreq  — sidecar/app/analysis/terms.py imports it the same way (optional at the package
#               level: without it, the shouting filter is a silent no-op). Same reasoning: this
#               fixture's whole point of having a shouted word (TOP) is to prove the filter
#               fires, so it must actually be installed for the check to mean anything.
#   (No torch, no spaCy model — KELD_TERMS=0 below disables the spaCy NER pass entirely, which
#   is what makes this reproducible across machines that may or may not have en_core_web_sm
#   installed; see the fixture's own docstring for why.)
#
#   PYTHON=~/.keld/study-venv/bin/python scripts/check-fixture-identity.sh
#   pip install pandas numpy pyyaml pyarrow bashlex wordfreq && scripts/check-fixture-identity.sh
#
# NOTE for `make fixture-identity-check STUDY_PYTHON=...`: Make does NOT shell-expand a `~` in
# its own VAR=value arguments (unlike a plain shell `VAR=~/x cmd`, which does) — pass an already-
# expanded path (`STUDY_PYTHON=$HOME/.keld/study-venv/bin/python`), not a literal `~/...`, or
# Make will hand the check a nonexistent literal path.
set -euo pipefail
cd "$(dirname "$0")/.."

PY="${PYTHON:-python3}"
FIXTURE_ROOT="sidecar/app/analysis/testdata/fixture-corpus/projects"
BASELINE="sidecar/app/analysis/testdata/fixture-identity-baseline.json"

# Preflight: fail LOUD and NAMED on a missing dependency, before extraction ever runs. Without
# this, a missing bashlex/wordfreq doesn't error — it silently degrades the extraction (naive
# shell parsing, shouting never filtered) and the gate reports a plain DIFFERS on ref/exe or
# ref/term, which reads exactly like a real regression in sidecar/app/analysis/ and sends
# whoever's looking chasing a code bug that isn't there. A missing pandas/numpy/pyyaml/pyarrow
# crashes anyway (module-load or first-call ImportError) but with a bare traceback instead of a
# message that says what to install. Exit code 3 is used deliberately — distinct from
# check_identity.py's own 0 (identical) / 1 (differs) / 2 (harness error), so a caller can tell
# "this environment is missing a dependency" from either of those.
missing=""
for spec in "pandas:pandas" "numpy:numpy" "yaml:pyyaml" "pyarrow:pyarrow" \
            "bashlex:bashlex" "wordfreq:wordfreq"; do
  mod="${spec%%:*}"; pkg="${spec##*:}"
  if ! "$PY" -c "import $mod" >/dev/null 2>&1; then
    missing="$missing $pkg"
  fi
done
if [ -n "$missing" ]; then
  echo "check-fixture-identity: missing dependency:$missing" >&2
  echo "  this is an ENVIRONMENT problem, not a code regression in sidecar/app/analysis/ —" >&2
  echo "  install with: pip install$missing" >&2
  exit 3
fi

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
