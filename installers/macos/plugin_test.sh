#!/usr/bin/env bash
# Static assertions over the wizard plugin. These pin the two failure modes that
# are SILENT at runtime, which is why they are asserted rather than trusted.
set -euo pipefail
d="$(cd "$(dirname "$0")" && pwd)"
p="$d/plugin"
fail() { echo "FAIL: $1"; exit 1; }

test -f "$p/KeldSetup.m" || fail "missing KeldSetup.m"
test -x "$p/build-plugin.sh" || fail "build-plugin.sh is not executable"

# ⚠️ Every SectionOrder entry must end in .bundle — a bare name loads nothing.
python3 - "$p/InstallerSections.plist" <<'PY' || fail "SectionOrder entries must all end in .bundle"
import plistlib, sys
order = plistlib.load(open(sys.argv[1], 'rb'))["SectionOrder"]
bad = [s for s in order if not s.endswith(".bundle")]
sys.exit(1 if bad else 0)
PY

# The pane must come BEFORE Install.bundle: a section after it never appears.
python3 - "$p/InstallerSections.plist" <<'PY' || fail "KeldSetup.bundle must be ordered before Install.bundle"
import plistlib, sys
order = plistlib.load(open(sys.argv[1], 'rb'))["SectionOrder"]
sys.exit(0 if order.index("KeldSetup.bundle") < order.index("Install.bundle") else 1)
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
grep -qF 'device_code' "$p/KeldSetup.m" || fail "pane does not handle the device_code event, so it cannot start a browser sign-in"
grep -qF 'user_code' "$p/KeldSetup.m" || fail "pane never renders the device-flow user code, which is what the person matches against the browser"
grep -qF 'verification_url' "$p/KeldSetup.m" || fail "pane does not surface the verification URL, so a failed browser open is a dead end"

# ⚠️ THE APPROVAL HAPPENS INSIDE THE WIZARD, on Atlas's OWN page. The pane loads
# the verification URL in a WKWebView, so the sign-in fields and the confirm
# button are Atlas's — this installer never sees a password, autofill behaves,
# and an SSO or 2FA step added later keeps working untouched. A native
# credential form here would own all three of those problems.
grep -qF 'WKWebView' "$p/KeldSetup.m" || fail "pane does not embed the approval page, so approval leaves the wizard"
# ...which means the CLI must NOT also open an external browser behind it.
grep -qF -- '--no-browser' "$p/KeldSetup.m" || fail "pane embeds the approval page but lets keld open a browser too, so both appear"

# Content flush against the pane's frame reads as broken; the stack needs insets.
grep -qF 'edgeInsets' "$p/KeldSetup.m" || fail "pane's stack has no edge insets, so content sits flush against the panel border"

echo "plugin_test.sh: OK"
