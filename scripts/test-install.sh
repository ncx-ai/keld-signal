#!/bin/sh
# Hermetic tests for scripts/install.sh.
#
# No network and no writes outside a temp dir: a fake "release" is laid out on
# disk and served to the installer through curl's file:// protocol via
# KELD_DOWNLOAD_BASE, with KELD_RELEASE_TAG pinning the tag (so the GitHub API
# is never consulted), KELD_INSTALL_DIR as DEST and KELD_HOME redirected. The
# `keld-agent` in the fixture archive is a stub shell script, so no service is
# ever installed and no daemon is ever started.
#
# Usage: sh scripts/test-install.sh     (also: make test-install)
set -u

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
INSTALL_SH="${here}/install.sh"
[ -f "$INSTALL_SH" ] || { echo "cannot find install.sh next to $0" >&2; exit 1; }

root=$(mktemp -d "${TMPDIR:-/tmp}/keld-install-test.XXXXXX") || exit 1
trap 'rm -rf "$root"' EXIT HUP INT TERM

pass=0
fail=0
ok()   { pass=$((pass+1)); echo "  ok   — $1"; }
bad()  { fail=$((fail+1)); echo "  FAIL — $1"; }
check() { # check <description> <condition-exit-code>
  if [ "$2" = 0 ]; then ok "$1"; else bad "$1"; fi
}

# ── The same os/arch mapping install.sh does, so fixture names line up ────────
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in linux) os=linux ;; darwin) os=darwin ;; *) echo "SKIP: unsupported os $os"; exit 0 ;; esac
arch=$(uname -m)
case "$arch" in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) echo "SKIP: unsupported arch $arch"; exit 0 ;; esac

TAG="v0.0.0-test"
ARCHIVE="keld_${os}_${arch}.tar.gz"
SC_ARCHIVE="keld-agent-sidecar_${os}_${arch}.tar.gz"

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'
  else echo "no sha256 tool available" >&2; exit 1; fi
}

# ── Build the fake release ────────────────────────────────────────────────────
# stub keld-agent: records that it ran, and (when KELD_STUB_ONBOARD=1) writes the
# hook.json a real `keld signal setup` would have written.
build_release() { # build_release <dir>
  rel="$1/rel/${TAG}"; src="$1/src"
  mkdir -p "$rel" "$src/keld-agent-sidecar"
  printf '#!/bin/sh\necho "[stub keld]"\n' > "$src/keld"
  cat > "$src/keld-agent" <<'STUB'
#!/bin/sh
echo "[stub keld-agent] $*"
: > "${KELD_STUB_MARKER:-/dev/null}"
if [ "${KELD_STUB_ONBOARD:-0}" = 1 ]; then
  mkdir -p "$KELD_HOME"
  printf '{"endpoint":"https://atlas.example","ingest_token":"tok"}\n' > "${KELD_HOME}/hook.json"
fi
exit "${KELD_STUB_EXIT:-0}"
STUB
  chmod +x "$src/keld" "$src/keld-agent"
  printf 'stub sidecar\n' > "$src/keld-agent-sidecar/keld-agent-sidecar"
  chmod +x "$src/keld-agent-sidecar/keld-agent-sidecar"
  tar -C "$src" -czf "${rel}/${ARCHIVE}" keld keld-agent
  tar -C "$src" -czf "${rel}/${SC_ARCHIVE}" keld-agent-sidecar
  {
    printf '%s  %s\n' "$(sha256_of "${rel}/${ARCHIVE}")" "$ARCHIVE"
    printf '%s  %s\n' "$(sha256_of "${rel}/${SC_ARCHIVE}")" "$SC_ARCHIVE"
  } > "${rel}/checksums.txt"
  printf '%s  %s\n' "$(sha256_of "${rel}/${SC_ARCHIVE}")" "$SC_ARCHIVE" \
    > "${rel}/${SC_ARCHIVE}.sha256"
}

# INSTALL_ARGS is forwarded to install.sh itself (e.g. --code X).
INSTALL_ARGS=""

