#!/usr/bin/env bash
# Static assertions over the wizard plugin. These pin the two failure modes that
# are SILENT at runtime, which is why they are asserted rather than trusted.
set -euo pipefail
d="$(cd "$(dirname "$0")" && pwd)"
p="$d/plugin"
fail() { echo "FAIL: $1"; exit 1; }

test -f "$p/KeldSetup.m" || fail "missing KeldSetup.m"
test -x "$p/build-plugin.sh" || fail "build-plugin.sh is not executable"

# ⚠️ BUILT-IN SECTIONS ARE NAMED; ONLY OUR OWN BUNDLE IS A FILENAME — and
# getting that backwards broke the INSTALL, not merely the order. Listing the
# built-ins as Introduction.bundle/TargetSelect.bundle/... produced a scrambled
# sidebar on a real run and the install then failed at the end with an EMPTY
# message and `IFDInstallController state = 0`, having never written a payload.
#
# Installer.app's binary carries the bare names beside the filenames, and the
# section whose file is TargetSelect.bundle is named just "Target" — which is
# what proves they are two namespaces rather than aliases.
python3 - "$p/InstallerSections.plist" <<'PY' || fail "SectionOrder must name built-ins (Introduction, Target, ...) and use a filename only for KeldSetup.bundle"
import plistlib, sys
order = plistlib.load(open(sys.argv[1], 'rb'))["SectionOrder"]
builtins = {"Introduction", "ReadMe", "License", "Target", "PackageSelection", "Install", "Summary"}
ours = [s for s in order if s.endswith(".bundle")]
named = [s for s in order if not s.endswith(".bundle")]
ok = ours == ["KeldSetup.bundle"] and set(named) <= builtins and {"Install", "Summary"} <= set(named)
sys.exit(0 if ok else 1)
PY

# The pane must come BEFORE Install.bundle: a section after it never appears.
python3 - "$p/InstallerSections.plist" <<'PY' || fail "KeldSetup.bundle must be ordered before Install"
import plistlib, sys
order = plistlib.load(open(sys.argv[1], 'rb'))["SectionOrder"]
sys.exit(0 if order.index("KeldSetup.bundle") < order.index("Install") else 1)
PY

# Sign-after-build, then verify.
grep -q 'clang -bundle' "$p/build-plugin.sh" || fail "build-plugin.sh does not compile the bundle"
awk '/clang -bundle/{c=NR} /codesign --force/{if (!s) s=NR} /codesign --verify/{v=NR} END{exit !(c && s && v && c<s && s<v)}' \
  "$p/build-plugin.sh" || fail "build-plugin.sh must compile, THEN sign, THEN verify"

# The pane must not reimplement Go logic.
grep -q 'login", @"--code' "$p/KeldSetup.m" || fail "pane does not redeem the code via keld login --json"
grep -q 'install-sidecar' "$p/KeldSetup.m" || fail "pane does not drive keld signal install-sidecar"
grep -q 'installer-handoff.json' "$p/KeldSetup.m" || fail "pane writes no handoff file"
grep -q 'nextEnabled' "$p/KeldSetup.m" || fail "pane never gates Continue"

# The setup code must never be persisted. A negative grep only catches
# spellings its own pattern anticipated (it's case-sensitive, and it only
# inspects the writeToFile: line, by which point the payload is already an
# opaque NSData) — so this is a POSITIVE assertion on the handoff payload's
# key set instead: exactly these five keys, no more, no fewer. A new key of
# any spelling then fails here until someone justifies it.
python3 - "$p/KeldSetup.m" <<'PY' || fail "the setup code must never be written to disk"
import re, sys
src = open(sys.argv[1]).read()
m = re.search(r'NSDictionary \*payload = @\{(.*?)\};', src, re.S)
if not m:
    sys.exit(1)
keys = set(re.findall(r'@"([A-Za-z0-9_]+)"\s*:', m.group(1)))
expected = {"version", "paired", "api_url", "tools", "sidecar_staged"}
sys.exit(0 if keys == expected else 1)
PY


# ⚠️ CRITICAL: CFBundleShortVersionString is stamped verbatim from build-pkg.sh's
# own $VERSION, which CI sets from the release tag itself (installers.yml:
# VER="$TAG", e.g. "v3.0.0-rc.5") — it ALREADY carries the leading "v". Blindly
# prefixing another "v" onto it asks GitHub for "vv3.0.0-rc.5" on every tagged
# build, which 404s outright. This is invisible on `make release-dry`, whose
# "0.0.0-dryrun" version takes the no-tag branch instead and never reaches this
# line. Defensive normalisation (strip any leading "v", then add exactly one
# back) is fine; blindly appending "v" to the raw bundle version is not.
grep -qE '@"v"[[:space:]]*stringByAppendingString:version\]' "$p/KeldSetup.m" \
  && fail "pane prefixes 'v' onto the raw bundle version, which already carries one from the release tag — normalise (strip then re-add) instead of blindly prepending"

