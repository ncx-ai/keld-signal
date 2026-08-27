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

# Everything is downloaded, verified and unpacked inside a temp workspace and
# only then moved into place. Nothing streams straight into $DEST any more: a
# `curl … | tar -xz` cannot check a hash (the bytes are gone by the time the
# exit status is known) and an aborted transfer leaves the 15,605-file sidecar
# tree half-extracted, which is worse than not installing it at all.
# The workspace lives INSIDE $DEST so the final move is a same-filesystem
# rename rather than a 15k-file copy.
work=$(mktemp -d "${DEST}/.keld-install.XXXXXX") || {
  echo "keld installer: could not create a temp directory under ${DEST}." >&2
  exit 1
}
cleanup() { rm -rf "$work"; }
trap cleanup EXIT HUP INT TERM

# ── Integrity verification ────────────────────────────────────────────────────
# sha256_file prints the SHA-256 of $1, using whichever tool the platform has:
# sha256sum on Linux (coreutils), shasum -a 256 on macOS (perl). Returns
# non-zero if neither exists.
sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    return 1
  fi
}

# checksums.txt is best-effort: goreleaser publishes it for the keld archives,
# and CI publishes a per-file <archive>.sha256 for the separately-built sidecar.
# Fetched once, up front.
checksums="${work}/checksums.txt"
curl -fsSL "${dl_base}/${tag}/checksums.txt" -o "$checksums" 2>/dev/null || rm -f "$checksums"

# published_sha prints the expected hash for archive $1: from checksums.txt if
# it lists it, else from a per-file <archive>.sha256. Empty output = no published
# hash for this asset.
published_sha() {
  if [ -f "$checksums" ]; then
    want=$(awk -v f="$1" '{ n=$NF; sub(/^\*/, "", n); if (n == f) { print $1; exit } }' "$checksums")
    if [ -n "$want" ]; then
      echo "$want"
      return 0
    fi
  fi
  if curl -fsSL "${dl_base}/${tag}/${1}.sha256" -o "${work}/${1}.sha256" 2>/dev/null; then
    awk 'NR==1 {print $1}' "${work}/${1}.sha256"
  fi
}

# verify_archive <file> <published-name>: aborts on a mismatch, warns and
# continues when no hash was published or no hashing tool exists.
#
# A MISSING hash is a warning, not a fatal error, deliberately: the hash is
# served by the same host as the archive, so it protects against corruption and
# truncated transfers (the failure actually reported) — not against a compromised
# release host, which is what TLS and GitHub's own asset integrity cover.
# Hard-failing would also brick installs of already-published releases that
# predate the sidecar .sha256 files, plus every local test/dev mirror. A hash
# that IS published and does NOT match is always fatal.
verify_archive() {
  _f=$1
  _n=$2
  _want=$(published_sha "$_n")
  if [ -z "$_want" ]; then
    echo "keld installer: warning — no published SHA-256 for ${_n}; skipping integrity check." >&2
    return 0
  fi
  if ! _got=$(sha256_file "$_f"); then
    echo "keld installer: warning — no sha256sum/shasum on this system; skipping integrity check." >&2
    return 0
  fi
  if [ "$_want" != "$_got" ]; then
    echo "" >&2
    echo "keld installer: CHECKSUM MISMATCH for ${_n} — refusing to install." >&2
    echo "  expected  ${_want}" >&2
    echo "  actual    ${_got}" >&2
    echo "  The download is corrupt or was truncated. Nothing has been installed;" >&2
    echo "  re-run the installer to try again." >&2
    exit 1
  fi
}

echo "Downloading…"

if ! curl -fsSL "$url" -o "${work}/${archive}"; then
  echo "" >&2
  echo "keld installer: download failed." >&2
  echo "  URL: ${url}" >&2
  echo "  Make sure the release exists and your network can reach github.com." >&2
  exit 1
fi
verify_archive "${work}/${archive}" "$archive"

mkdir -p "${work}/cli"
if ! tar -xzf "${work}/${archive}" -C "${work}/cli"; then
  echo "" >&2
  echo "keld installer: could not unpack ${archive}." >&2
  exit 1
