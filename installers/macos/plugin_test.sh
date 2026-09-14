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

echo "plugin_test.sh: OK"
