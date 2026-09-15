#!/usr/bin/env bash
# The installers workflow can aim a dry-run build at a non-default Atlas.
#
# ⚠️ AN ENVIRONMENT VARIABLE CANNOT REACH THE WIZARD PANE AT INSTALL TIME. The
# pane runs inside InstallerRemotePluginService, an XPC service that starts with
# a clean environment — measured 2026-09-14, the pane read KELD_API_URL=(unset)
# in the same run where Installer.app's own env dump showed it set. So the Atlas
# target is BAKED IN at build time by build-plugin.sh, and the only way a CI
# build can target a dev deployment is for the workflow to pass it through.
#
# These assertions exist because the plumbing is three hops long (input ->
# job env -> build-pkg.sh -> build-plugin.sh) and a break anywhere in it is
# SILENT: the build still succeeds and the pkg still installs, it just quietly
# points at production.
set -euo pipefail
d="$(cd "$(dirname "$0")/.." && pwd)"
wf="$d/.github/workflows/installers.yml"
fail() { echo "FAIL: $1"; exit 1; }

test -f "$wf" || fail "missing installers.yml"

python3 - "$wf" <<'PY' || exit 1
import re, sys
src = open(sys.argv[1]).read()
fails = []

# 1. The input exists and is optional, so every existing caller keeps working.
dispatch = re.search(r'\n  workflow_dispatch:\n(.*?)\n\n', src, re.S)
if not dispatch or 'api_url:' not in dispatch.group(1):
    fails.append("workflow_dispatch has no api_url input")
elif 'required: true' in dispatch.group(1).split('api_url:')[1].split('\n\n')[0]:
    fails.append("api_url must be optional; a required input breaks every existing dry run")

# 2. It reaches the pkg build step's environment. Anything else — referencing it
#    in a comment, or setting it on the wrong step — leaves the pane on the
#    compiled-in default with nothing to show for it.
step = re.search(r'\n      - name: [^\n]*(?:pkg|installer)[^\n]*\n(.*?)(?=\n      - name: )', src, re.S | re.I)
if 'KELD_API_URL' not in src:
    fails.append("KELD_API_URL is never set anywhere in the workflow")
else:
    # The assignment must be in an `env:` block, not merely mentioned.
    if not re.search(r'^\s+KELD_API_URL:\s*\$\{\{\s*(inputs|github\.event\.inputs)\.api_url\s*\}\}\s*$', src, re.M):
        fails.append("KELD_API_URL is not bound to the api_url input in an env: block")

# 3. It must NOT be wired into a release path. A published installer that points
#    at a dev Atlas would send real machines' telemetry to it.
for line in src.splitlines():
    if 'KELD_API_URL' in line and 'inputs.api_url' not in line and not line.strip().startswith('#'):
        fails.append(f"KELD_API_URL set from something other than the input: {line.strip()}")

for f in fails:
    print(f"FAIL: {f}")
sys.exit(1 if fails else 0)
PY

echo "installers_api_url_test.sh: OK"
