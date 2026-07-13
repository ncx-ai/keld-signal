#!/usr/bin/env bash
# Integration test for scripts/install.sh: fake binaries + file:// download, assert the
# code flows into `keld-agent install --code`. No network. Run: bash scripts/test-install-sh.sh
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# fake binaries: record args to $KELD_TEST_LOG, then exit 0
mkdir -p "$tmp/pkg"
cat > "$tmp/pkg/keld" <<'EOF'
#!/bin/sh
echo "keld $*" >> "$KELD_TEST_LOG"
EOF
cat > "$tmp/pkg/keld-agent" <<'EOF'
#!/bin/sh
echo "keld-agent $*" >> "$KELD_TEST_LOG"
EOF
chmod +x "$tmp/pkg/keld" "$tmp/pkg/keld-agent"

# tarball at <base>/<tag>/keld_linux_amd64.tar.gz (install.sh's expected layout)
mkdir -p "$tmp/dl/testtag"
tar -C "$tmp/pkg" -czf "$tmp/dl/testtag/keld_linux_amd64.tar.gz" keld keld-agent

export KELD_TEST_LOG="$tmp/log"; : > "$KELD_TEST_LOG"
KELD_RELEASE_TAG=testtag \
KELD_DOWNLOAD_BASE="file://$tmp/dl" \
KELD_INSTALL_DIR="$tmp/bin" \
KELD_NO_SIDECAR=1 \
  sh "$here/scripts/install.sh" --code TESTCODE >/dev/null 2>&1 || true

# On x86_64 linux the archive matches; on other hosts uname differs — skip cleanly.
if [ "$(uname -s)" != "Linux" ] || { [ "$(uname -m)" != "x86_64" ] && [ "$(uname -m)" != "amd64" ]; }; then
  echo "SKIP: install.sh test requires linux/amd64 host"; exit 0
fi

if ! grep -q "^keld-agent install --code TESTCODE$" "$KELD_TEST_LOG"; then
  echo "FAIL: keld-agent not invoked with the code. Log:"; cat "$KELD_TEST_LOG"; exit 1
fi
echo "PASS: keld-agent install --code TESTCODE"

# env-var precedence: --code wins over KELD_SETUP_CODE
: > "$KELD_TEST_LOG"
KELD_RELEASE_TAG=testtag KELD_DOWNLOAD_BASE="file://$tmp/dl" KELD_INSTALL_DIR="$tmp/bin" \
KELD_NO_SIDECAR=1 KELD_SETUP_CODE=ENVCODE \
  sh "$here/scripts/install.sh" --code ARGCODE >/dev/null 2>&1 || true
if ! grep -q "^keld-agent install --code ARGCODE$" "$KELD_TEST_LOG"; then
  echo "FAIL: --code did not win over KELD_SETUP_CODE. Log:"; cat "$KELD_TEST_LOG"; exit 1
fi
echo "PASS: --code wins over KELD_SETUP_CODE"
