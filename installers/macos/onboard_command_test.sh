#!/usr/bin/env bash
set -euo pipefail
d="$(cd "$(dirname "$0")" && pwd)"
cmd="$d/onboard.command"
test -f "$cmd" || { echo "missing onboard.command"; exit 1; }
head -1 "$cmd" | grep -q '^#!' || { echo "no shebang"; exit 1; }
# onboard.command invokes keld/keld-agent via $KELD/$AGENT path variables rather
# than literal binary names, so match on those calls (fixed-string) instead of the
# literal command names.
# ⚠️ **BOTH SIDES OF A MERGE FIXED THE SAME STALENESS, and this is the union rather than
# either.** These assertions had gone stale unnoticed — the script is not wired into CI — because
# they pinned the OLD shape (`$KELD login` then `keld signal setup --yes`), which onboard.command
# stopped using when `keld-agent install` took ownership of login -> setup -> service.
#
# Main's half is the one that matters and this branch did not have it: match against the script
# with COMMENTS STRIPPED, because the previous version passed its `login --code` assertion against
# a comment and so kept passing after the real call went away. A test that can be satisfied by
# prose is not a test.
#
# This branch's half that main lacked: the `ingest_token` check. Redeeming a code is not the
# contract — reporting success from OBSERVED STATE is, and without this the script may claim it
# worked while hook.json holds nothing.
code_only="$(sed 's/#.*//' "$cmd")"
printf '%s' "$code_only" | grep -qF 'install --code "$CODE"' \
  || { echo "no code redeem via agent install"; exit 1; }
printf '%s' "$code_only" | grep -qF 'install --yes' \
  || { echo "no interactive fallback via agent install"; exit 1; }
printf '%s' "$code_only" | grep -qF '"$AGENT" install' \
  || { echo "no agent install"; exit 1; }
printf '%s' "$code_only" | grep -qF 'ingest_token' \
  || { echo "claims success without checking hook.json"; exit 1; }
grep -q 'KeldSetup' "$d/build-pkg.sh" && { echo "build-pkg still refs KeldSetup"; exit 1; } || true
grep -q 'KeldSetup.app' "$d/scripts/postinstall" && { echo "postinstall still refs app"; exit 1; } || true
grep -q 'onboard.command' "$d/scripts/postinstall" || { echo "postinstall does not open onboard.command"; exit 1; }
echo "onboard checks passed"