# ⚠️ NSTask REFUSES STREAM-PROPERTY WRITES AFTER LAUNCH, AND THE RAISE IS FATAL
# HERE. `t.standardOutput = nil` inside the terminationHandler throws
# NSInvalidArgumentException ("task already launched"); an ObjC throw inside a
# dispatch block is uncaught, so it is SIGTRAP — the plugin process dies and
# Installer.app puts up "the installer encountered an error, install anyway?".
# Measured on a real install 2026-09-14 (crash report: EXC_BREAKPOINT, frame
# `-[KeldSetupPane runKeld:onEvent:done:]_block_invoke` -> NOCOPY_SETTER_IMPL),
# then reproduced standalone: setting `terminationHandler` after launch is fine,
# setting `standardOutput` is not.
#
# NOTHING ELSE CAN CATCH THIS. `make pkg-plugin-check` compiles and signs but
# never RUNS the pane, no CI check can drive a wizard, and the design probe used
# a synchronous readDataToEndOfFile/waitUntilExit — so this async path had never
# executed once before a human double-clicked the pkg.
#
# The check keys on the handler's parameter name `t`, which is what separates a
# post-launch write from the legitimate pre-launch ones on `task`.
pane_body="$(sed 's|//.*||' "$p/KeldSetup.m")"
if grep -qE '(^|[^A-Za-z0-9_])t\.(standardOutput|standardError|standardInput|arguments|executableURL)[[:space:]]*=' <<< "$pane_body"; then
  fail "KeldSetup.m writes an NSTask stream/launch property on the terminationHandler's task; that raises NSInvalidArgumentException and kills the plugin process"
fi

# ⚠️ THE INSTALL IS ALL-OR-NOTHING: there is no "set up later". Continue is
# enabled only by a VERIFIED connection — either a setup code Atlas accepted, or
# `whoami --verify` confirming the stored credential still works. A deferral
# button would produce exactly the state this wizard exists to prevent: a machine
# that installed cleanly, collects nothing, and has no non-shell way to finish.
if grep -qiE 'set[[:space:]]?up later|setUpLater|skip for now' "$p/KeldSetup.m"; then
  fail "the pane offers a way to defer setup; the install is all-or-nothing"
fi

# The already-connected claim must rest on a VERIFIED credential, never on
# auth.json existing — `keld whoami` without --verify never contacts Atlas, so a
# revoked token reads identical to a live one.
grep -qF 'whoami' "$p/KeldSetup.m" || fail "pane never checks whether this machine is already connected"
grep -qF -- '--verify' "$p/KeldSetup.m" || fail "pane checks identity without --verify, which cannot tell a revoked token from a live one"

# The code is prefilled from the clipboard (the Atlas download page's Copy
# button is what puts it there) rather than typed.
grep -qF 'NSPasteboard' "$p/KeldSetup.m" || fail "pane does not read the clipboard, so the person must type the code by hand"
grep -qF 'KeldLooksLikePairingCode' "$p/KeldSetup.m" || fail "pane does not shape-check clipboard contents before submitting them"

# ⚠️ NOBODY SHOULD HAVE TO FETCH A CODE BY HAND. With no verified credential and
# nothing usable on the clipboard, the pane starts the OAuth device flow itself
# (`keld login --json` with no --code), so Atlas mints the code for that browser
# session. The pane must RENDER the returned user code: matching it against what
# the browser shows is the device flow's anti-phishing step, and hiding it turns
# a security property into decoration.
grep -qF 'device_code' "$p/KeldSetup.m" || fail "pane does not handle the device_code event, so it cannot start a sign-in"
grep -qF 'verification_url' "$p/KeldSetup.m" || fail "pane has no fallback URL for an Atlas that predates the compact route"

# ⚠️ THIS ASSERTION USED TO DEMAND THE OPPOSITE, and the reversal is the point
# rather than a loosening. It required the pane to RENDER the device-flow user
# code, because matching that code against the one in the browser is the flow's
# anti-phishing step — true while approval happened in a browser.
#
# Approval now happens on Atlas's page EMBEDDED IN THIS PANE: the pane supplies
# the code, loads the page and reads the result, so there is no second surface
# to compare against and printing it asks someone to check a number against
# itself. If approval ever moves out of the pane again — a system browser, or a
# platform that cannot embed a web view — the comparison becomes real again and
# this assertion should flip back with it.
if grep -qF '@"user_code"' "$p/KeldSetup.m"; then
  fail "pane reads the device-flow user code; with the approval page embedded there is nothing to compare it against, so it should not be displayed"
fi

# ⚠️ THE APPROVAL HAPPENS INSIDE THE WIZARD, on Atlas's OWN page. The pane loads
# the verification URL in a WKWebView, so the sign-in fields and the confirm
# button are Atlas's — this installer never sees a password, autofill behaves,
# and an SSO or 2FA step added later keeps working untouched. A native
# credential form here would own all three of those problems.
grep -qF 'WKWebView' "$p/KeldSetup.m" || fail "pane does not embed the approval page, so approval leaves the wizard"
# ⚠️ It must embed Atlas's COMPACT route. `verification_url` is the page built
# for a browser window — unauthenticated it redirects to the full login, and
# every state is min-h-screen — which in a 560x300 panel is a page the person
# has to scroll around inside a frame. Atlas returns `installer_url` for this,
# and the pane prefers it, falling back only for an Atlas that predates it.
grep -qF 'installer_url' "$p/KeldSetup.m" || fail "pane embeds verification_url, the browser-shaped page, instead of Atlas's compact installer route"
# ...which means the CLI must NOT also open an external browser behind it.
grep -qF -- '--no-browser' "$p/KeldSetup.m" || fail "pane embeds the approval page but lets keld open a browser too, so both appear"

