#!/usr/bin/env bash
# Executable cases for scripts/check-sidecar-stamp.sh — AC-1's real gate.
#
# The point of this file is that the gate is EXERCISED somewhere outside a
# release. The four lines it replaced lived inside installers.yml, where nothing
# in the repo could run them, guarding the one stamp that a chain built to end a
# silent failure depends on.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
gate="$here/check-sidecar-stamp.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
fails=0

# build_tarball <name> [version]  — omit the version to build an UNSTAMPED tree,
# which is what a freeze that lost the stamp actually produces.
build_tarball() {
  name="$1"; version="${2-}"
  root="$work/build-$name"
  rm -rf "$root"; mkdir -p "$root/keld-agent-sidecar/_internal"
  echo "binary" > "$root/keld-agent-sidecar/keld-agent-sidecar"
  # A decoy under _internal/, so a gate that looked there instead of at the tree
  # root would pass this case and ship a tarball onboard.command cannot read.
  echo "v9.9.9" > "$root/keld-agent-sidecar/_internal/VERSION"
  [ -n "$version" ] && printf '%s\n' "$version" > "$root/keld-agent-sidecar/VERSION"
  tar -C "$root" -czf "$work/$name.tar.gz" keld-agent-sidecar
  echo "$work/$name.tar.gz"
}

check() {
  desc="$1"; want="$2"; shift 2
  "$@" > "$work/out" 2>&1
  got=$?
  if [ "$got" != "$want" ]; then
    echo "FAIL: $desc — exit $got, want $want"
    sed 's/^/    /' "$work/out"
    fails=1
  else
    echo "PASS: $desc"
  fi
}

stamped=$(build_tarball stamped v2.3.0)
unstamped=$(build_tarball unstamped)
empty=$(build_tarball empty "")

check "a correctly stamped tarball passes"            0 bash "$gate" "$stamped" v2.3.0
check "a stamped tarball passes with no tag (dry run)" 0 bash "$gate" "$stamped"
check "an UNSTAMPED tarball fails"                     1 bash "$gate" "$unstamped" v2.3.0
check "an unstamped tarball fails on a dry run too"    1 bash "$gate" "$unstamped"
check "a tarball stamped for another tag fails"        1 bash "$gate" "$stamped" v9.9.9
check "an empty VERSION fails"                         1 bash "$gate" "$empty" v2.3.0
check "a missing tarball is a usage error, not a pass" 2 bash "$gate" "$work/nope.tar.gz" v2.3.0
check "no argument is a usage error"                   2 bash "$gate"

# The stamp must be read from the TREE ROOT. The decoy at _internal/VERSION says
# v9.9.9, so a gate reading the wrong path would report that instead — and the
# whole reason the stamp is at the root is that onboard.command, a shell script,
# reads it there.
if bash "$gate" "$unstamped" > "$work/decoy" 2>&1; then
  echo "FAIL: the gate accepted a tree whose only VERSION is under _internal/ —"
  echo "      onboard.command cannot read that path, so the tarball is unusable"
  fails=1
else
  echo "PASS: a VERSION under _internal/ does not satisfy the gate"
fi

# The reported version is the file's content, with the writer's trailing newline
# absorbed — a newline compared literally against a tag is skew that is not there.
if ! bash "$gate" "$stamped" v2.3.0 | grep -q 'stamped v2.3.0'; then
  echo "FAIL: the gate does not report the version it read"
  fails=1
else
  echo "PASS: the gate reports the version it read"
fi

[ "$fails" = 0 ] || { echo; echo "check-sidecar-stamp: FAILURES"; exit 1; }
echo
echo "check-sidecar-stamp: all checks passed"
