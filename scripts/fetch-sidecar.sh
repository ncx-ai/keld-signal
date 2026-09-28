#!/usr/bin/env bash
# fetch-sidecar.sh <dest-dir> — install the PUBLISHED analysis sidecar from the release
# mirror (https://dl.keld.co), exactly the way users get it: the tag from latest.json,
# the tarball from releases/<tag>/ (prereleases/<tag>/ when the tag has a '-'), and its
# <tarball>.sha256 checked before anything is extracted. Lands <dest>/keld-agent-sidecar/.
#
# ⚠️ WHY THIS EXISTS: every install path (install.sh, install.ps1, onboard.command, the
# macOS wizard, auto-update) fetches from the mirror since the repo went private, but the
# conformance chain fetched its sidecar from GitHub with `gh release download`. So nothing
# in CI ever downloaded from the host users download from, and a broken mirror would have
# been found by a user's failed install. The chain now installs through this script.
#
# Env:
#   KELD_RELEASES_URL   mirror root (default https://dl.keld.co)
#   KELD_SIDECAR_TAG    release tag (default: latest.json's tag_name)
#   KELD_SIDECAR_ASSET  tarball name (default: from this host — linux_amd64, darwin_arm64)
#   KELD_FETCH_ATTEMPTS attempts per download (default 3); KELD_FETCH_RETRY_S base backoff (10)
set -uo pipefail

die() { echo "fetch-sidecar: $*" >&2; exit 1; }
dest=${1:?usage: fetch-sidecar.sh <dest-dir>}
base=${KELD_RELEASES_URL:-https://dl.keld.co}
base=${base%/}
attempts=${KELD_FETCH_ATTEMPTS:-3}
backoff=${KELD_FETCH_RETRY_S:-10}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}

asset=${KELD_SIDECAR_ASSET:-}
if [ -z "$asset" ]; then
  case "$(uname -s)/$(uname -m)" in
    Linux/x86_64|Linux/amd64) asset="keld-agent-sidecar_linux_amd64.tar.gz" ;;
    Darwin/arm64)             asset="keld-agent-sidecar_darwin_arm64.tar.gz" ;;
    *) die "no sidecar tarball is published for $(uname -s)/$(uname -m) (linux/amd64 and darwin/arm64 only)" ;;
  esac
fi

tag=${KELD_SIDECAR_TAG:-}
if [ -z "$tag" ]; then
  # latest.json moves only after a release passed verify-release AND was uploaded
  # whole (publish-releases.yml), so it never names a release still being built.
  tag=$(curl -fsSL "$base/latest.json" 2>/dev/null | python3 -c 'import json,sys
try: print(json.load(sys.stdin)["tag_name"])
except Exception: pass' 2>/dev/null)
  [ -n "$tag" ] || die "could not read a release tag from $base/latest.json (not JSON, or unreachable)"
fi
case "$tag" in *-*) prefix=prereleases ;; *) prefix=releases ;; esac
url="$base/$prefix/$tag/$asset"

tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
get() { # get <url> <file> — retried; a ~440 MB GET has failed mid-stream before
  local i
  for i in $(seq 1 "$attempts"); do
    curl -fsSL "$1" -o "$2" && return 0
    [ "$i" -lt "$attempts" ] && { echo "fetch-sidecar: attempt $i for $1 failed; retrying" >&2; sleep $((i * backoff)); }
  done
  return 1
}
echo "fetch-sidecar: $url"
get "$url" "$tmp/$asset" || die "could not download $url"
get "$url.sha256" "$tmp/$asset.sha256" || die "could not download $url.sha256 — refusing to install an unverified sidecar"
want=$(awk '{print $1; exit}' "$tmp/$asset.sha256")
got=$(sha256_file "$tmp/$asset")
[ -n "$want" ] && [ "$want" = "$got" ] || die "checksum mismatch for $asset (published $want, downloaded $got)"

mkdir -p "$tmp/x" "$dest"
tar -xzf "$tmp/$asset" -C "$tmp/x" || die "could not extract $asset"
[ -x "$tmp/x/keld-agent-sidecar/keld-agent-sidecar" ] || die "$asset has no keld-agent-sidecar/keld-agent-sidecar"
rm -rf "$dest/keld-agent-sidecar"
mv "$tmp/x/keld-agent-sidecar" "$dest/keld-agent-sidecar"
echo "fetch-sidecar: installed $tag ($(cat "$dest/keld-agent-sidecar/VERSION" 2>/dev/null || echo 'no VERSION')) at $dest/keld-agent-sidecar"
