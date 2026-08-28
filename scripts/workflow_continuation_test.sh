#!/usr/bin/env bash
set -o pipefail
# Guard: a `#` comment inside a backslash-continued shell command in a workflow
# `run:` block is NOT a comment — bash ends the command there.
#
# This shipped. installers.yml's linux-sidecar job had a ten-line explanation
# written between two continued `docker run` arguments, so docker received no
# image and answered "requires at least 1 argument". It is valid YAML, so the
# YAML parse passed; it is not a Go or Python file, so no unit gate looked at
# it; and the dry-run that would have caught it predated the merge that
# introduced it. The first thing to notice was the v2.0.0 release build failing
# and leaving the release without its Linux sidecar.
#
# A comment inside a line-continuation is not a comment; it is a truncation.
set -u

root="$(cd "$(dirname "$0")/.." && pwd)"
shopt -s nullglob
workflows=("$root"/.github/workflows/*.yml)
# A guard that inspects nothing must not report success. This one nearly did:
# its first run found no files (a bad path) and printed "ok (0 workflows)".
if [ "${#workflows[@]}" -eq 0 ]; then
  echo "workflow continuation guard: found NO workflows under $root/.github/workflows — refusing to pass" >&2
  exit 1
fi

fail=0
for wf in "${workflows[@]}"; do
  # State: prev_continues = the previous non-blank line ended in a backslash.
  prev_continues=0
  lineno=0
  while IFS= read -r line; do
    lineno=$((lineno + 1))
    trimmed="${line#"${line%%[![:space:]]*}"}"

    if [ "$prev_continues" = 1 ] && [ "${trimmed:0:1}" = "#" ]; then
      echo "$(basename "$wf"):$lineno: comment inside a line-continuation — bash ends the command here" >&2
      echo "    $trimmed" >&2
      fail=1
    fi

    # Blank lines inside a continuation would already have ended it, so only
    # non-blank lines update the state.
    if [ -n "$trimmed" ]; then
      case "$line" in
        *\\) prev_continues=1 ;;
        *)   prev_continues=0 ;;
      esac
    fi
  done < "$wf"
done

if [ "$fail" = 1 ]; then
  echo "" >&2
  echo "Move the comment ABOVE the \`run:\` key (or above the command), never between" >&2
  echo "two continued arguments." >&2
  exit 1
fi

echo "workflow continuation guard: ok (${#workflows[@]} workflow(s) scanned)"
