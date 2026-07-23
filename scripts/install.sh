#!/bin/sh
# keld installer — POSIX sh, no jq required
# Usage: curl -fsSL https://raw.githubusercontent.com/ncx-ai/keld-signal/main/scripts/install.sh | sh
set -e

REPO="ncx-ai/keld-signal"
DEST="${KELD_INSTALL_DIR:-${HOME}/.local/bin}"

# ── One-time setup code (pre-authenticated onboarding) ────────────────────────
# Precedence: a `--code <X>` argument (curl … | sh -s -- --code X) wins over the
# KELD_SETUP_CODE env var. The resolved code is handed to `keld-agent install`.
CODE="${KELD_SETUP_CODE:-}"
while [ $# -gt 0 ]; do
  case "$1" in
    --code) shift; CODE="${1:-}"; [ $# -gt 0 ] && shift ;;
    --code=*) CODE="${1#--code=}"; shift ;;
    *) shift ;;
  esac
done

# ── OS detection ──────────────────────────────────────────────────────────────
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux)  os=linux ;;
  darwin) os=darwin ;;
  *)
    echo "keld installer: unsupported operating system: $os" >&2
    echo "  Supported: Linux, macOS (Darwin)." >&2
    echo "  For Windows, use the PowerShell installer:" >&2
    echo "  irm https://raw.githubusercontent.com/ncx-ai/keld-signal/main/scripts/install.ps1 | iex" >&2
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

echo "Keld · installing  (${os}/${arch}, ${tag})"
echo ""

mkdir -p "$DEST"

echo "Downloading…"

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
fi

echo "  ✓ $(printf '%-26s' 'keld + keld-agent') → ${DEST}"

# Fetch the frozen GLiNER2 ML sidecar — REQUIRED. Keld Signal runs on-device ML;
# there is no deterministic alternative, so a failed sidecar install aborts the whole
# install rather than degrading. Linux only here: macOS ships the sidecar in its .pkg.
if [ "$os" = "linux" ] && [ -f "${DEST}/keld-agent" ]; then
  sc_archive="keld-agent-sidecar_${os}_${arch}.tar.gz"
  sc_url="${dl_base}/${tag}/${sc_archive}"
  rm -rf "${DEST}/keld-agent-sidecar"   # clear any prior sidecar (incl. a dev venv-wrapper file) so the dir extracts cleanly
  if curl -fsSL "$sc_url" | tar -xz -C "$DEST"; then
    chmod +x "${DEST}/keld-agent-sidecar/keld-agent-sidecar" 2>/dev/null || true
    echo "  ✓ $(printf '%-26s' 'ML sidecar (GLiNER2)') → ${DEST}/keld-agent-sidecar"
  else
    echo "keld: ML sidecar download failed — Keld Signal requires on-device ML and has no" >&2
    echo "  deterministic fallback. Aborting. URL: ${sc_url}" >&2
    exit 1
  fi
fi

agent_ok=1
# Pair against the origin this installer was fetched from. KELD_API_URL is set by the
# server that serves this script (see the Keld-served header) — pass it as --api-url so
# login/setup target THIS host explicitly. Without the flag, keld reuses the API URL of any
# previously stored token (device.go), so re-installing from a new origin over an existing
# install would silently keep signing in against the old host.
api_flag=""
[ -n "${KELD_API_URL:-}" ] && api_flag="--api-url ${KELD_API_URL}"
if [ -f "${DEST}/keld-agent" ]; then
  # keld-agent install owns login → signal setup → service (agent last), and the
  # TTY/headless decision. With a setup code it onboards non-interactively.
  if [ -n "$CODE" ]; then
    # shellcheck disable=SC2086  # api_flag must word-split into two args (a URL has no spaces)
    "${DEST}/keld-agent" install $api_flag --code "$CODE" \
      || { agent_ok=0; echo "keld: onboarding/agent install did not fully complete — re-run: keld-agent install --code <CODE>" >&2; }
  else
    # shellcheck disable=SC2086
    "${DEST}/keld-agent" install $api_flag \
      || { agent_ok=0; echo "keld: agent install did not complete — finish with: keld login && keld signal setup && keld-agent install" >&2; }
  fi
fi

case ":$PATH:" in
  *":${DEST}:"*) ;;
  *)
    echo ""
    echo "Note: ${DEST} is not on your PATH. Add it with:"
    echo "  export PATH=\"${DEST}:\${PATH}\""
    if [ ! -f "${DEST}/keld-agent" ]; then
      echo "  Then run:  keld login && keld signal setup"
    fi
    ;;
esac

# Idempotent-install guard: if a DIFFERENT keld earlier on PATH would shadow this
# install (e.g. a macOS .pkg copy in /usr/local/bin), say so with the exact fix.
# We don't silently rewrite dirs this installer may not own (esp. /usr/local,
# the .pkg's domain — repointing it at a curl install would break the sidecar).
shadow=""
OLD_IFS="$IFS"; IFS=:
for d in $PATH; do
  [ -n "$d" ] || continue
  if [ -x "$d/keld" ]; then
    [ "$d/keld" -ef "${DEST}/keld" ] || shadow="$d/keld"
    break
  fi
done
IFS="$OLD_IFS"
if [ -n "$shadow" ]; then
  echo "" >&2
  echo "Warning: another keld earlier on PATH will shadow this install:" >&2
  echo "    ${shadow}" >&2
  echo "  Repoint it:  ln -sf \"${DEST}/keld\" \"${shadow}\"   (and likewise keld-agent)" >&2
  echo "  or remove it, or put \"${DEST}\" first on PATH. Then run: keld doctor" >&2
fi

if [ "$os" = "darwin" ]; then
  echo ""
  echo "Note: macOS users may see a Gatekeeper warning on first run."
  echo "  To allow the binary: System Settings > Privacy & Security > Allow."
fi

echo ""
if [ -f "${DEST}/keld-agent" ] && [ "$agent_ok" = "1" ]; then
  echo "Done — Keld is set up and running."
elif [ ! -f "${DEST}/keld-agent" ]; then
  echo "Done — keld installed. Run: keld login && keld signal setup"
else
  echo "Keld installed, but onboarding did not complete — see the errors above." >&2
  exit 1
fi
