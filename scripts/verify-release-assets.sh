#!/usr/bin/env bash
# Verify a published release carries the complete installer asset manifest.
#
# Why this exists: the v0.20.0 release published 9 of 10 assets because GitHub
# never acquired a runner for the linux-sidecar job. install.sh treats the sidecar
# as mandatory (scripts/install.sh:104-115) and exits 1, so every Linux curl|sh
# install hard-failed until the job was re-run four days later. Nothing gated
# publication on asset completeness.
#
# On an incomplete release this demotes it to prerelease, because GitHub's
# releases/latest API excludes prereleases and install.sh resolves its tag from
# that API — so demotion makes `latest` fall back to the last complete release and
# installs keep working without human intervention. The non-zero exit still turns
# the release run red so it gets fixed forward.
#
# Recovery: re-run the failed job, then `gh release edit <tag> --prerelease=false`.
#
# Spec: docs/superpowers/specs/2026-08-10-release-asset-completeness-gate-design.md
set -euo pipefail

TAG="${1:-}"
[ -n "$TAG" ] || { echo "usage: $(basename "$0") <tag>" >&2; exit 2; }

# The expected manifest, derived from what produces each file rather than copied
# off a known-good release:
#   goreleaser archives + checksum  -> checksums.txt, keld_<os>_<arch>.{tar.gz,zip}
#   installers.yml build, macOS leg -> keld-agent-sidecar_darwin_arm64.tar.gz, the pkg
#   installers.yml build, win leg   -> keld-setup.exe
#   installers.yml linux-sidecar    -> keld-agent-sidecar_linux_amd64.tar.gz
# windows/arm64 is `ignore`d in .goreleaser.yaml, and Linux arm64 intentionally
# ships no sidecar tarball — neither belongs here.
expected=(
  "checksums.txt"
  "keld_linux_amd64.tar.gz"
  "keld_linux_arm64.tar.gz"
  "keld_darwin_amd64.tar.gz"
  "keld_darwin_arm64.tar.gz"
  "keld_windows_amd64.zip"
  "keld-agent-sidecar_linux_amd64.tar.gz"
  "keld-agent-sidecar_darwin_arm64.tar.gz"
  "keld-${TAG}-arm64.pkg"
  "keld-setup.exe"
)

# Asset list comes from a fixture when KELD_VERIFY_ASSETS_JSON is set (offline
# tests), otherwise from the live release.
if [ -n "${KELD_VERIFY_ASSETS_JSON:-}" ]; then
  assets_json=$(cat "$KELD_VERIFY_ASSETS_JSON")
else
  assets_json=$(gh release view "$TAG" --json assets -q '.assets') \
    || { echo "verify-release-assets: no release found for tag $TAG" >&2; exit 2; }
fi

missing=()
for name in "${expected[@]}"; do
  size=$(printf '%s' "$assets_json" \
    | jq -r --arg n "$name" 'map(select(.name == $n)) | .[0].size // empty')
  if [ -z "$size" ]; then
    missing+=("$name — absent")
  elif [ "$size" -le 0 ]; then
    missing+=("$name — present but zero bytes")
  fi
done

if [ "${#missing[@]}" -eq 0 ]; then
  echo "✓ $TAG carries all ${#expected[@]} expected assets"
  exit 0
fi

{
  echo "✗ $TAG is missing ${#missing[@]} of ${#expected[@]} expected assets:"
  for m in "${missing[@]}"; do echo "    - $m"; done
} >&2

if [ "${KELD_VERIFY_DEMOTE:-}" = "1" ]; then
  echo "demoting $TAG to prerelease so releases/latest falls back to the last complete release" >&2
  gh release edit "$TAG" --prerelease >&2 \
    || echo "warning: demotion failed — $TAG is still serving latest while incomplete" >&2
else
  echo "(KELD_VERIFY_DEMOTE unset — release flags left untouched)" >&2
fi
exit 1
