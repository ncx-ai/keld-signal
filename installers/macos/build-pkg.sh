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

# Stage the Terminal onboarding script into the payload (executable).
cp "$ROOT/onboard.command" "$STAGE/onboard.command"
chmod +x "$STAGE/onboard.command"

# Optional codesign of the Mach-O binaries (hardened runtime) when a signing identity is present.
# EVERY Mach-O in the payload must be signed, not just the three entrypoints: the frozen sidecar
# is a PyInstaller one-dir tree carrying hundreds of .so/.dylib files from torch et al, and
# notarization rejects the whole submission if any one of them is unsigned. Nested code signs
# inside-out (dependencies before the executables that load them), so the three top-level
# binaries are signed last.
if [ -n "${APPLE_DEVELOPER_ID_APP:-}" ]; then
  sign() { codesign --force --options runtime --timestamp --sign "$APPLE_DEVELOPER_ID_APP" "$1"; }
  while IFS= read -r f; do
    sign "$f"
  done < <(find "$STAGE/keld-agent-sidecar" -type f ! -name keld-agent-sidecar \
             -exec sh -c 'file -b "$1" | grep -q "Mach-O"' _ {} \; -print)
  for b in keld keld-agent keld-agent-sidecar/keld-agent-sidecar; do
    sign "$STAGE/$b"
  done
  # Verify rather than trust: an unsigned or badly-sealed binary otherwise surfaces much
  # later as an opaque notarization rejection.
  for b in keld keld-agent keld-agent-sidecar/keld-agent-sidecar; do
    codesign --verify --strict --verbose=2 "$STAGE/$b"
  done
fi

# Build component pkg into a temp dir so the final pkg glob never catches it and
# productbuild doesn't scan the repo root.
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pkgbuild --root "$STAGE" --install-location /usr/local/keld \
  --scripts "$ROOT/scripts" --identifier co.keld.agent --version "$VERSION" "$TMP/component.pkg"

PB=(productbuild --distribution "$ROOT/distribution.xml" --resources "$ROOT/../resources" --package-path "$TMP" "$OUT")
if [ -n "${APPLE_DEVELOPER_ID_INSTALLER:-}" ]; then
  PB+=(--sign "$APPLE_DEVELOPER_ID_INSTALLER")
fi
"${PB[@]}"
if [ -n "${APPLE_DEVELOPER_ID_INSTALLER:-}" ]; then
  pkgutil --check-signature "$OUT"
fi

# Notarize + staple when notarytool creds are present.
if [ -n "${APPLE_NOTARY_KEY:-}" ] && [ -n "${APPLE_NOTARY_KEY_ID:-}" ] && [ -n "${APPLE_NOTARY_ISSUER:-}" ]; then
  # `--key` takes a PATH to the App Store Connect .p8, but a CI secret naturally holds the key
  # CONTENTS — accept either, materializing contents into the trap-cleaned temp dir.
  KEYFILE="$APPLE_NOTARY_KEY"
  if [ ! -f "$KEYFILE" ]; then
    KEYFILE="$TMP/notary.p8"
    (umask 077; printf '%s\n' "$APPLE_NOTARY_KEY" > "$KEYFILE")
  fi
  xcrun notarytool submit "$OUT" --key "$KEYFILE" --key-id "$APPLE_NOTARY_KEY_ID" \
    --issuer "$APPLE_NOTARY_ISSUER" --wait
  xcrun stapler staple "$OUT"
fi
echo "built $OUT"
