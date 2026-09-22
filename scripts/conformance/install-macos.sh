#!/usr/bin/env bash
# Install Signal on macOS from the REAL artifact, unattended, and prove it (AC-12).
#
#   scripts/conformance/install-macos.sh --pkg keld-3.0.0-arm64.pkg --code CONFORM
#   scripts/conformance/install-macos.sh --artifacts artifacts/ --api-url http://127.0.0.1:8123
#
# Three phases, and the third is the one that matters:
#
#   1. INSTALL, unattended:  sudo installer -pkg <pkg> -target /
#   2. ONBOARD, by command:  keld login --code · keld signal setup --yes · keld-agent install
#   3. VERIFY, from OBSERVED STATE: the binaries run, launchd knows the job, and
#      hook.json holds an ingest token. Never from an exit code.
#
# ⚠️ **THE SILENT PATH MUST NOT DEPEND ON THE WIZARD PANE.** macOS onboarding
# now happens inside an Installer.app plugin pane ordered BEFORE the install
# step (installers/macos/plugin/, AGENTS.md → "macOS onboarding UI"): it redeems
# the setup code, fetches the sidecar, and collects which tools to configure.
# `installer -pkg` runs no UI at all, so the pane never runs, no handoff file is
# written, `postinstall` sees `paired=false` and configures no tools — and its
# `onboard.command` fallback is a Terminal script that cannot onboard anybody
# unattended either. That is not a defect: it is exactly why AC-12 requires a
# COMMAND equivalent for every onboarding step, and phase 2 is that equivalent.
# An MDM push is in the same position and runs the same three commands.
#
# ⚠️ This rewrites the machine it runs on — see iv_require_disposable.
set -uo pipefail

IV_NAME="install-macos"
HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=lib/install-verify.sh
. "$HERE/lib/install-verify.sh"

PKG=${KELD_CONFORM_PKG:-}
ARTIFACTS=""
CODE=${KELD_SETUP_CODE:-}
API_URL=${KELD_API_URL:-}
STOP_SERVICE=0
SKIP_ONBOARD=0

usage() { sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
  case "$1" in
    --pkg)          PKG=$2; shift 2 ;;
    --artifacts)    ARTIFACTS=$2; shift 2 ;;
    --code)         CODE=$2; shift 2 ;;
    --api-url)      API_URL=$2; shift 2 ;;
    --stop-service) STOP_SERVICE=1; shift ;;
    --skip-onboarding) SKIP_ONBOARD=1; shift ;;
    -h|--help)      usage; exit 0 ;;
    *) echo "$IV_NAME: unknown flag $1" >&2; exit 2 ;;
  esac
done

iv_require_disposable

[ "$(uname -s)" = "Darwin" ] || { echo "$IV_NAME: macOS only (this is $(uname -s))" >&2; exit 2; }

# --- 1. install ---------------------------------------------------------------

