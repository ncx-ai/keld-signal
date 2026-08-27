#!/usr/bin/env bash
set -euo pipefail
d="$(cd "$(dirname "$0")" && pwd)"
cmd="$d/onboard.command"
test -f "$cmd" || { echo "missing onboard.command"; exit 1; }
head -1 "$cmd" | grep -q '^#!' || { echo "no shebang"; exit 1; }
# onboard.command invokes keld/keld-agent via $KELD/$AGENT path variables rather
# than literal binary names, so match on those calls (fixed-string) instead of the
# literal command names.
# ⚠️ THESE ASSERTIONS WENT STALE AND NOTHING NOTICED, because this script is not
# wired into CI. They pinned the OLD shape — a direct `$KELD login` followed by
# `keld signal setup --yes` — which onboard.command stopped using when
# `keld-agent install` took ownership of login -> setup -> service (see AGENTS.md,
# macOS onboarding). The behaviour they exist to protect is unchanged; only the
# command that performs it moved. Assert the CONTRACT, not the spelling.
grep -qF 'install --code' "$cmd" || { echo "no setup-code redeem"; exit 1; }
grep -qF 'install --yes'  "$cmd" || { echo "no browser-login fallback when the code fails"; exit 1; }
grep -qF 'ingest_token'   "$cmd" || { echo "claims success without checking hook.json"; exit 1; }
grep -q 'KeldSetup' "$d/build-pkg.sh" && { echo "build-pkg still refs KeldSetup"; exit 1; } || true
grep -q 'KeldSetup.app' "$d/scripts/postinstall" && { echo "postinstall still refs app"; exit 1; } || true
grep -q 'onboard.command' "$d/scripts/postinstall" || { echo "postinstall does not open onboard.command"; exit 1; }
echo "onboard checks passed"
