#!/usr/bin/env bash
# Build KeldSetup.app into <stage-dir>. macOS-only (needs the Swift toolchain that
# ships with Xcode on the CI runners). No-op with a message if swift is absent, so
# a binaries-only pkg can still be built.
set -euo pipefail
STAGE="${1:?stage dir}"
ROOT="$(cd "$(dirname "$0")" && pwd)"
PKG="$ROOT/KeldSetup"

if ! command -v swift >/dev/null 2>&1; then
  echo "build-app.sh: swift not found — skipping KeldSetup.app"
  exit 0
fi

echo "build-app.sh: building KeldSetup ($(swift --version 2>/dev/null | head -1))"
( cd "$PKG" && swift build -c release )
BIN="$PKG/.build/release/KeldSetup"
[ -x "$BIN" ] || { echo "build-app.sh: no binary at $BIN"; exit 1; }

APP="$STAGE/KeldSetup.app"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS"
cp "$BIN" "$APP/Contents/MacOS/KeldSetup"
cp "$ROOT/Info.plist" "$APP/Contents/Info.plist"
echo "build-app.sh: wrapped $APP"
