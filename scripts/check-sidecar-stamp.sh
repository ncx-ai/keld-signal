#!/usr/bin/env bash
# Assert a built sidecar tarball carries the VERSION stamp, and that it names
# the release being built.
#
#   scripts/check-sidecar-stamp.sh <tarball> [expected-tag]
#
# ⚠️ WHY THIS IS A SCRIPT AND NOT FOUR LINES INSIDE installers.yml. It was those
# four lines first, and a review pointed out the obvious: the one link protecting
# a chain built to end a SILENT failure was itself protected by nothing. Nothing
# in this repo executes a workflow file, and installers.yml's own comment records
# what that costs — obfuscation broke on 2026-08-23 and the pipeline sat
# unbuildable for about a week "because nothing ran this workflow in between".
# Out here it has scripts/check-sidecar-stamp_test.sh beside it, and the workflow
# calls this instead of restating it.
#
# ⚠️ WHAT AN UNSTAMPED TARBALL COSTS, so nobody is tempted to downgrade this to a
# warning: `onboard.command` reads a missing VERSION as "stale" and re-downloads
# ~300MB on every single run, while the daemon reads it as "cannot tell" and goes
# back to saying nothing at all — which is precisely the silence the stamp
# exists to end. A release is the last place that can catch it, because after
# publication the only detector is a person noticing their blocks stopped. That
# took ~3 weeks last time. See
# docs/superpowers/specs/2026-09-04-sidecar-version-skew-discovery.md.
#
# The tag argument is OPTIONAL because a dry run has no tag: build-freeze.sh then
# stamps "dev", which is a legitimate build. Presence is still required there —
# a dry run that produces an unstamped tree would ship one the day it becomes a
# real release.
set -euo pipefail

tarball="${1:-}"
expected="${2:-}"

[ -n "$tarball" ] || { echo "usage: $(basename "$0") <tarball> [expected-tag]" >&2; exit 2; }
[ -f "$tarball" ] || { echo "check-sidecar-stamp: no such tarball: $tarball" >&2; exit 2; }

# The stamp sits at the ROOT of the tree, beside the executable — not under
# _internal/, where PyInstaller puts everything handed to it as `datas`. That is
# load-bearing rather than tidy: onboard.command reads the same path from shell,
# so a PyInstaller release moving _internal/ must not be able to turn every
# version comparison into "no version". Asserting the exact path here is what
# keeps the two readers agreeing.
if ! stamped=$(tar -xzOf "$tarball" keld-agent-sidecar/VERSION 2>/dev/null); then
  echo "check-sidecar-stamp: $tarball has no keld-agent-sidecar/VERSION." >&2
  echo "  Without it the installer re-downloads ~300MB on every run and the daemon" >&2
  echo "  cannot tell the two halves apart — the silent failure the stamp ends." >&2
  echo "  Fix: build-freeze.sh writes it; check KELD_VERSION reached the freeze step." >&2
  exit 1
fi

stamped=$(printf '%s' "$stamped" | tr -d ' \n')
[ -n "$stamped" ] || { echo "check-sidecar-stamp: VERSION is empty in $tarball" >&2; exit 1; }

if [ -n "$expected" ] && [ "$stamped" != "$expected" ]; then
  echo "check-sidecar-stamp: $tarball is stamped '$stamped' but this release is '$expected'." >&2
  echo "  A tarball attached to one tag while claiming another makes every downstream" >&2
  echo "  comparison wrong in a way no reader can see." >&2
  exit 1
fi

echo "check-sidecar-stamp: $tarball stamped $stamped"