# run_install <case-dir> [env assignments...] — returns the installer's exit code,
# stdout+stderr in ${case}/out.
run_install() {
  c="$1"; shift
  mkdir -p "${c}/bin" "${c}/home"
  env KELD_RELEASE_TAG="$TAG" \
      KELD_DOWNLOAD_BASE="file://${c}/rel" \
      KELD_INSTALL_DIR="${c}/bin" \
      KELD_HOME="${c}/home" \
      KELD_STUB_MARKER="${c}/agent-install-ran" \
      PATH="${c}/bin:${PATH}" \
      "$@" \
      sh "$INSTALL_SH" ${INSTALL_ARGS} >"${c}/out" 2>&1
}

new_case() { # new_case <name> — fresh dir with a fresh fake release
  c="${root}/$1"; mkdir -p "$c"; build_release "$c"; echo "$c"
}

echo "install.sh — hermetic tests (${os}/${arch})"

# ── 1. Happy path: everything verifies, everything lands ──────────────────────
echo "case: clean install, onboarding completes"
c=$(new_case happy)
run_install "$c" KELD_STUB_ONBOARD=1; rc=$?
check "exits 0" "$([ "$rc" = 0 ] && echo 0 || echo 1)"
check "installs keld" "$([ -x "${c}/bin/keld" ] && echo 0 || echo 1)"
check "installs keld-agent" "$([ -x "${c}/bin/keld-agent" ] && echo 0 || echo 1)"
check "installs the sidecar tree" "$([ -x "${c}/bin/keld-agent-sidecar/keld-agent-sidecar" ] && echo 0 || echo 1)"
check "ran keld-agent install" "$([ -f "${c}/agent-install-ran" ] && echo 0 || echo 1)"
check "reports success" "$(grep -q 'set up and running' "${c}/out" && echo 0 || echo 1)"
check "leaves no temp workspace behind" "$([ -z "$(find "${c}/bin" -maxdepth 1 -name '.keld-install.*' 2>/dev/null)" ] && echo 0 || echo 1)"

# ── 2. Onboarding never ran: the message must not claim success ───────────────
echo "case: binaries installed, onboarding never ran (the curl|sh headless path)"
c=$(new_case notonboarded)
run_install "$c"; rc=$?
check "exits 0 (a pending manual step is not a failure)" "$([ "$rc" = 0 ] && echo 0 || echo 1)"
check "does NOT claim 'set up and running'" "$(grep -q 'set up and running' "${c}/out" && echo 1 || echo 0)"
check "says setup is not finished" "$(grep -qi 'not set up' "${c}/out" && echo 0 || echo 1)"
check "prints the exact finishing command" "$(grep -q 'keld login && keld signal setup' "${c}/out" && echo 0 || echo 1)"

# ── 3. A --code was supplied but onboarding still produced no token → failure ─
echo "case: --code supplied but no token written"
c=$(new_case codefailed)
INSTALL_ARGS="--code ABC123"
run_install "$c"; rc=$?
INSTALL_ARGS=""
check "exits non-zero" "$([ "$rc" != 0 ] && echo 0 || echo 1)"
check "passed the code through to keld-agent" "$(grep -q 'code ABC123' "${c}/out" && echo 0 || echo 1)"

# ── 4. Corrupt main archive: caught, and nothing is installed ────────────────
echo "case: corrupt keld archive (checksum mismatch)"
c=$(new_case corrupt)
printf 'not the archive you signed for' >> "${c}/rel/${TAG}/${ARCHIVE}"
run_install "$c" KELD_STUB_ONBOARD=1; rc=$?
check "exits non-zero" "$([ "$rc" != 0 ] && echo 0 || echo 1)"
check "names the mismatch" "$(grep -qi 'checksum' "${c}/out" && echo 0 || echo 1)"
check "installs nothing" "$([ ! -e "${c}/bin/keld" ] && echo 0 || echo 1)"
check "never ran keld-agent install" "$([ ! -f "${c}/agent-install-ran" ] && echo 0 || echo 1)"

