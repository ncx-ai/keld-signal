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

# ── Latest release tag ────────────────────────────────────────────────────────
api_url="https://api.github.com/repos/${REPO}/releases/latest"
tag=$(curl -fsSL "$api_url" \
  | grep -o '"tag_name": *"[^"]*"' \
  | head -1 \
  | cut -d'"' -f4)

if [ -z "$tag" ]; then
  echo "keld installer: could not determine the latest release tag." >&2
  echo "  Check your network connection or visit:" >&2
  echo "  https://github.com/${REPO}/releases/latest" >&2
  exit 1
fi

# ── Download and extract ──────────────────────────────────────────────────────
archive="keld_${os}_${arch}.tar.gz"
url="https://github.com/${REPO}/releases/download/${tag}/${archive}"

echo "Installing keld ${tag} (${os}/${arch})..."
echo "  Source:      ${url}"
echo "  Destination: ${DEST}/keld"

mkdir -p "$DEST"

if ! curl -fsSL "$url" | tar -xz -C "$DEST" keld; then
  echo "" >&2
  echo "keld installer: download or extraction failed." >&2
  echo "  URL: ${url}" >&2
  echo "  Make sure the release exists and your network can reach github.com." >&2
  exit 1
fi

chmod +x "${DEST}/keld"

echo ""
echo "keld ${tag} installed to ${DEST}/keld"
echo ""
echo "Next steps:"
echo "  1. Ensure ${DEST} is on your PATH."
echo "     If it is not, add this to your shell profile (~/.bashrc, ~/.zshrc, etc.):"
echo "       export PATH=\"\${HOME}/.local/bin:\${PATH}\""
echo "  2. Run:  keld login"
echo "  3. Run:  keld signal setup"
echo ""
echo "Note: macOS users may see a Gatekeeper warning on first run."
echo "  To allow the binary: System Settings > Privacy & Security > Allow."
echo "  Code signing is a planned follow-up."
