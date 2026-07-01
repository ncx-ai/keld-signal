#!/usr/bin/env bash
# Build keld-<version>-<arch>.pkg from a staged payload dir. Signs + notarizes ONLY when
# the Developer ID / notarytool secrets are present; otherwise emits an unsigned
# pkg (unsigned-first). macOS-only (uses pkgbuild/productbuild/xcrun) — CI-verified.
set -euo pipefail
VERSION="${1:?version}"
STAGE="${2:?payload dir (contains keld, keld-agent, keld-agent-sidecar)}"
ARCH="${3:?arch}"
OUT="keld-${VERSION}-${ARCH}.pkg"
ROOT="$(cd "$(dirname "$0")" && pwd)"

# Optional codesign of the Mach-O binaries (hardened runtime) when a signing identity is present.
if [ -n "${APPLE_DEVELOPER_ID_APP:-}" ]; then
  for b in keld keld-agent keld-agent-sidecar/keld-agent-sidecar; do
    codesign --force --options runtime --timestamp --sign "$APPLE_DEVELOPER_ID_APP" "$STAGE/$b" || true
  done
fi

# Build component pkg into a temp dir so the final pkg glob never catches it and
# productbuild doesn't scan the repo root.
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pkgbuild --root "$STAGE" --install-location /usr/local/keld \
  --scripts "$ROOT/scripts" --identifier co.keld.agent --version "$VERSION" "$TMP/component.pkg"

PB=(productbuild --distribution "$ROOT/distribution.xml" --package-path "$TMP" "$OUT")
if [ -n "${APPLE_DEVELOPER_ID_INSTALLER:-}" ]; then
  PB+=(--sign "$APPLE_DEVELOPER_ID_INSTALLER")
fi
"${PB[@]}"

# Notarize + staple when notarytool creds are present.
if [ -n "${APPLE_NOTARY_KEY:-}" ] && [ -n "${APPLE_NOTARY_KEY_ID:-}" ] && [ -n "${APPLE_NOTARY_ISSUER:-}" ]; then
  xcrun notarytool submit "$OUT" --key "$APPLE_NOTARY_KEY" --key-id "$APPLE_NOTARY_KEY_ID" \
    --issuer "$APPLE_NOTARY_ISSUER" --wait
  xcrun stapler staple "$OUT"
fi
echo "built $OUT"
