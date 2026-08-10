#!/usr/bin/env bash
set -euo pipefail
d="$(cd "$(dirname "$0")" && pwd)"
cmd="$d/onboard.command"
test -f "$cmd" || { echo "missing onboard.command"; exit 1; }
head -1 "$cmd" | grep -q '^#!' || { echo "no shebang"; exit 1; }
# onboard.command invokes keld/keld-agent via $KELD/$AGENT path variables rather
# than literal binary names, so match on those calls (fixed-string) instead of the
# literal command names.
# Match against the script with comments stripped: the previous version of this
# test passed its `login --code` assertion against a COMMENT, so it kept passing
# after onboard.command stopped calling `keld login` directly.
code_only="$(sed 's/#.*//' "$cmd")"
# onboard.command delegates the whole flow to `keld-agent install`, which owns
# login -> signal setup -> service (see the comment block above line 68).
printf '%s' "$code_only" | grep -qF 'install --code "$CODE"' \
  || { echo "no code redeem via agent install"; exit 1; }
printf '%s' "$code_only" | grep -qF 'install --yes' \
  || { echo "no interactive fallback via agent install"; exit 1; }
printf '%s' "$code_only" | grep -qF '"$AGENT" install' \
  || { echo "no agent install"; exit 1; }
grep -q 'KeldSetup' "$d/build-pkg.sh" && { echo "build-pkg still refs KeldSetup"; exit 1; } || true
grep -q 'KeldSetup.app' "$d/scripts/postinstall" && { echo "postinstall still refs app"; exit 1; } || true
grep -q 'onboard.command' "$d/scripts/postinstall" || { echo "postinstall does not open onboard.command"; exit 1; }
echo "onboard checks passed"
