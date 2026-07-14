#!/usr/bin/env bash
# Integration test for scripts/install.sh: fake binaries + file:// download, assert the
# code flows into `keld-agent install --code`, the ML sidecar is installed (mandatory),
# and a missing sidecar aborts. No network. Run: bash scripts/test-install-sh.sh
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

# tarballs at <base>/<tag>/ (install.sh's expected layout)
mkdir -p "$tmp/dl/testtag"
tar -C "$tmp/pkg" -czf "$tmp/dl/testtag/keld_linux_amd64.tar.gz" keld keld-agent
# fake ML sidecar (mandatory): nested keld-agent-sidecar/keld-agent-sidecar
mkdir -p "$tmp/scpkg/keld-agent-sidecar"
echo "#!/bin/sh" > "$tmp/scpkg/keld-agent-sidecar/keld-agent-sidecar"
chmod +x "$tmp/scpkg/keld-agent-sidecar/keld-agent-sidecar"
tar -C "$tmp/scpkg" -czf "$tmp/dl/testtag/keld-agent-sidecar_linux_amd64.tar.gz" keld-agent-sidecar

# Non-linux/amd64 hosts: the archive names differ — skip cleanly.
if [ "$(uname -s)" != "Linux" ] || { [ "$(uname -m)" != "x86_64" ] && [ "$(uname -m)" != "amd64" ]; }; then
  echo "SKIP: install.sh test requires linux/amd64 host"; exit 0
fi

run_install() { # args passed through to install.sh; env: KELD_SETUP_CODE optional
  KELD_RELEASE_TAG=testtag \
  KELD_DOWNLOAD_BASE="file://$tmp/dl" \
  KELD_INSTALL_DIR="$tmp/bin" \
    sh "$here/scripts/install.sh" "$@"
}

# 1) code flows through + ML sidecar installed
export KELD_TEST_LOG="$tmp/log"; : > "$KELD_TEST_LOG"
rm -rf "$tmp/bin"
run_install --code TESTCODE >/dev/null 2>&1 || true
grep -q "^keld-agent install --code TESTCODE$" "$KELD_TEST_LOG" \
  || { echo "FAIL: keld-agent not invoked with the code. Log:"; cat "$KELD_TEST_LOG"; exit 1; }
echo "PASS: keld-agent install --code TESTCODE"
[ -x "$tmp/bin/keld-agent-sidecar/keld-agent-sidecar" ] \
  || { echo "FAIL: ML sidecar not installed"; exit 1; }
echo "PASS: ML sidecar installed"

# 2) --code arg wins over KELD_SETUP_CODE env
: > "$KELD_TEST_LOG"; rm -rf "$tmp/bin"
KELD_SETUP_CODE=ENVCODE run_install --code ARGCODE >/dev/null 2>&1 || true
grep -q "^keld-agent install --code ARGCODE$" "$KELD_TEST_LOG" \
  || { echo "FAIL: --code did not win over KELD_SETUP_CODE. Log:"; cat "$KELD_TEST_LOG"; exit 1; }
echo "PASS: --code wins over KELD_SETUP_CODE"

# 3) mandatory ML: a missing sidecar tarball ABORTS (exit!=0) and never runs keld-agent install
: > "$KELD_TEST_LOG"; rm -rf "$tmp/bin"
mv "$tmp/dl/testtag/keld-agent-sidecar_linux_amd64.tar.gz" "$tmp/dl/testtag/_sidecar.hidden"
rc=0; run_install --code TESTCODE >/dev/null 2>&1 || rc=$?
mv "$tmp/dl/testtag/_sidecar.hidden" "$tmp/dl/testtag/keld-agent-sidecar_linux_amd64.tar.gz"
[ "$rc" -ne 0 ] || { echo "FAIL: install did not abort on a missing sidecar (ML is mandatory)"; exit 1; }
grep -q "keld-agent install" "$KELD_TEST_LOG" \
  && { echo "FAIL: keld-agent install ran despite the sidecar abort"; exit 1; }
echo "PASS: missing ML sidecar aborts the install (no deterministic fallback)"
