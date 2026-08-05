#!/usr/bin/env bash
# Sign every Mach-O under the given roots with the Developer ID Application identity.
#
# Shared by the two macOS artifacts that need it, because "which files are code?" is
# exactly the question that rots when answered in two places — and answering it wrong
# is what makes notarization reject an entire submission over one missed .dylib:
#   1. installers/macos/build-pkg.sh  — the .pkg payload
#   2. the standalone keld-agent-sidecar_darwin_*.tar.gz, which since the pkg stopped
#      bundling the sidecar is the ONLY sidecar macOS users ever run.
#
# Detection is by CONTENT (`file`), not by extension: a PyInstaller bundle carries
# Mach-O objects with .so names, extensionless helpers, and frameworks, and any
# hand-maintained list of them silently goes stale.
#
# Usage: sign-macho.sh <root> [root...]     (identity from $APPLE_DEVELOPER_ID_APP)
set -euo pipefail

ID="${APPLE_DEVELOPER_ID_APP:-}"
if [ -z "$ID" ]; then
  # Unsigned-first: no identity configured is a supported build, not an error.
  echo "sign-macho: APPLE_DEVELOPER_ID_APP unset — skipping (unsigned build)"
  exit 0
fi
[ $# -gt 0 ] || { echo "usage: sign-macho.sh <root> [root...]" >&2; exit 2; }

# Deepest path first. Nested code must be signed BEFORE the executable that loads it:
# signing an outer binary seals the inner ones, so signing outside-in leaves the outer
# signature sealing stale inner signatures.
count=0
while IFS= read -r f; do
  codesign --force --options runtime --timestamp --sign "$ID" "$f"
  count=$((count + 1))
done < <(
  for root in "$@"; do
    [ -e "$root" ] || continue
    find "$root" -type f -exec sh -c 'file -b "$1" | grep -q "Mach-O"' _ {} \; -print
  done | awk -F/ '{print NF "\t" $0}' | sort -rn -k1,1 | cut -f2-
)
echo "sign-macho: signed $count Mach-O file(s) under: $*"