# ── 5. Corrupt sidecar: caught, and the previous sidecar survives ─────────────
echo "case: corrupt sidecar archive keeps the working one in place"
c=$(new_case corruptsc)
mkdir -p "${c}/bin/keld-agent-sidecar"
printf 'previous install\n' > "${c}/bin/keld-agent-sidecar/keld-agent-sidecar"
printf 'garbage' >> "${c}/rel/${TAG}/${SC_ARCHIVE}"
run_install "$c" KELD_STUB_ONBOARD=1; rc=$?
check "exits non-zero" "$([ "$rc" != 0 ] && echo 0 || echo 1)"
check "does not leave a partial sidecar tree" \
  "$(grep -q 'previous install' "${c}/bin/keld-agent-sidecar/keld-agent-sidecar" && echo 0 || echo 1)"
check "cleans up its temp workspace" "$([ -z "$(find "${c}/bin" -maxdepth 1 -name '.keld-install.*' 2>/dev/null)" ] && echo 0 || echo 1)"

# ── 6. Truncated sidecar transfer (well-formed prefix) is caught too ──────────
echo "case: truncated sidecar transfer"
c=$(new_case truncated)
dd if="${c}/rel/${TAG}/${SC_ARCHIVE}" of="${c}/trunc" bs=1 count=60 2>/dev/null
mv "${c}/trunc" "${c}/rel/${TAG}/${SC_ARCHIVE}"
run_install "$c" KELD_STUB_ONBOARD=1; rc=$?
check "exits non-zero" "$([ "$rc" != 0 ] && echo 0 || echo 1)"
check "no sidecar tree left" "$([ ! -e "${c}/bin/keld-agent-sidecar" ] && echo 0 || echo 1)"

# ── 7. No checksums.txt at all: warn, but still install ──────────────────────
echo "case: release without checksums.txt"
c=$(new_case nochecksums)
rm -f "${c}/rel/${TAG}/checksums.txt" "${c}/rel/${TAG}/${SC_ARCHIVE}.sha256"
run_install "$c" KELD_STUB_ONBOARD=1; rc=$?
check "exits 0" "$([ "$rc" = 0 ] && echo 0 || echo 1)"
check "warns that integrity was not verified" "$(grep -qi 'skipping integrity check' "${c}/out" && echo 0 || echo 1)"
check "still installs keld" "$([ -x "${c}/bin/keld" ] && echo 0 || echo 1)"

# ── 8. Sidecar verified from its own .sha256 when checksums.txt omits it ──────
echo "case: sidecar hash published as a sidecar .sha256 file"
c=$(new_case scsha)
grep -v "$SC_ARCHIVE" "${c}/rel/${TAG}/checksums.txt" > "${c}/cs" && mv "${c}/cs" "${c}/rel/${TAG}/checksums.txt"
printf 'garbage' >> "${c}/rel/${TAG}/${SC_ARCHIVE}"
run_install "$c" KELD_STUB_ONBOARD=1; rc=$?
check "still catches a corrupt sidecar" "$([ "$rc" != 0 ] && echo 0 || echo 1)"

# ── 9. No stale command suggestions ──────────────────────────────────────────
echo "case: suggested commands exist"
check "no bare 'keld doctor' (it is 'keld signal doctor')" \
  "$(grep -n 'keld doctor' "$INSTALL_SH" >/dev/null && echo 1 || echo 0)"
check "no bare 'keld setup' (it is 'keld signal setup')" \
  "$(grep -nE '(^|[^-])keld setup' "$INSTALL_SH" >/dev/null && echo 1 || echo 0)"
check "no bare 'keld status'/'keld uninstall'" \
  "$(grep -nE 'keld (status|uninstall)' "$INSTALL_SH" >/dev/null && echo 1 || echo 0)"

echo ""
echo "${pass} passed, ${fail} failed"
[ "$fail" = 0 ] || exit 1