fi
if [ ! -f "${work}/cli/keld" ]; then
  echo "keld installer: ${archive} did not contain a keld binary." >&2
  exit 1
fi

chmod +x "${work}/cli/keld"
if [ -f "${work}/cli/keld-agent" ]; then
  chmod +x "${work}/cli/keld-agent"
fi
# Verified — publish into $DEST. mv over a running binary is a rename on
# Linux/macOS, so an in-flight daemon keeps its own inode until it restarts.
mv -f "${work}/cli/keld" "${DEST}/keld"
if [ -f "${work}/cli/keld-agent" ]; then
  mv -f "${work}/cli/keld-agent" "${DEST}/keld-agent"
fi

echo "  ✓ $(printf '%-26s' 'keld + keld-agent') → ${DEST}"

# Fetch the frozen analysis sidecar — REQUIRED on Linux AND macOS.
#
# This is not "the ML sidecar" any more and the distinction now matters. It is the
# client-side ANALYSIS service: /analyze, /ingest, /blocks and /pii, which is what
# turns transcripts into workstreams, dynamics and v2 blocks. GLiNER2 is one
# capability it loads lazily, and a v2 install (ml_backend:"deterministic") never
# asks for it — so no multi-gigabyte model is ever downloaded.
#
# The abort below is therefore MORE justified than when it was written for ML, not
# less: without this binary a Keld install collects credential detection and nothing
# else. The macOS .pkg does not bundle it either (Apple notarization scans all ~15k
# of its files) — onboard.command fetches it exactly as this does. Published
# per-OS/arch as keld-agent-sidecar_<os>_<arch>.tar.gz (macOS: darwin/arm64, Apple
# Silicon only).
if { [ "$os" = "linux" ] || [ "$os" = "darwin" ]; } && [ -f "${DEST}/keld-agent" ]; then
  sc_archive="keld-agent-sidecar_${os}_${arch}.tar.gz"
  sc_url="${dl_base}/${tag}/${sc_archive}"
  sc_fail() {
    echo "keld: analysis sidecar install failed — without it Keld can derive nothing from" >&2
    echo "  your transcripts (no workstreams, no blocks, no PII scan). Aborting." >&2
    echo "  URL: ${sc_url}" >&2
    exit 1
  }
  curl -fsSL "$sc_url" -o "${work}/${sc_archive}" || sc_fail
  verify_archive "${work}/${sc_archive}" "$sc_archive"
  mkdir -p "${work}/sc"
  tar -xzf "${work}/${sc_archive}" -C "${work}/sc" || sc_fail
  [ -d "${work}/sc/keld-agent-sidecar" ] || sc_fail
  chmod +x "${work}/sc/keld-agent-sidecar/keld-agent-sidecar" 2>/dev/null || true
  # Swap only now that the new tree is complete and verified. The previous
  # sidecar (or a dev venv-wrapper FILE of the same name) is displaced, not
  # deleted up front: a failed download above must leave the working install
  # alone rather than trading it for nothing.
  rm -rf "${DEST}/keld-agent-sidecar.prev"
  if [ -e "${DEST}/keld-agent-sidecar" ]; then
    mv "${DEST}/keld-agent-sidecar" "${DEST}/keld-agent-sidecar.prev" || sc_fail
  fi
  mv "${work}/sc/keld-agent-sidecar" "${DEST}/keld-agent-sidecar" || {
    # Restore the displaced install before giving up.
    [ -e "${DEST}/keld-agent-sidecar.prev" ] && mv "${DEST}/keld-agent-sidecar.prev" "${DEST}/keld-agent-sidecar"
    sc_fail
  }
  rm -rf "${DEST}/keld-agent-sidecar.prev"
  echo "  ✓ $(printf '%-26s' 'analysis sidecar') → ${DEST}/keld-agent-sidecar"
fi

agent_ok=1
# Pair against the origin this installer was fetched from. KELD_API_URL is set by the
# server that serves this script (see the Keld-served header) — pass it as --api-url so
# login/setup target THIS host explicitly. Without the flag, keld reuses the API URL of any
# previously stored token (device.go), so re-installing from a new origin over an existing
# install would silently keep signing in against the old host.
api_flag=""
[ -n "${KELD_API_URL:-}" ] && api_flag="--api-url ${KELD_API_URL}"

