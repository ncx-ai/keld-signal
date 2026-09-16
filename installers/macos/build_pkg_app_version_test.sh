#!/usr/bin/env bash
# Static assertions over build-pkg.sh's stamping of the desktop app bundle.
#
# ⚠️ AN UNSTAMPED APP IS SILENTLY SKIPPED ON EVERY UPGRADE. Tauri's default
# version is 0.1.0, and build-pkg.sh stamped the payload's VERSION file and the
# wizard plugin but never the app bundle — so every release shipped
# `CFBundleShortVersionString = 0.1.0`. PackageKit compares component versions
# and refuses to install one that is not newer, logging:
#
#   PackageKit: Skipping component "co.keld.signal" (0.1.0-0.1.0-*) because the
#   version 2.5.0-1.0.0-* is already installed at /Applications/Keld Signal.app.
#
# Observed on a real v3.0.0 install (2026-09-16). Nothing failed, nothing warned,
# and the app on disk stayed at whatever got there first. Because the version
# never advances, this is not a one-off: after the first install, no later
# release could ever replace the app.
#
# These are static assertions because the gate cannot execute off macOS (no
# pkgbuild/productbuild/plutil), which is the same reason
# build_pkg_notarization_test.sh is written this way.
set -euo pipefail
p="$(cd "$(dirname "$0")" && pwd)"
fail() { echo "FAIL: $1"; exit 1; }

src="$p/build-pkg.sh"
[ -f "$src" ] || fail "build-pkg.sh not found"

# 1. Both keys are stamped. CFBundleShortVersionString is what a person sees;
#    CFBundleVersion is what PackageKit actually compares. Stamping only the
#    display one would leave the skip in place while looking fixed.
grep -q 'CFBundleShortVersionString' "$src" \
  || fail "build-pkg.sh never stamps CFBundleShortVersionString into the app bundle"
grep -q 'CFBundleVersion' "$src" \
  || fail "build-pkg.sh never stamps CFBundleVersion — the key PackageKit compares"

# 2. It stamps the STAGED COPY, not the source tree. build-pkg.sh already copies
#    the bundle into $TMP/app-stage; stamping the original would mutate a
#    developer's build output as a side effect of packaging.
awk '/APP_STAGE=/,/pkgbuild/' "$src" | grep -q 'plutil -replace' \
  || fail "the version stamp does not happen between staging the copy and pkgbuild"

# 3. The stamp is applied BEFORE the app component is built, or the pkg carries
#    the unstamped Info.plist and the assertion above is decorative.
stamp_line=$(grep -n 'plutil -replace CFBundleVersion' "$src" | head -1 | cut -d: -f1)
build_line=$(grep -n 'app-component.pkg' "$src" | head -1 | cut -d: -f1)
[ -n "$stamp_line" ] || fail "no CFBundleVersion stamp line found"
[ -n "$build_line" ] || fail "no app-component.pkg build line found"
[ "$stamp_line" -lt "$build_line" ] \
  || fail "the app is packaged (line $build_line) before it is stamped (line $stamp_line)"

# 4. A stamp that silently no-ops is the defect wearing a fix's clothes: plutil
#    exits 0 for a key it could not set in some layouts, so the result is read
#    back and compared.
grep -q 'stamped_version\|verify the stamp\|plutil -extract CFBundleVersion' "$src" \
  || fail "build-pkg.sh does not read the stamp back, so a no-op stamp would ship"

echo "build-pkg app version stamp: all checks passed"
