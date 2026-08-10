#!/usr/bin/env bash
# Standalone test for verify-release-assets.sh, matching the repo's shell-test
# convention (scripts/test-install-sh.sh, sidecar/test_build_freeze.sh).
# Fully offline: KELD_VERIFY_ASSETS_JSON substitutes a fixture for `gh release view`,
# and a stub `gh` on PATH records any call the script makes.
set -euo pipefail

d="$(cd "$(dirname "$0")" && pwd)"
script="$d/verify-release-assets.sh"
test -x "$script" || { echo "missing or non-executable $script"; exit 1; }

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
fails=0

# Stub `gh` so tests can assert whether the script tried to mutate the release.
mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'STUB'
#!/usr/bin/env bash
echo "$*" >> "$GH_CALLS"
exit 0
STUB
chmod +x "$tmp/bin/gh"
export PATH="$tmp/bin:$PATH"

# Build a {name,size} JSON array from "name:size" pairs.
mkjson() {
  local out=""
  for pair in "$@"; do
    out+="{\"name\":\"${pair%:*}\",\"size\":${pair##*:}}"$'\n'
  done
  printf '%s' "$out" | jq -s '.'
}

# The complete 10-asset manifest for tag v0.20.0.
complete_pairs=(
  "checksums.txt:451"
  "keld_linux_amd64.tar.gz:100"
  "keld_linux_arm64.tar.gz:100"
  "keld_darwin_amd64.tar.gz:100"
  "keld_darwin_arm64.tar.gz:100"
  "keld_windows_amd64.zip:100"
  "keld-agent-sidecar_linux_amd64.tar.gz:100"
  "keld-agent-sidecar_darwin_arm64.tar.gz:100"
  "keld-v0.20.0-arm64.pkg:100"
  "keld-setup.exe:100"
)

# run <name> <tag> <expected-exit> <fixture-json> [env assignments...]
run() {
  local name="$1" tag="$2" want="$3" json="$4"; shift 4
  local f="$tmp/assets.json"; printf '%s' "$json" > "$f"
  export GH_CALLS="$tmp/gh-calls"; : > "$GH_CALLS"
  local out rc=0
  out=$(env KELD_VERIFY_ASSETS_JSON="$f" "$@" "$script" "$tag" 2>&1) || rc=$?
  if [ "$rc" -ne "$want" ]; then
    echo "FAIL: $name — exit $rc, wanted $want"; echo "$out" | sed 's/^/    /'
    fails=$((fails+1)); return 1
  fi
  echo "PASS: $name"
  LAST_OUT="$out"
  return 0
}

# 1. Complete manifest passes and does not touch the release.
if run "complete manifest -> 0" v0.20.0 0 "$(mkjson "${complete_pairs[@]}")"; then
  [ -s "$tmp/gh-calls" ] && { echo "FAIL: complete manifest invoked gh: $(cat "$tmp/gh-calls")"; fails=$((fails+1)); }
fi

# 2. The real v0.20.0 shape: the Linux sidecar tarball absent.
without_linux=()
for p in "${complete_pairs[@]}"; do
  [ "${p%:*}" = "keld-agent-sidecar_linux_amd64.tar.gz" ] || without_linux+=("$p")
done
if run "missing linux sidecar -> 1" v0.20.0 1 "$(mkjson "${without_linux[@]}")"; then
  echo "$LAST_OUT" | grep -q "keld-agent-sidecar_linux_amd64.tar.gz" \
    || { echo "FAIL: did not name the missing asset"; fails=$((fails+1)); }
fi

# 3. A present-but-empty asset is as broken as an absent one.
zeroed=()
for p in "${complete_pairs[@]}"; do
  if [ "${p%:*}" = "keld-agent-sidecar_linux_amd64.tar.gz" ]; then
    zeroed+=("keld-agent-sidecar_linux_amd64.tar.gz:0")
  else zeroed+=("$p"); fi
done
if run "zero-byte asset -> 1" v0.20.0 1 "$(mkjson "${zeroed[@]}")"; then
  echo "$LAST_OUT" | grep -qi "zero" \
    || { echo "FAIL: did not flag the zero-byte asset"; fails=$((fails+1)); }
fi

# 4. The pkg name must track the tag, not be a fixed literal.
if run "pkg name tracks tag -> 1" v1.2.3 1 "$(mkjson "${complete_pairs[@]}")"; then
  echo "$LAST_OUT" | grep -q "keld-v1.2.3-arm64.pkg" \
    || { echo "FAIL: did not expect the tag-derived pkg name"; fails=$((fails+1)); }
fi

# 5. Demotion is opt-in: incomplete + flag unset must not call gh.
if run "demote off by default" v0.20.0 1 "$(mkjson "${without_linux[@]}")"; then
  [ -s "$tmp/gh-calls" ] && { echo "FAIL: demoted without KELD_VERIFY_DEMOTE: $(cat "$tmp/gh-calls")"; fails=$((fails+1)); }
fi

# 6. Demotion happens when explicitly enabled.
if run "demote on request" v0.20.0 1 "$(mkjson "${without_linux[@]}")" KELD_VERIFY_DEMOTE=1; then
  grep -q "release edit v0.20.0 --prerelease" "$tmp/gh-calls" \
    || { echo "FAIL: expected demotion call, got: $(cat "$tmp/gh-calls")"; fails=$((fails+1)); }
fi

# 7. Every missing asset is reported, not just the first.
minimal="$(mkjson "checksums.txt:451")"
if run "reports all missing -> 1" v0.20.0 1 "$minimal"; then
  for want in keld_linux_amd64.tar.gz keld-setup.exe keld-agent-sidecar_darwin_arm64.tar.gz; do
    echo "$LAST_OUT" | grep -q "$want" \
      || { echo "FAIL: did not report $want"; fails=$((fails+1)); }
  done
fi

# 8. No tag argument is a usage error.
rc=0; "$script" >/dev/null 2>&1 || rc=$?
[ "$rc" -eq 2 ] && echo "PASS: no tag -> 2" \
  || { echo "FAIL: no tag -> exit $rc, wanted 2"; fails=$((fails+1)); }

[ "$fails" -eq 0 ] || { echo; echo "$fails check(s) failed"; exit 1; }
echo; echo "verify-release-assets: all checks passed"
