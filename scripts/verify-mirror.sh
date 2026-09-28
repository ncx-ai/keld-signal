#!/usr/bin/env bash
# verify-mirror.sh <assets-dir> <tag> [--latest] — after publish-releases.yml uploads a
# release, prove the mirror (https://dl.keld.co) SERVES it: every file in <assets-dir> (the
# release as downloaded from GitHub) must answer 200 at <mirror>/<prefix>/<tag>/<name> with
# the same size, and every small text file (*.sha256, checksums.txt) must be byte-identical.
# With --latest, latest.json must name <tag>.
#
# ⚠️ WHY THIS EXISTS: the upload step reporting success is not the same as users being able
# to download. A wrong prefix, a stale latest.json or a CDN/R2 misconfiguration would pass
# every test in CI and fail the first real install. This asks the mirror the way installers
# do, right after publishing, so a broken mirror fails the RELEASE instead.
#
# Every request carries a cache-busting query: staple.yml re-uploads a larger pkg under the
# same name, and assets are served with max-age=3600, so a plain request could see the old
# object. Env: KELD_RELEASES_URL (default https://dl.keld.co), KELD_VERIFY_ATTEMPTS (3).
set -uo pipefail

dir=${1:?usage: verify-mirror.sh <assets-dir> <tag> [--latest]}
tag=${2:?usage: verify-mirror.sh <assets-dir> <tag> [--latest]}
check_latest=${3:-}
base=${KELD_RELEASES_URL:-https://dl.keld.co}
base=${base%/}
attempts=${KELD_VERIFY_ATTEMPTS:-3}
case "$tag" in *-*) prefix=prereleases ;; *) prefix=releases ;; esac
bust="verify=$(date +%s)$$"

size_of() { wc -c < "$1" | tr -d ' '; }
remote_size() { # Content-Length of a HEAD, or "" when the mirror does not answer 200
  curl -sSI "$1?$bust" 2>/dev/null | tr -d '\r' | awk '
    NR==1 { ok = ($2 == 200) }
    tolower($1) == "content-length:" { n = $2 }
    END { if (ok) print n }'
}

bad=0
checked=0
for f in "$dir"/*; do
  [ -f "$f" ] || continue
  name=$(basename "$f")
  url="$base/$prefix/$tag/$name"
  want=$(size_of "$f")
  got=""
  for i in $(seq 1 "$attempts"); do
    got=$(remote_size "$url")
    [ "$got" = "$want" ] && break
    [ "$i" -lt "$attempts" ] && sleep $((i * 5))
  done
  if [ -z "$got" ]; then
    echo "::error::mirror does not serve $url"; bad=1; continue
  elif [ "$got" != "$want" ]; then
    echo "::error::$url is $got bytes on the mirror, $want in the release"; bad=1; continue
  fi
  case "$name" in
    *.sha256|checksums.txt|*.json|*.txt)
      if ! curl -fsSL "$url?$bust" 2>/dev/null | cmp -s - "$f"; then
        echo "::error::$url differs from the release's copy"; bad=1; continue
      fi ;;
  esac
  checked=$((checked + 1))
done
[ "$checked" -gt 0 ] || [ "$bad" -ne 0 ] || { echo "::error::no assets in $dir to verify"; bad=1; }

if [ "$check_latest" = "--latest" ]; then
  served=$(curl -fsSL "$base/latest.json?$bust" 2>/dev/null | python3 -c 'import json,sys
try: print(json.load(sys.stdin)["tag_name"])
except Exception: pass' 2>/dev/null)
  if [ "$served" != "$tag" ]; then
    echo "::error::$base/latest.json names '${served:-nothing readable}', not $tag"; bad=1
  fi
fi

if [ "$bad" -eq 0 ]; then
  echo "verify-mirror: $checked asset(s) of $tag served correctly from $base/$prefix/$tag/${check_latest:+, and latest.json names it}"
fi
exit $bad
