#!/usr/bin/env bash
# The macOS pkg has NO Keld screen. This file used to pin the wizard pane
# (installers/macos/plugin/, KeldSetup.bundle); since 2026-09-29 it pins the
# pane's ABSENCE, which is why it keeps its name and its place in CI rather than
# being deleted.
#
# ⚠️ WHY INVERTED RATHER THAN DELETED. Installers only install
# (docs/superpowers/specs/2026-09-29-signal-web-signin-discovery.html, AC-10 and
# §8 row 8): no installer asks anyone anything about Keld — no sign-in, no setup
# code, no tool picker. Signal asks on first open, and the daemon's own
# auto-setup configures detected tools. A pane put back into the pkg would
# re-open exactly the question that decision closed ("I couldn't proceed without
# signing in", #431), so putting it back must fail here, loudly.
set -euo pipefail
d="$(cd "$(dirname "$0")" && pwd)"
fail() { echo "FAIL: $1"; exit 1; }

# 1. The plugin tree is gone, and with it the SectionOrder that inserted a Keld
#    section between PackageSelection and Install.
[ ! -e "$d/plugin" ] || fail "installers/macos/plugin/ is back; the pkg must have no Keld screen"
if find "$d" -name 'InstallerSections.plist' -print | grep -q .; then
  fail "an InstallerSections.plist exists under installers/macos; it can only be there to add a section"
fi
if find "$d" -name 'KeldSetup*' -print | grep -q .; then
  fail "a KeldSetup source or bundle exists under installers/macos"
fi

# 2. build-pkg.sh builds, signs and passes no plugin. Comments are stripped so the
#    history written in them cannot satisfy — or trip — the assertion.
code="$(sed 's/#.*//' "$d/build-pkg.sh")"
case "$code" in *build-plugin*) fail "build-pkg.sh still builds a wizard plugin" ;; esac
case "$code" in *--plugins*) fail "build-pkg.sh still hands productbuild a --plugins directory" ;; esac
case "$code" in *KeldSetup*) fail "build-pkg.sh still references KeldSetup.bundle" ;; esac

# 3. distribution.xml declares no custom section either: a <plugin> or
#    <installation-check> that asks the person something is the same screen by
#    another route.
dist="$(sed 's/<!--.*-->//' "$d/distribution.xml")"
case "$dist" in *"<plugin"*) fail "distribution.xml names an installer plugin" ;; esac

# 4. The release workflow neither builds nor signs a plugin.
wf="$d/../../.github/workflows/installers.yml"
test -f "$wf" || fail "cannot find installers.yml - this guard would pass vacuously"
wf_code="$(sed 's/#.*//' "$wf")"
case "$wf_code" in *build-plugin*|*KeldSetup*) fail "installers.yml still builds or signs the wizard plugin" ;; esac

echo "plugin_test.sh: OK (the pkg has no Keld section and builds no plugin)"