# ⚠️ THE PANE CANNOT BE AIMED WITH AN ENVIRONMENT VARIABLE, AND THAT IS
# MEASURED. Installer.app inherits the env of whatever launched it — a launch
# carrying KELD_API_URL shows up in /var/log/install.log — but the plugin runs in
# InstallerRemotePluginService, an XPC service that starts with a CLEAN
# environment: the pane logged `KELD_API_URL=(unset)` while Installer's own dump
# showed it set. So `keld` defaulted to production and loaded production's page.
#
# The target therefore has to be BAKED IN at build time (build-plugin.sh stamps
# KeldAPIURL into the bundle's Info.plist when KELD_API_URL is set) and passed
# explicitly on the command line. A release build sets nothing and gets the
# compiled-in default.
grep -qF 'KeldAPIURL' "$p/KeldSetup.m" || fail "pane cannot be pointed at a non-default Atlas: nothing reads the bundle's KeldAPIURL"
grep -qF -- '--api-url' "$p/KeldSetup.m" || fail "pane reads a configured Atlas but never passes --api-url, so the child still uses the default"
grep -qF 'KeldAPIURL' "$p/build-plugin.sh" || fail "build-plugin.sh never stamps the configured Atlas into the bundle"

# Content flush against the pane's frame reads as broken; the stack needs insets.
grep -qF 'edgeInsets' "$p/KeldSetup.m" || fail "pane's stack has no edge insets, so content sits flush against the panel border"

# ⚠️ THE PANE MUST NOT PROMPT FOR A SIGN-IN THAT IS NOT ON SCREEN YET. The
# approval page is fetched over the network and compiled by Atlas on demand, so
# there is a window — seconds, and longer against a cold route — in which the
# pane showed "Sign in to connect this device." above an empty rectangle. That
# is the worst possible reading of a wait: it names an action, offers nothing to
# act on, and so invites a retry of something that was merely still arriving.
#
# Two halves, and the fix is incomplete without either. A progress indicator has
# to be RUNNING while the page loads, and the prompt has to be set from the
# navigation delegate's didFinish — i.e. when the form actually exists — rather
# than from the `device_code` event, which only marks the moment the load began.
grep -q 'navigationDelegate = self' "$p/KeldSetup.m" \
  || fail "pane never becomes the web view's navigation delegate, so it cannot know when the page has loaded"
grep -q 'didFinishNavigation' "$p/KeldSetup.m" \
  || fail "pane does not implement didFinishNavigation, so nothing distinguishes a loading page from a loaded one"

# The prompt must live INSIDE didFinishNavigation. A grep for the string alone
# would pass on exactly the code this pins against — it was already present, in
# the device_code handler, which is the bug.
awk '/didFinishNavigation/,/^}/' "$p/KeldSetup.m" | grep -q 'Sign in to connect' \
  || fail "the sign-in prompt is not set when the page finishes loading, so it still appears over a blank view"

# And the wait has to be visible for as long as it lasts. The invariant is
# co-location: whichever method issues the request must also start the
# indicator, so a later edit cannot move the load somewhere the spinner does not
# follow. Asserting them separately would pass on a file where one runs and the
# other never does.
body=$(awk '/- \(void\)beginApprovalLoad:/,/^}/' "$p/KeldSetup.m")
printf '%s' "$body" | grep -q 'startAnimation' \
  || fail "no progress indicator is started when the approval page begins loading"
printf '%s' "$body" | grep -q 'loadRequest' \
  || fail "the indicator starts in a method that does not issue the load"


# ⚠️ -initialKeyView MUST NEVER NAME A HIDDEN CONTROL, and returning one broke
# the pane on RE-ENTRY only. Installer.app applies initialKeyView on every pane
# entry, not just the first. The code field is hidden while the approval page is
# showing, so after Back-then-Continue the window's first responder became a
# field nobody could see: measured with a standalone harness (focustest.m),
# `makeFirstResponder:` on a HIDDEN NSTextField returns YES and installs its
# field editor, so every keystroke lands in an invisible NSTextView and the
# embedded page's own inputs look disabled.
#
# The guard is that the method consults `hidden` before naming anything.
key=$(awk '/- \(NSView \*\)initialKeyView/,/^}/' "$p/KeldSetup.m")
printf '%s' "$key" | grep -q 'hidden' \
  || fail "initialKeyView can return a hidden control, so pane re-entry sends typing to an invisible field"
printf '%s' "$key" | grep -q '_approvalWeb' \
  || fail "initialKeyView ignores the approval page, so focus never reaches the form actually on screen"

echo "plugin_test.sh: OK"