# In the documented `curl … | sh` form, stdin is the curl PIPE, not the terminal
# — so any prompt keld runs would read EOF and silently take its default.
# keld-agent's own TTY check keys on stdout (which IS the terminal in that form,
# see agentcli.stdoutIsTTY), so it correctly chooses the interactive branch;
# re-opening the controlling terminal here is what makes that branch actually
# usable. Where there is no controlling terminal at all (GUI installer, CI,
# `| sh > log`), stdin is left alone and the summary below does not claim
# onboarding happened.
has_tty=0
if (exec 3</dev/tty) 2>/dev/null; then has_tty=1; fi

# agent_install runs `keld-agent install` with the arguments given, wiring the
# controlling terminal to its stdin when one exists.
agent_install() {
  if [ "$has_tty" = 1 ]; then
    "${DEST}/keld-agent" install "$@" < /dev/tty
  else
    "${DEST}/keld-agent" install "$@"
  fi
}

if [ -f "${DEST}/keld-agent" ]; then
  # keld-agent install owns login → signal setup → service (agent last), and the
  # TTY/headless decision. With a setup code it onboards non-interactively.
  if [ -n "$CODE" ]; then
    # shellcheck disable=SC2086  # api_flag must word-split into two args (a URL has no spaces)
    agent_install $api_flag --code "$CODE" \
      || { agent_ok=0; echo "keld: onboarding/agent install did not fully complete — re-run: keld-agent install --code <CODE>" >&2; }
  else
    # shellcheck disable=SC2086
    agent_install $api_flag \
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
  echo "  or remove it, or put \"${DEST}\" first on PATH. Then run: keld signal doctor" >&2
fi

if [ "$os" = "darwin" ]; then
  echo ""
  echo "Note: macOS users may see a Gatekeeper warning on first run."
  echo "  To allow the binary: System Settings > Privacy & Security > Allow."
fi

# ── Summary: report what actually happened, never what was hoped for ──────────
# `keld-agent install` exits 0 after merely REGISTERING the service when it has
# no terminal and no setup code (the normal headless path — a GUI installer, CI,
# or `curl … | sh` with stdout redirected). Treating that exit code as "set up
# and running" is how the installer came to print success on a machine where
# onboarding had never run. So the summary is decided by observed state: setup
# is done when it has written an ingest token to hook.json — exactly the file
# the daemon itself requires (internal/hook.LoadConfig).
keld_home="${KELD_HOME:-${HOME}/.keld}"
onboarded=0
if [ -f "${keld_home}/hook.json" ] \
  && grep -q '"ingest_token"[[:space:]]*:[[:space:]]*"[^"]' "${keld_home}/hook.json" 2>/dev/null; then
  onboarded=1
fi

echo ""
if [ ! -f "${DEST}/keld-agent" ]; then
  echo "Done — keld installed. Run: keld login && keld signal setup"
elif [ "$agent_ok" != "1" ]; then
  echo "Keld installed, but onboarding did not complete — see the errors above." >&2
  exit 1
elif [ "$onboarded" = "1" ]; then
  echo "Done — Keld is set up and running."
  echo "  Enrichment runs on-device with no model download — nothing multi-gigabyte"
  echo "  is fetched, now or later."
elif [ -n "$CODE" ]; then
  # A setup code was supplied, so onboarding was meant to complete without a
  # human. No token means it genuinely failed — that IS an error.
  echo "Keld installed, but the setup code did not produce a session:" >&2
  echo "  no ingest token in ${keld_home}/hook.json." >&2
  echo "  Re-run with a fresh code:  keld-agent install --code <CODE>" >&2
  exit 1
else
  # No terminal and no code: the service is registered but Keld is NOT set up.
  # This is an expected, recoverable state — not a failure — so exit 0, but say
  # so plainly. The daemon idles and picks the config up on its own (it polls for
  # hook.json), so no restart is needed after finishing setup.
  echo "Installed — but Keld is NOT set up yet (nothing is being collected)."
  echo "Finish setup in a terminal:"
  echo "  keld login && keld signal setup"
  echo "The agent is already running and will start collecting the moment you do."
fi
