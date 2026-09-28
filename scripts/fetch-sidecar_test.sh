#!/usr/bin/env bash
# Tests for scripts/fetch-sidecar.sh against a local HTTP server laid out like the
# release mirror (dl.keld.co): latest.json, releases/<tag>/, prereleases/<tag>/.
# No network beyond 127.0.0.1. Run: bash scripts/fetch-sidecar_test.sh
set -uo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
srv=""
trap '[ -n "$srv" ] && kill "$srv" 2>/dev/null; rm -rf "$tmp"' EXIT
fail=0
ok()  { echo "ok: $1"; }
bad() { echo "FAIL: $1"; fail=1; }

sha() { if command -v sha256sum >/dev/null; then sha256sum "$1" | awk '{print $1}'; else shasum -a 256 "$1" | awk '{print $1}'; fi; }

asset="keld-agent-sidecar_test_amd64.tar.gz"
publish() { # publish <dir-under-mirror> <version-stamp>
  local d="$tmp/mirror/$1"; mkdir -p "$d" "$tmp/pkg/keld-agent-sidecar"
  printf '#!/bin/sh\necho sidecar\n' > "$tmp/pkg/keld-agent-sidecar/keld-agent-sidecar"
  chmod +x "$tmp/pkg/keld-agent-sidecar/keld-agent-sidecar"
  echo "$2" > "$tmp/pkg/keld-agent-sidecar/VERSION"
  tar -C "$tmp/pkg" -czf "$d/$asset" keld-agent-sidecar
  echo "$(sha "$d/$asset")  $asset" > "$d/$asset.sha256"
  rm -rf "$tmp/pkg"
}
mkdir -p "$tmp/mirror"
publish releases/v9.9.9 v9.9.9
publish prereleases/v9.9.10-rc.1 v9.9.10-rc.1
echo '{"tag_name":"v9.9.9","assets":[]}' > "$tmp/mirror/latest.json"

port=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')
python3 -m http.server "$port" --bind 127.0.0.1 --directory "$tmp/mirror" >/dev/null 2>&1 &
srv=$!
for _ in $(seq 1 50); do curl -fsS "http://127.0.0.1:$port/latest.json" >/dev/null 2>&1 && break; sleep 0.1; done

run() { # run <dest> [env...]
  local dest=$1; shift
  env KELD_RELEASES_URL="http://127.0.0.1:$port" KELD_SIDECAR_ASSET="$asset" KELD_FETCH_RETRY_S=0 "$@" \
    bash "$here/scripts/fetch-sidecar.sh" "$dest" >"$tmp/out" 2>&1
}

# 1) the stable tag comes from latest.json and the sidecar lands, executable, under <dest>
run "$tmp/d1"; rc=$?
if [ $rc -eq 0 ] && [ -x "$tmp/d1/keld-agent-sidecar/keld-agent-sidecar" ] && grep -q v9.9.9 "$tmp/d1/keld-agent-sidecar/VERSION"; then
  ok "stable: tag from latest.json, installed under <dest>"
else bad "stable install (rc=$rc): $(cat "$tmp/out")"; fi

# 2) an explicit rc tag reads prereleases/<tag>/ — install.sh's rule ('-' means pre-release)
run "$tmp/d2" KELD_SIDECAR_TAG=v9.9.10-rc.1; rc=$?
if [ $rc -eq 0 ] && grep -q v9.9.10-rc.1 "$tmp/d2/keld-agent-sidecar/VERSION" 2>/dev/null; then
  ok "rc tag: fetched from prereleases/"
else bad "rc tag (rc=$rc): $(cat "$tmp/out")"; fi

# 3) a checksum mismatch FAILS and installs nothing — a corrupt download never lands
echo "0000000000000000000000000000000000000000000000000000000000000000  $asset" > "$tmp/mirror/releases/v9.9.9/$asset.sha256"
run "$tmp/d3"; rc=$?
if [ $rc -ne 0 ] && [ ! -e "$tmp/d3/keld-agent-sidecar" ]; then
  ok "checksum mismatch: fails, installs nothing"
else bad "checksum mismatch was accepted (rc=$rc)"; fi
publish releases/v9.9.9 v9.9.9

# 4) a missing asset FAILS (the mirror lacks what installers fetch)
mv "$tmp/mirror/releases/v9.9.9/$asset" "$tmp/mirror/releases/v9.9.9/_hidden"
run "$tmp/d4"; rc=$?
[ $rc -ne 0 ] && ok "missing asset: fails" || bad "missing asset was not a failure"
mv "$tmp/mirror/releases/v9.9.9/_hidden" "$tmp/mirror/releases/v9.9.9/$asset"

# 5) a missing .sha256 FAILS rather than installing unverified
mv "$tmp/mirror/releases/v9.9.9/$asset.sha256" "$tmp/mirror/releases/v9.9.9/_hidden.sha256"
run "$tmp/d5"; rc=$?
if [ $rc -ne 0 ] && [ ! -e "$tmp/d5/keld-agent-sidecar" ]; then ok "missing .sha256: fails, installs nothing"
else bad "missing .sha256 was accepted (rc=$rc)"; fi
mv "$tmp/mirror/releases/v9.9.9/_hidden.sha256" "$tmp/mirror/releases/v9.9.9/$asset.sha256"

# 6) latest.json that is not JSON (a captive portal) FAILS with a clear message
echo '<html>login</html>' > "$tmp/mirror/latest.json"
run "$tmp/d6"; rc=$?
if [ $rc -ne 0 ] && grep -qi 'tag' "$tmp/out"; then ok "non-JSON latest.json: fails and says why"
else bad "non-JSON latest.json (rc=$rc): $(cat "$tmp/out")"; fi

# 7) the shipped default is the mirror, not GitHub
grep -qF 'KELD_RELEASES_URL:-https://dl.keld.co' "$here/scripts/fetch-sidecar.sh" \
  && ok "defaults to https://dl.keld.co" || bad "default host is not https://dl.keld.co"

exit $fail
