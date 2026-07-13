#!/usr/bin/env bash
set -euo pipefail
d="$(cd "$(dirname "$0")" && pwd)"
cmd="$d/onboard.command"
test -f "$cmd" || { echo "missing onboard.command"; exit 1; }
head -1 "$cmd" | grep -q '^#!' || { echo "no shebang"; exit 1; }
# onboard.command invokes keld/keld-agent via $KELD/$AGENT path variables rather
# than literal binary names, so match on those calls (fixed-string) instead of the
# literal command names.
grep -qF 'login --code' "$cmd" || { echo "no code redeem"; exit 1; }
grep -qF '"$KELD" login ||' "$cmd" || { echo "no interactive login fallback"; exit 1; }
grep -qF 'signal setup --yes' "$cmd" || { echo "no setup --yes"; exit 1; }
grep -qF '"$AGENT" install' "$cmd" || { echo "no agent install"; exit 1; }
grep -q 'KeldSetup' "$d/build-pkg.sh" && { echo "build-pkg still refs KeldSetup"; exit 1; } || true
grep -q 'KeldSetup.app' "$d/scripts/postinstall" && { echo "postinstall still refs app"; exit 1; } || true
grep -q 'onboard.command' "$d/scripts/postinstall" || { echo "postinstall does not open onboard.command"; exit 1; }
echo "onboard checks passed"
