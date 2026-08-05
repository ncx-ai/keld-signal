#!/bin/bash
# Keld setup — runs after install. Redeems your one-time setup code (from the Keld
# download page) for a non-interactive login, configures your AI tools, then starts
# the background agent. Safe to re-run.
set -uo pipefail
AGENT="${KELD_AGENT_BIN:-/usr/local/bin/keld-agent}"
PREFIX="${KELD_PREFIX:-/usr/local/keld}"
REPO="ncx-ai/keld-signal"
echo; echo "==== Set up Keld ===="; echo

# ── ML sidecar ────────────────────────────────────────────────────────────────
# The pkg deliberately ships without the frozen GLiNER2 sidecar (~15k files /
# ~190MB of torch): Apple's notary service scans every file, which put release
# builds hours into an unbounded queue. Fetch it here instead.
#
# Destination is ~/.local/bin, NOT $PREFIX: this script runs as the logged-in user
# while $PREFIX is root-owned, and ~/.local/bin is already a well-known
# sidecarBinPath() search dir on darwin — so no sudo prompt, and the daemon finds
# it with no extra configuration. Same place the curl|sh installer puts it.
#
# Onboarding already requires the network (browser device-auth login), so this
# adds no new connectivity requirement. Non-fatal if it fails: the binaries are
# already installed, enrichment jobs spool until a sidecar appears, and re-running
# this script retries.
fetch_sidecar() {
  dest="${HOME}/.local/bin"
  if [ -x "${dest}/keld-agent-sidecar/keld-agent-sidecar" ]; then
    echo "  ✓ ML sidecar already present → ${dest}/keld-agent-sidecar"
    return 0
  fi
  # Apple Silicon only — there is no darwin/amd64 sidecar asset to fetch (PyTorch
  # dropped Intel-mac wheels after 2.2.2), so say so rather than 404 on a bad URL.
  arch=$(uname -m)
  case "$arch" in
    arm64 | aarch64) arch=arm64 ;;
    *) echo "  ! Keld's ML sidecar ships for Apple Silicon only (this Mac reports ${arch})." >&2
       return 1 ;;
  esac
  # Pin to the tag this pkg was built from; fall back to latest for dry-run builds.
  tag=""
  [ -f "${PREFIX}/VERSION" ] && tag=$(tr -d ' \n' < "${PREFIX}/VERSION")
  case "$tag" in ""|*dryrun*)
    tag=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
          | grep -o '"tag_name": *"[^"]*"' | head -1 | cut -d'"' -f4) ;;
  esac
  [ -n "$tag" ] || { echo "  ! could not determine a release to fetch the ML sidecar from" >&2; return 1; }
  url="https://github.com/${REPO}/releases/download/${tag}/keld-agent-sidecar_darwin_${arch}.tar.gz"
  echo "  … downloading ML sidecar (${tag}, ~190MB) → ${dest}"
  mkdir -p "$dest"
  rm -rf "${dest}/keld-agent-sidecar"   # clear a partial/older extract so the dir lands clean
  if curl -fsSL "$url" | tar -xz -C "$dest"; then
    chmod +x "${dest}/keld-agent-sidecar/keld-agent-sidecar" 2>/dev/null || true
    echo "  ✓ ML sidecar → ${dest}/keld-agent-sidecar"
  else
    echo "  ! ML sidecar download failed ($url)" >&2
    echo "    Keld will still install and collect telemetry, but on-device enrichment" >&2
    echo "    stays paused (jobs queue) until it is present. Re-run this script to retry." >&2
    return 1
  fi
}
fetch_sidecar || true
echo
printf "Paste your setup code from the Keld download page (or press Enter to log in with a browser): "
read -r CODE
if [ -n "$CODE" ]; then
  # keld-agent install redeems the code (keld login --code), configures tools, then
  # starts the agent. Fall back to interactive install (browser login) if the code fails.
  "$AGENT" install --code "$CODE" || { echo "Setup code didn't work; falling back to browser login…"; "$AGENT" install --yes || exit 1; }
else
  "$AGENT" install --yes || exit 1
fi
echo; echo "Keld is set up and running. You can close this window."; echo
echo "(Re-run anytime: /usr/local/keld/onboard.command)"