iv_step "resolve-pkg"
if [ -z "$PKG" ] && [ -n "$ARTIFACTS" ]; then
  # Newest first: an artifacts dir downloaded from two runs holds two pkgs, and
  # silently testing the older one is worse than refusing.
  PKG=$(ls -t "$ARTIFACTS"/*.pkg 2>/dev/null | head -1)
fi
[ -n "$PKG" ] || iv_die "no .pkg given (--pkg, --artifacts or \$KELD_CONFORM_PKG)"
[ -f "$PKG" ] || iv_die "no such file: $PKG"
PKG=$(cd "$(dirname "$PKG")" && pwd)/$(basename "$PKG")
iv_ok "$PKG ($(( $(stat -f %z "$PKG") / 1024 / 1024 )) MB)"

# What the payload claims to be, before it is installed. `installer` itself is
# happy to install a pkg for another architecture, and the failure would only
# show up later as "keld-agent would not run".
iv_step "inspect-pkg"
if out=$(installer -pkginfo -pkg "$PKG" 2>&1); then
  printf '%s\n' "$out" | sed "s/^/$IV_NAME:   | /"
else
  iv_say "  (installer -pkginfo said nothing usable; continuing)"
fi

# ⚠️ THE unattended invocation. `-target /` is the whole of it: no UI, no
# clicks, no pane. If this ever needs a window, the installer has failed AC-12.
# -verbose so a failing postinstall is readable in CI rather than a bare code.
iv_step "installer-pkg"
iv_say "\$ sudo installer -verbose -pkg $PKG -target /"
if ! sudo installer -verbose -pkg "$PKG" -target / 2>&1 | sed "s/^/$IV_NAME:   | /"; then
  iv_die "installer -pkg exited non-zero (see the lines above)"
fi

PREFIX=/usr/local/keld

# --- 2. onboard, by command ---------------------------------------------------

if [ "$SKIP_ONBOARD" = "1" ]; then
  iv_say "onboarding skipped (--skip-onboarding): only the install and the service are verified"
else
  [ -n "$CODE" ] || iv_die "no setup code (--code or \$KELD_SETUP_CODE); onboarding cannot run unattended without one"

  # ⚠️ `"${ARR[@]}"` on an EMPTY array is an unbound-variable error under
  # `set -u` in bash 3.2, which is the bash macOS ships at /bin/bash. The
  # `${ARR[@]+…}` form is the 3.2-safe idiom and costs nothing.
  API_ARGS=()
  [ -n "$API_URL" ] && API_ARGS=(--api-url "$API_URL")

  # The three commands AC-12 names. Each is a step of its own, so a failure says
  # which one: a rejected code and an unwritable config dir are different bugs.
  iv_run "keld-login" "$PREFIX/keld" login --code "$CODE" ${API_ARGS[@]+"${API_ARGS[@]}"} \
    || iv_die "keld login --code was rejected (is the mock/real Atlas reachable at ${API_URL:-the default}?)"
  iv_run "keld-signal-setup" "$PREFIX/keld" signal setup --yes ${API_ARGS[@]+"${API_ARGS[@]}"} \
    || iv_die "keld signal setup --yes failed"
  # Idempotent: the pkg's own postinstall already ran this. Running it again is
  # what an MDM push does after handing the machine a code, and it re-points the
  # service at the installed binary and restarts it so the new config is read —
  # ml_backend is read at daemon STARTUP and never re-read.
  iv_run "keld-agent-install" "$PREFIX/keld-agent" install \
    || iv_die "keld-agent install failed"
fi

# --- 3. verify, from observed state -------------------------------------------

iv_step "verify-binaries"
iv_verify_binaries "$PREFIX"

iv_step "verify-service"
iv_verify_service_darwin

if [ "$SKIP_ONBOARD" != "1" ]; then
  iv_step "verify-onboarded"
  iv_verify_onboarded
fi

# The analysis sidecar is NOT in the pkg payload (Apple's notary scans every one
# of its ~15,000 files), so postinstall fetches it in the BACKGROUND. It is
# therefore reported, never required: a machine without it yet is late, not
# broken — enrichment spools until it lands. install.sh's Linux path is the
# opposite and aborts, which is why only that one is a hard check.
iv_step "report-sidecar"
SIDECAR_DIR="$HOME/.local/bin/keld-agent-sidecar"
if [ -x "$SIDECAR_DIR/keld-agent-sidecar" ]; then
  iv_ok "sidecar $(cat "$SIDECAR_DIR/VERSION" 2>/dev/null || echo '(no VERSION — predates the stamp)') at $SIDECAR_DIR"
else
  iv_say "  sidecar not on disk yet — postinstall fetches it in the background; enrichment spools until it lands"
fi

if [ "$STOP_SERVICE" = "1" ]; then
  # For a caller that wants the machine INSTALLED but the daemon not running —
  # the conformance chain, which drives its own foreground daemon against an
  # isolated HOME and must not race a launchd one. The job stays REGISTERED, so
  # what was verified above is still true afterwards.
  iv_step "stop-service"
  launchctl bootout "gui/$(id -u)/co.keld.agent" >/dev/null 2>&1 \
    && iv_ok "daemon stopped; the job is still registered at $HOME/Library/LaunchAgents/co.keld.agent.plist" \
    || iv_say "  launchctl bootout said the job was not loaded (already stopped)"
fi

echo
iv_say "OK — installed from $(basename "$PKG"), onboarded by command, verified from observed state"
iv_say "bin_dir=$PREFIX"
iv_say "sidecar_dir=$SIDECAR_DIR"
