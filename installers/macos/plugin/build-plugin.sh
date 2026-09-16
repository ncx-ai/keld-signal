#!/usr/bin/env bash
# Build KeldSetup.bundle — the Installer.app wizard pane.
#
# Command Line Tools are enough: clang against InstallerPlugins.framework, no
# Xcode project and no nib (the section supplies its pane programmatically).
#
# Usage: build-plugin.sh <out-dir> <version> <keld-binary>
set -euo pipefail
OUT="${1:?output dir (the --plugins dir)}"
VERSION="${2:?version}"
KELD="${3:?path to the keld binary to embed}"
ROOT="$(cd "$(dirname "$0")" && pwd)"
SDK="$(xcrun --show-sdk-path)"

BUNDLE="$OUT/KeldSetup.bundle"
rm -rf "$BUNDLE"
mkdir -p "$BUNDLE/Contents/MacOS" "$BUNDLE/Contents/Resources"

clang -bundle -fobjc-arc -arch arm64 \
  -isysroot "$SDK" -mmacosx-version-min=12.0 \
  -framework Cocoa -framework WebKit -framework InstallerPlugins \
  -o "$BUNDLE/Contents/MacOS/KeldSetup" "$ROOT/KeldSetup.m"

cp "$ROOT/Info.plist" "$BUNDLE/Contents/Info.plist"
plutil -replace CFBundleShortVersionString -string "$VERSION" "$BUNDLE/Contents/Info.plist"

# The pane drives this copy: the payload is not installed when it runs.
cp "$KELD" "$BUNDLE/Contents/Resources/keld"
chmod +x "$BUNDLE/Contents/Resources/keld"

# ⚠️ THE ATLAS TARGET MUST BE BAKED IN; AN ENV VAR CANNOT REACH THE PANE.
# Installer.app inherits the environment of whatever launched it, but the plugin
# runs in InstallerRemotePluginService — an XPC service with a CLEAN
# environment. Measured 2026-09-14: the pane read KELD_API_URL=(unset) in the
# same run where Installer's own env dump showed it set, so `keld` silently used
# production. Stamping it here is what makes a dev-targeted build possible.
#
# Set at BUILD time, never at install time. A release build sets nothing, the key
# is absent, and the CLI uses its compiled-in default.
if [ -n "${KELD_API_URL:-}" ]; then
  plutil -replace KeldAPIURL -string "$KELD_API_URL" "$BUNDLE/Contents/Info.plist"
  echo "  ! plugin targets $KELD_API_URL (KELD_API_URL was set at build time) — NOT a release build"
fi

cp "$ROOT/InstallerSections.plist" "$OUT/InstallerSections.plist"

# ⚠️ SIGN AFTER BUILDING, THEN VERIFY. A bundle whose executable was recompiled
# after signing FAILS TO LOAD WITH NO DIAGNOSTIC AT ALL — no pane, no error
# dialog, nothing in `log show`. Measured 2026-09-14; it cost three runs.
if [ -n "${APPLE_DEVELOPER_ID_APP:-}" ]; then
  codesign --force --options runtime --timestamp \
    --sign "$APPLE_DEVELOPER_ID_APP" "$BUNDLE/Contents/Resources/keld"
  codesign --force --options runtime --timestamp \
    --sign "$APPLE_DEVELOPER_ID_APP" "$BUNDLE"
else
  # Unsigned-first, matching build-pkg.sh: ad-hoc so a LOCAL test still loads.
  codesign --force --sign - "$BUNDLE/Contents/Resources/keld"
  codesign --force --sign - "$BUNDLE"
fi
codesign --verify --strict --verbose=2 "$BUNDLE"
echo "built $BUNDLE"
