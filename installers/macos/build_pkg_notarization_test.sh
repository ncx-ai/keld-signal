#!/usr/bin/env bash
# Guards the notarization gate in build-pkg.sh.
#
# LIMITATION, stated plainly: these are static assertions over the script text, not an
# execution test. build-pkg.sh needs pkgbuild/productbuild/codesign/notarytool, all
# macOS-only, so the gate cannot be exercised on the Linux CI runner or a dev box. What
# this catches is the regression that actually matters — someone restoring the old
# permissive default, where a pkg with no notarization verdict shipped anyway.
#
# Why that default was wrong: an unstapled pkg is only safe once a ticket EXISTS (then
# Gatekeeper validates online). With no verdict there is no ticket, so Gatekeeper blocks
# the installer outright. The permissive default could ship an unusable release.
set -euo pipefail

d="$(cd "$(dirname "$0")" && pwd)"
pkg="$d/build-pkg.sh"
wf="$d/../../.github/workflows/installers.yml"
test -f "$pkg" || { echo "missing build-pkg.sh"; exit 1; }
test -f "$wf" || { echo "missing installers.yml"; exit 1; }
fails=0

# Comments are stripped before grepping (matching onboard_command_test.sh's
# practice): a grep for prose is satisfied by a comment describing the thing
# just as easily as by the thing itself, so a check phrased that way can keep
# passing after the real line it names has gone.
pkg_code="$(sed 's/#.*//' "$pkg")"
wf_code="$(sed 's/#.*//' "$wf")"

check() { # check <description> <grep-args...>
  local desc="$1"; shift
  if printf '%s' "$pkg_code" | grep -qE "$@"; then echo "PASS: $desc"; else echo "FAIL: $desc"; fails=$((fails+1)); fi
}

# The default must be REQUIRED. `${KELD_NOTARY_REQUIRED:-1}` is the whole gate; if this
# reverts to :-0 or to a bare -n test, un-notarized pkgs ship again.
check "notarization required by default (:-1)" 'NOTARY_REQUIRED="\$\{KELD_NOTARY_REQUIRED:-1\}"'

# A pending verdict must be fatal unless explicitly opted out.
check "pending verdict fails unless opted out" '\[ "\$NOTARY_REQUIRED" != "0" \]'

# Missing creds must not silently produce an un-notarized pkg.
check "absent creds fail when required" '^elif \[ "\$NOTARY_REQUIRED" != "0" \]'

# A rejection must still fail — it means a broken payload, which waiting cannot fix.
check "Invalid/Rejected still exits 1" 'Invalid \| Rejected'

# The old permissive behaviour must not creep back in as the DEFAULT path. The phrase is
# still allowed inside the KELD_NOTARY_REQUIRED=0 branch, so match the old unconditional
# wording specifically.
if printf '%s' "$pkg_code" | grep -qE 'then ship regardless|ship regardless\.'; then
  echo "FAIL: 'ship regardless' language back in build-pkg.sh"; fails=$((fails+1))
else
  echo "PASS: no unconditional 'ship regardless' path"
fi

# The workflow must relax the gate ONLY for the documented no-secrets path, and must not
# hardcode a relaxation.
if printf '%s' "$wf_code" | grep -qE "KELD_NOTARY_REQUIRED: \\\$\{\{ secrets.APPLE_NOTARY_KEY != '' && '1' \|\| '0' \}\}"; then
  echo "PASS: workflow gates on presence of notary secrets"
else
  echo "FAIL: workflow no longer ties KELD_NOTARY_REQUIRED to secret presence"; fails=$((fails+1))
fi
if printf '%s' "$wf_code" | grep -qE "KELD_NOTARY_REQUIRED: *['\"]?0['\"]?$"; then
  echo "FAIL: workflow hardcodes KELD_NOTARY_REQUIRED=0"; fails=$((fails+1))
else
  echo "PASS: workflow does not hardcode the relaxation"
fi

# ── The wizard plugin ────────────────────────────────────────────────────────
# The pane is what removes the Terminal; these pin that it is actually built,
# actually signed, and actually handed to productbuild.
#
# ⚠️ THESE MUST FEED THE SAME `fails` COUNTER AS THE CHECKS ABOVE, and the
# summary line must print AFTER every check has run, not between the two
# groups. Printing "all checks passed" before this section (as this file used
# to) means a failure HERE still prints that line first and only THEN fails —
# a false-positive banner every reader would trust. $pkg and $b are the same
# file, so this reuses $pkg_code rather than re-reading it.
if printf '%s' "$pkg_code" | grep -qF 'plugin/build-plugin.sh'; then
  echo "PASS: build-pkg.sh builds the wizard plugin"
else
  echo "FAIL: build-pkg.sh does not build the wizard plugin"; fails=$((fails+1))
fi
if printf '%s' "$pkg_code" | grep -qF -- '--plugins'; then
  echo "PASS: build-pkg.sh passes --plugins to productbuild"
else
  echo "FAIL: build-pkg.sh does not pass --plugins to productbuild"; fails=$((fails+1))
fi
# ⚠️ The plugin lives OUTSIDE $STAGE, so sign-macho.sh's sweep does not see it.
# An unsigned Mach-O anywhere in a submission fails notarization for the whole pkg.
if printf '%s' "$pkg_code" | grep -qF 'codesign --verify --strict --verbose=2 "$PLUGIN_DIR/KeldSetup.bundle"'; then
  echo "PASS: build-pkg.sh verifies the plugin's signature"
else
  echo "FAIL: build-pkg.sh does not verify the plugin's signature"; fails=$((fails+1))
fi

[ "$fails" -eq 0 ] || { echo; echo "$fails check(s) failed"; exit 1; }
echo; echo "build-pkg notarization gate: all checks passed"
