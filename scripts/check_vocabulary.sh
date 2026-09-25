#!/bin/bash
# check_vocabulary.sh — fails when a retired name comes back (AC-11 of
# docs/superpowers/specs/2026-09-23-multi-group-attribution-discovery.html).
#
# Signal names things the way Atlas does: a GROUP holds PROJECTS, and the
# repo/branch/model facets are DIMENSIONS. The denylist is the exact set of
# names that were retired, so a hit is never a judgement call. A line may opt
# out with the marker `vocab:keep` when it deliberately reads an OLD stored
# name (a migration, a legacy fallback, the test that pins one).
#
# Usage: scripts/check_vocabulary.sh [ROOT]   (exit 0 clean, 1 on a hit)
set -u
ROOT=${1:-$(cd "$(dirname "$0")/.." && pwd)}
DENY="$(cd "$(dirname "$0")" && pwd)/vocabulary-denylist.txt"
cd "$ROOT" || exit 2

patterns=$(grep -v -E '^\s*(#|$)' "$DENY")
[ -n "$patterns" ] || { echo "check_vocabulary: empty denylist" >&2; exit 2; }

files=$(find internal cmd sidecar/app sidecar/serve.py ui/e2e scripts \
  \( -name node_modules -o -name testdata -o -name .venv -o -name __pycache__ \) -prune -o \
  -type f \( -name '*.go' -o -name '*.py' -o -name '*.js' -o -name '*.ts' -o -name '*.html' -o -name '*.css' -o -name '*.sh' \) \
  -print 2>/dev/null | grep -v -E '(^|/)(check_vocabulary(_test)?\.sh|vocabulary-denylist\.txt)$' | sort)

# A file whose whole job is the legacy names (the test pinning a migration)
# says so once with `vocab:keep-file` rather than marking every line.
files=$(printf '%s\n' "$files" | tr '\n' '\0' | xargs -0 grep -L 'vocab:keep-file' 2>/dev/null)

hits=$(printf '%s\n' "$files" | tr '\n' '\0' | xargs -0 grep -n -E -f <(printf '%s\n' "$patterns") 2>/dev/null | grep -v 'vocab:keep')
if [ -n "$hits" ]; then
  echo "check_vocabulary: retired names found (see scripts/vocabulary-denylist.txt):" >&2
  printf '%s\n' "$hits" >&2
  exit 1
fi
echo "check_vocabulary: clean ($(printf '%s\n' "$files" | wc -l | tr -d ' ') files)"
