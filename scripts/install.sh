#!/bin/sh
# keld installer — POSIX sh, no jq required
# Usage: curl -fsSL https://raw.githubusercontent.com/ncx-ai/keld-cli/main/scripts/install.sh | sh
set -e

REPO="ncx-ai/keld-cli"
DEST="${KELD_INSTALL_DIR:-${HOME}/.local/bin}"

# ── OS detection ──────────────────────────────────────────────────────────────
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux)  os=linux ;;
  darwin) os=darwin ;;
  *)
    echo "keld installer: unsupported operating system: $os" >&2
    echo "  Supported: Linux, macOS (Darwin)." >&2
    echo "  For Windows, use the PowerShell installer:" >&2
    echo "  irm https://raw.githubusercontent.com/ncx-ai/keld-cli/main/scripts/install.ps1 | iex" >&2
    exit 1
    ;;
esac

# ── Arch detection ────────────────────────────────────────────────────────────
arch=$(uname -m)
case "$arch" in
  x86_64|amd64)   arch=amd64 ;;
  arm64|aarch64)  arch=arm64 ;;
  *)
    echo "keld installer: unsupported architecture: $arch" >&2
    echo "  Supported: x86_64/amd64, arm64/aarch64." >&2
    exit 1
    ;;
esac

# ── Release tag ───────────────────────────────────────────────────────────────
# KELD_RELEASE_TAG overrides the GitHub API lookup (pin a version, or test offline
# against a local server where the "latest" API isn't available).
tag="${KELD_RELEASE_TAG:-}"
if [ -z "$tag" ]; then
  api_url="https://api.github.com/repos/${REPO}/releases/latest"
  tag=$(curl -fsSL "$api_url" \
    | grep -o '"tag_name": *"[^"]*"' \
    | head -1 \
    | cut -d'"' -f4)
fi

if [ -z "$tag" ]; then
  echo "keld installer: could not determine the latest release tag." >&2
  echo "  Check your network connection or visit:" >&2
  echo "  https://github.com/${REPO}/releases/latest" >&2
  echo "  (or set KELD_RELEASE_TAG to pin a version)." >&2
  exit 1
fi

# ── Download and extract ──────────────────────────────────────────────────────
# KELD_DOWNLOAD_BASE overrides the release download host — point it at a local
# file server (e.g. http://localhost:8000) to test the installer without a real
# release. Default: the GitHub release download path.
dl_base="${KELD_DOWNLOAD_BASE:-https://github.com/${REPO}/releases/download}"
archive="keld_${os}_${arch}.tar.gz"
url="${dl_base}/${tag}/${archive}"

echo "Installing keld ${tag} (${os}/${arch})..."
echo "  Source:      ${url}"
echo "  Destination: ${DEST}/keld"

mkdir -p "$DEST"

if ! curl -fsSL "$url" | tar -xz -C "$DEST"; then
  echo "" >&2
  echo "keld installer: download or extraction failed." >&2
  echo "  URL: ${url}" >&2
  echo "  Make sure the release exists and your network can reach github.com." >&2
  exit 1
fi

chmod +x "${DEST}/keld"

if [ -f "${DEST}/keld-agent" ]; then
  chmod +x "${DEST}/keld-agent"
  if command -v systemctl >/dev/null 2>&1; then
    "${DEST}/keld-agent" install || echo "keld: could not enable keld-agent service (enable later with: keld-agent install)" >&2
  fi
fi

# Fetch the frozen GLiNER2 sidecar (large, ~hundreds of MB) BY DEFAULT — this is
# the full ML experience, matching the GUI installers. Set KELD_NO_SIDECAR=1 for
# a lean, deterministic-only install (the deterministic backend needs no sidecar).
# Linux only: macOS uses the .pkg installer; no darwin sidecar tarball is published.
if [ "$os" = "linux" ] && [ -f "${DEST}/keld-agent" ] && [ "${KELD_NO_SIDECAR:-0}" != "1" ]; then
  sc_archive="keld-agent-sidecar_${os}_${arch}.tar.gz"
  sc_url="${dl_base}/${tag}/${sc_archive}"
  echo "Fetching keld-agent-sidecar (large; set KELD_NO_SIDECAR=1 to skip)..."
  if curl -fsSL "$sc_url" | tar -xz -C "$DEST"; then
    chmod +x "${DEST}/keld-agent-sidecar/keld-agent-sidecar" 2>/dev/null || true
    echo "keld-agent-sidecar installed to ${DEST}/keld-agent-sidecar/keld-agent-sidecar"
  else
    echo "keld: sidecar download failed; continuing with the deterministic backend." >&2
  fi
fi

echo ""
echo "keld ${tag} installed to ${DEST}/keld"
if [ -f "${DEST}/keld-agent" ]; then
  echo "keld-agent installed to ${DEST}/keld-agent"
fi
echo ""
echo "Next steps:"
echo "  1. Ensure ${DEST} is on your PATH."
echo "     If it is not, add this to your shell profile (~/.bashrc, ~/.zshrc, etc.):"
echo "       export PATH=\"${DEST}:\${PATH}\""
echo "  2. Run:  keld login"
echo "  3. Run:  keld signal setup"
echo ""
echo "Note: macOS users may see a Gatekeeper warning on first run."
echo "  To allow the binary: System Settings > Privacy & Security > Allow."
echo "  Code signing is a planned follow-up."
