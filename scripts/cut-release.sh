#!/usr/bin/env bash
# Cut a new Keld release: compute the next version (minor bump by default, or a
# version passed as $1), then create and push a vX.Y.Z tag. Pushing the tag is the
# CI/CD kickoff — release.yml runs GoReleaser (builds keld/keld-agent, stamps
# internal/version.CLI from the tag, publishes the GitHub Release) and installers.yml
# fires on release:published to build + attach the sidecar and native installers.
#
# Usage:
#   scripts/cut-release.sh                 # minor bump (v0.3.4 -> v0.4.0)
#   scripts/cut-release.sh 1.2.3           # explicit version (v prefix optional)
#   scripts/cut-release.sh -y 1.2.3        # skip the confirmation prompt
#
# Env:
#   REMOTE=origin   git remote to push to
#   FORCE=1         same as -y (skip confirmation)
#   DRY_RUN=1       compute + print the plan, then exit before tagging/pushing
set -euo pipefail

REMOTE="${REMOTE:-origin}"
ASSUME_YES="${FORCE:-}"

die() { echo "cut-release: $*" >&2; exit 1; }

# Parse args: an optional -y flag and an optional version.
VERSION_ARG=""
for a in "$@"; do
  case "$a" in
    -y|--yes) ASSUME_YES=1 ;;
    -*) die "unknown flag: $a" ;;
    *)
      [ -z "$VERSION_ARG" ] || die "unexpected extra argument: $a"
      VERSION_ARG="$a"
      ;;
  esac
done

command -v git >/dev/null 2>&1 || die "git not found"

# Work from the repo root regardless of CWD.
ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || die "not inside a git repository"
cd "$ROOT"

echo "cut-release: fetching tags from ${REMOTE}…"
git fetch --tags --quiet "$REMOTE" || die "git fetch failed"

# --- Guardrails ------------------------------------------------------------
branch="$(git rev-parse --abbrev-ref HEAD)"
[ "$branch" = "main" ] || die "must be on 'main' (currently on '$branch')"

[ -z "$(git status --porcelain)" ] || die "working tree is not clean — commit or stash first"

git rev-parse --verify --quiet "$REMOTE/main" >/dev/null || die "$REMOTE/main not found (fetch failed?)"
local_head="$(git rev-parse HEAD)"
remote_head="$(git rev-parse "$REMOTE/main")"
[ "$local_head" = "$remote_head" ] || die "local main is not in sync with $REMOTE/main — push/pull first"

# --- Version resolution ----------------------------------------------------
# Highest stable tag (no prerelease suffix), or 0.0.0 if none exist yet.
latest="$(git tag --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || true)"
[ -n "$latest" ] || latest="v0.0.0"

if [ -n "$VERSION_ARG" ]; then
  new="v${VERSION_ARG#v}"
  echo "$new" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$' \
    || die "invalid version '$VERSION_ARG' (expected X.Y.Z or X.Y.Z-prerelease)"
else
  # Minor bump, reset patch: v0.3.4 -> v0.4.0
  base="${latest#v}"
  major="${base%%.*}"
  rest="${base#*.}"
  minor="${rest%%.*}"
  new="v${major}.$((minor + 1)).0"
fi

git rev-parse --verify --quiet "refs/tags/$new" >/dev/null && die "tag $new already exists locally"
if git ls-remote --tags --quiet "$REMOTE" "refs/tags/$new" | grep -q .; then
  die "tag $new already exists on $REMOTE"
fi

commit_short="$(git rev-parse --short HEAD)"
commit_subject="$(git log -1 --pretty=%s)"

echo
echo "  current : $latest"
echo "  new     : $new"
echo "  commit  : $commit_short  $commit_subject"
echo "  remote  : $REMOTE"
echo
echo "Pushing $new triggers CI/CD: GoReleaser publishes the GitHub Release, then"
echo "installers.yml builds + attaches the sidecar and native installers."

if [ -n "${DRY_RUN:-}" ]; then
  echo
  echo "DRY_RUN=1 — not tagging or pushing."
  exit 0
fi

if [ -z "$ASSUME_YES" ]; then
  printf '\nCut release %s? [y/N] ' "$new"
  read -r reply
  case "$reply" in
    y|Y|yes|YES) ;;
    *) die "aborted." ;;
  esac
fi

git tag -a "$new" -m "release $new"
git push "$REMOTE" "$new"

# Derive the Actions URL from the remote (best-effort).
url="$(git remote get-url "$REMOTE" 2>/dev/null || true)"
slug="$(echo "$url" | sed -E 's#(git@|https://)github.com[:/]##; s#\.git$##')"
echo
echo "Pushed $new. Watch the build:"
if [ -n "$slug" ]; then
  echo "  https://github.com/$slug/actions"
fi
