#!/usr/bin/env bash
# Container entrypoint for the Linux conformance leg.
#
# It does three things the host leg does not have to: say out loud what a
# container cannot prove, put a usable analysis sidecar on the image, and force
# `tool@latest` rather than "whatever this machine happens to have installed".
# Then it hands over to the same run-chain.sh the Makefile calls.
set -uo pipefail

REPO_DIR=${KELD_CONFORM_REPO:-/repo}
WORK=${KELD_CONFORM_WORK:-/work}

say() { echo "conformance(container): $*"; }

# ⚠️ Stated every run, deliberately. A green container result covers the tool,
# the adapters and the five lanes; it does NOT cover service registration or the
# packaged installers (AC-12), which need launchd/systemd/Inno on a real machine.
say "service registration is NOT exercised in a container (no launchd, no systemd user bus):"
say "  the daemon runs in the FOREGROUND, exactly as the host harness runs it."
say "  AC-12's installer path is proven by the Tart VM leg and by the macOS/Windows CI runners."

# --- the analysis sidecar ----------------------------------------------------
#
# lib/isolate.sh resolves, in order: $KELD_CONFORM_PYTHON, the `make sidecar`
# venv, then a FROZEN sidecar under ~/.local/bin. Building the venv inside the
# image would mean ~5 GB of torch per build, so the container uses the frozen
# tarball a Linux user actually installs — which also makes the container leg
# test the artifact rather than the source tree.
ensure_sidecar() {
  if [ -n "${KELD_CONFORM_PYTHON:-}" ] && [ -x "${KELD_CONFORM_PYTHON}" ]; then
    say "sidecar: KELD_CONFORM_PYTHON=$KELD_CONFORM_PYTHON"
    return 0
  fi
  local dest="$HOME/.local/bin"
  if [ -x "$dest/keld-agent-sidecar/keld-agent-sidecar" ]; then
    say "sidecar: already present at $dest/keld-agent-sidecar ($(cat "$dest/keld-agent-sidecar/VERSION" 2>/dev/null || echo 'no VERSION'))"
    return 0
  fi
  if [ "${KELD_CONFORM_FETCH_SIDECAR:-1}" != "1" ]; then
    say "sidecar: absent and fetching is off (KELD_CONFORM_FETCH_SIDECAR=0) — run-chain.sh will say so and stop"
    return 0
  fi

  # The public release mirror (dl.keld.co), not GitHub's API: keld-signal is going private, and an
  # unauthenticated releases/latest call stops answering the moment it does.
  local releases=${KELD_RELEASES_URL:-https://dl.keld.co}
  releases=${releases%/}
  local tag=${KELD_CONFORM_SIDECAR_TAG:-}
  if [ -z "$tag" ]; then
    tag=$(curl -fsSL "${releases}/latest.json" | jq -r .tag_name 2>/dev/null)
  fi
  [ -n "$tag" ] && [ "$tag" != "null" ] || { say "sidecar: could not resolve a release tag; set KELD_CONFORM_SIDECAR_TAG"; return 0; }

  local arch archive url
  arch=$(dpkg --print-architecture)
  # ⚠️ Only linux/amd64 is PUBLISHED (installers.yml freezes one Linux sidecar,
  # in a manylinux_2_28 amd64 image). On an arm64 host — i.e. Docker Desktop or
  # OrbStack on an Apple Silicon Mac — there is no matching asset, so a full
  # container chain needs either DOCKER_DEFAULT_PLATFORM=linux/amd64 (emulated,
  # and torch under qemu is impractical) or an amd64 Linux host, which is what
  # ubuntu-latest on GitHub is. Said plainly rather than failing as "download
  # failed".
  if [ "$arch" != "amd64" ]; then
    say "sidecar: no keld-agent-sidecar_linux_${arch} asset is published (amd64 only);"
    say "  run this leg on an amd64 host, or mount a sidecar and set KELD_CONFORM_PYTHON."
  fi
  archive="keld-agent-sidecar_linux_${arch}.tar.gz"
  local channel=releases
  case "$tag" in *-*) channel=prereleases ;; esac
  url="${KELD_DOWNLOAD_BASE:-${releases}/${channel}}/${tag}/${archive}"
  say "sidecar: fetching $url"
  mkdir -p "$dest" /tmp/sc
  if ! curl -fsSL "$url" -o /tmp/sc/"$archive"; then
    say "sidecar: download failed — the checkpoints that need the store will report it"
    return 0
  fi
  tar -xzf /tmp/sc/"$archive" -C /tmp/sc || { say "sidecar: extract failed"; return 0; }
  rm -rf "$dest/keld-agent-sidecar"
  mv /tmp/sc/keld-agent-sidecar "$dest/keld-agent-sidecar" || { say "sidecar: install failed"; return 0; }
  chmod +x "$dest/keld-agent-sidecar/keld-agent-sidecar" 2>/dev/null || true
  say "sidecar: installed $tag at $dest/keld-agent-sidecar"
}

ensure_sidecar

# The container is disposable, so the tool is installed at @latest into an
# isolated npm prefix rather than borrowed from the machine (tools.sh reads
# this switch). That is what makes this leg the tool-release canary.
export KELD_CONFORM_INSTALL=${KELD_CONFORM_INSTALL:-1}

mkdir -p "$WORK"
cd "$REPO_DIR" || { echo "conformance(container): no repo at $REPO_DIR" >&2; exit 2; }

ARGS=()
if [ $# -gt 0 ]; then
  ARGS=("$@")
else
  [ -n "${TOOL:-}" ] || { echo "conformance(container): set TOOL=<id> (or pass run-chain.sh flags)" >&2; exit 2; }
  ARGS=(--tool "$TOOL")
  [ -n "${CHAIN:-}" ] && ARGS+=(--chain "$CHAIN")
  [ -n "${SEED:-}" ]  && ARGS+=(--seed "$SEED")
  ARGS+=(--work "$WORK")
fi

say "exec run-chain.sh ${ARGS[*]}"
exec bash "$REPO_DIR/scripts/conformance/run-chain.sh" "${ARGS[@]}"
