#!/usr/bin/env bash
# Tests for scripts/verify-mirror.sh against a local HTTP server laid out like the
# release mirror. No network beyond 127.0.0.1. Run: bash scripts/verify-mirror_test.sh
set -uo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
srv=""
trap '[ -n "$srv" ] && kill "$srv" 2>/dev/null; rm -rf "$tmp"' EXIT
fail=0
ok()  { echo "ok: $1"; }
bad() { echo "FAIL: $1"; fail=1; }

# The release as publish-releases.yml downloads it (`out/`), and the mirror copy.
mkdir -p "$tmp/out"
head -c 200000 /dev/urandom > "$tmp/out/keld-agent-sidecar_linux_amd64.tar.gz"
echo "abc123  keld-agent-sidecar_linux_amd64.tar.gz" > "$tmp/out/keld-agent-sidecar_linux_amd64.tar.gz.sha256"
printf 'aaa  keld_linux_amd64.tar.gz\n' > "$tmp/out/checksums.txt"
head -c 5000 /dev/urandom > "$tmp/out/keld_linux_amd64.tar.gz"
reset_mirror() {
  rm -rf "$tmp/mirror"; mkdir -p "$tmp/mirror/releases/v9.9.9" "$tmp/mirror/prereleases/v9.9.10-rc.1"
  cp "$tmp/out/"* "$tmp/mirror/releases/v9.9.9/"
  cp "$tmp/out/"* "$tmp/mirror/prereleases/v9.9.10-rc.1/"
  echo '{"tag_name":"v9.9.9","assets":[]}' > "$tmp/mirror/latest.json"
}
reset_mirror

port=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')
python3 -m http.server "$port" --bind 127.0.0.1 --directory "$tmp/mirror" >/dev/null 2>&1 &
srv=$!
for _ in $(seq 1 50); do curl -fsS "http://127.0.0.1:$port/latest.json" >/dev/null 2>&1 && break; sleep 0.1; done

run() { # run <tag> [--latest]
  KELD_RELEASES_URL="http://127.0.0.1:$port" KELD_VERIFY_ATTEMPTS=1 \
    bash "$here/scripts/verify-mirror.sh" "$tmp/out" "$@" >"$tmp/log" 2>&1
}

run v9.9.9 --latest && ok "a complete stable mirror passes, latest.json included" || bad "complete mirror failed: $(cat "$tmp/log")"
run v9.9.10-rc.1 && ok "an rc is checked under prereleases/" || bad "rc under prereleases/ failed: $(cat "$tmp/log")"

rm "$tmp/mirror/releases/v9.9.9/keld-agent-sidecar_linux_amd64.tar.gz"
run v9.9.9 && bad "a missing sidecar tarball passed" || { grep -q 'keld-agent-sidecar_linux_amd64.tar.gz' "$tmp/log" && ok "a missing asset fails, naming it" || bad "missing asset not named: $(cat "$tmp/log")"; }
reset_mirror

head -c 100 /dev/urandom > "$tmp/mirror/releases/v9.9.9/keld_linux_amd64.tar.gz"
run v9.9.9 && bad "a truncated asset passed" || ok "a size mismatch fails"
reset_mirror

echo "zzz999  keld-agent-sidecar_linux_amd64.tar.gz" > "$tmp/mirror/releases/v9.9.9/keld-agent-sidecar_linux_amd64.tar.gz.sha256"
run v9.9.9 && bad "a wrong .sha256 of the same size passed" || ok "a text asset's content is compared, not just its size"
reset_mirror

echo '{"tag_name":"v9.9.8","assets":[]}' > "$tmp/mirror/latest.json"
run v9.9.9 --latest && bad "a stale latest.json passed" || ok "latest.json naming another tag fails"
run v9.9.9 && ok "without --latest, latest.json is not consulted (a backfill leaves it alone)" || bad "non-latest run consulted latest.json"
reset_mirror

grep -qF 'KELD_RELEASES_URL:-https://dl.keld.co' "$here/scripts/verify-mirror.sh" \
  && ok "defaults to https://dl.keld.co" || bad "default host is not https://dl.keld.co"

exit $fail
