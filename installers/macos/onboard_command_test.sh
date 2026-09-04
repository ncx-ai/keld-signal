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

# ── AC-3 / AC-4: the sidecar is replaced on VERSION MISMATCH, not merely fetched
# when absent ─────────────────────────────────────────────────────────────────
#
# ⚠️ THESE ARE THE FIRST EXECUTABLE CASES IN THIS FILE, and the gap they close is
# the reason the defect shipped: every assertion above is a grep over the script's
# text, so a skip condition that was wrong in SUBSTANCE rather than in wording was
# invisible to all of them. The presence-only skip kept an Aug 11 sidecar under a
# 2.3.0 daemon for ~3 weeks; blocks stopped, doctor stayed green.
#
# The two functions are extracted rather than sourced because onboard.command IS
# the onboarding flow — sourcing it would run a login. Both close on a column-0
# brace, which is what makes the extraction exact.
#
# What is asserted is the DECISION (fetch, or skip), observed through a stubbed
# curl. The swap that follows is unchanged by this work and is the same
# download → verify → extract → swap install.sh already performs.
sidecar_fns="$(awk '/^sidecar_installed_version\(\) \{/,/^\}/' "$cmd"
               awk '/^fetch_sidecar\(\) \{/,/^\}/' "$cmd")"

run_case() {
  # $1 = installed VERSION content ("" = no VERSION file at all), $2 = pkg VERSION
  ( set +e
    tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
    HOME="$tmp/home"; PREFIX="$tmp/prefix"; REPO="ncx-ai/keld-signal"
    export HOME PREFIX REPO
    mkdir -p "$HOME/.local/bin/keld-agent-sidecar" "$PREFIX"
    : > "$HOME/.local/bin/keld-agent-sidecar/keld-agent-sidecar"
    chmod +x "$HOME/.local/bin/keld-agent-sidecar/keld-agent-sidecar"
    [ -n "$1" ] && printf '%s\n' "$1" > "$HOME/.local/bin/keld-agent-sidecar/VERSION"
    printf '%s\n' "$2" > "$PREFIX/VERSION"
    eval "$sidecar_fns"
    # Apple-Silicon gate: stubbed so the case runs on any CI arch.
    uname() { echo arm64; }
    # Records the attempt and fails, so the decision is observable without a
    # 190MB download or a stubbed tar/shasum/mv chain.
    curl() { echo "FETCHED" >> "$tmp/calls"; return 1; }
    fetch_sidecar > "$tmp/out" 2>&1
    printf '%s|%s' "$(grep -c FETCHED "$tmp/calls" 2>/dev/null || echo 0)" "$(cat "$tmp/out")"
  )
}

r="$(run_case "v2.3.0" "v2.3.0")"
case "$r" in
  0\|*already\ current*) ;;
  *) echo "AC-3: a CURRENT sidecar was not skipped — got: $r"; exit 1 ;;
esac

r="$(run_case "v2.2.1" "v2.3.0")"
case "$r" in
  0\|*) echo "AC-3: a STALE sidecar was not replaced — got: $r"; exit 1 ;;
  *replacing\ it*) ;;
  *) echo "AC-3: a STALE sidecar was not replaced — got: $r"; exit 1 ;;
esac

r="$(run_case "" "v2.3.0")"
case "$r" in
  0\|*) echo "AC-4: an UNVERSIONED sidecar was not replaced — got: $r"; exit 1 ;;
  *unversioned*) ;;
  *) echo "AC-4: an UNVERSIONED sidecar was not replaced — got: $r"; exit 1 ;;
esac

echo "onboard checks passed"
