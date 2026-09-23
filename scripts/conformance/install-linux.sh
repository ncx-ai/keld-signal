#!/usr/bin/env bash
# Install Signal on Linux from the REAL artifacts, unattended, and prove it (AC-12).
#
#   scripts/conformance/install-linux.sh --artifacts artifacts/ --code CONFORM
#   scripts/conformance/install-linux.sh --tag v2.5.0 --code CONFORM --api-url http://127.0.0.1:8123
#
# Three phases:
#
#   1. INSTALL, unattended:  scripts/install.sh — the same file a user pipes
#      into sh, run VERBATIM, including its checksum check and its refusal to
#      finish without the analysis sidecar.
#   2. ONBOARD, by command:  keld login --code · keld signal setup --yes · keld-agent install
#      (--installer-code instead hands the code to install.sh, which is what the
#      documented `curl … | sh -s -- --code X` one-liner does; the same three
#      commands run, inside `keld-agent install`.)
#   3. VERIFY, from OBSERVED STATE: the binaries run, the sidecar tree is on
#      disk, the systemd user unit exists and is enabled, and hook.json holds an
#      ingest token. Never from an exit code — install.sh can exit 0 having
#      placed files and onboarded nobody.
#
# ⚠️ **install.sh DOWNLOADS; it does not take a local path.** So a local
# artifacts dir is SERVED on loopback and `KELD_DOWNLOAD_BASE`/`KELD_RELEASE_TAG`
# point the installer at it. That is deliberately not a new code path in the
# installer: the thing under test has to be the file users run, or this proves
# something else.
#
# ⚠️ **A CONTAINER HAS NO SYSTEMD USER BUS.** `--allow-no-service-manager`
# downgrades the "is it loaded" half of the service check to a stated line; the
# unit FILE is still required. Without the flag a missing bus fails, so the
# check can never quietly evaporate.
#
# ⚠️ This rewrites the machine it runs on — see iv_require_disposable.
set -uo pipefail

IV_NAME="install-linux"
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
# shellcheck source=lib/install-verify.sh
. "$HERE/lib/install-verify.sh"

ARTIFACTS=""
TAG=${KELD_RELEASE_TAG:-}
CODE=${KELD_SETUP_CODE:-}
API_URL=${KELD_API_URL:-}
DEST=${KELD_INSTALL_DIR:-$HOME/.local/bin}
STOP_SERVICE=0
SKIP_ONBOARD=0
INSTALLER_CODE=0
ALLOW_NO_MANAGER=""
SERVER_PID=""
INSTALL_RC=0

usage() { sed -n '2,32p' "$0" | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
  case "$1" in
    --artifacts)   ARTIFACTS=$2; shift 2 ;;
    --tag)         TAG=$2; shift 2 ;;
    --code)        CODE=$2; shift 2 ;;
    --api-url)     API_URL=$2; shift 2 ;;
    --dest)        DEST=$2; shift 2 ;;
    --installer-code) INSTALLER_CODE=1; shift ;;
    --allow-no-service-manager) ALLOW_NO_MANAGER="--allow-no-manager"; shift ;;
    --stop-service) STOP_SERVICE=1; shift ;;
    --skip-onboarding) SKIP_ONBOARD=1; shift ;;
    -h|--help)     usage; exit 0 ;;
    *) echo "$IV_NAME: unknown flag $1" >&2; exit 2 ;;
  esac
done

iv_require_disposable

cleanup() { [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null; }
trap cleanup EXIT

command -v curl >/dev/null 2>&1 || { echo "$IV_NAME: curl is required (install.sh uses it)" >&2; exit 2; }

case "$(uname -m)" in
  x86_64|amd64)  ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "$IV_NAME: unsupported architecture $(uname -m)" >&2; exit 2 ;;
esac
OS=$(uname -s | tr '[:upper:]' '[:lower:]')

# --- 1. install, unattended ---------------------------------------------------

DL_BASE=${KELD_DOWNLOAD_BASE:-}

if [ -n "$ARTIFACTS" ]; then
  iv_step "serve-artifacts"
  [ -d "$ARTIFACTS" ] || iv_die "no such directory: $ARTIFACTS"
  ARTIFACTS=$(cd "$ARTIFACTS" && pwd)
  [ -n "$TAG" ] || TAG=local

  CLI_ARCHIVE="keld_${OS}_${ARCH}.tar.gz"
  SC_ARCHIVE="keld-agent-sidecar_${OS}_${ARCH}.tar.gz"
  [ -f "$ARTIFACTS/$CLI_ARCHIVE" ] || iv_die "no $CLI_ARCHIVE in $ARTIFACTS — that is the artifact install.sh downloads"
  [ -f "$ARTIFACTS/$SC_ARCHIVE" ] \
    || iv_die "no $SC_ARCHIVE in $ARTIFACTS — install.sh ABORTS without the analysis sidecar, and rightly: without it Keld derives nothing from a transcript"

  SERVE_ROOT=$(mktemp -d)
  mkdir -p "$SERVE_ROOT/$TAG"
  for f in "$ARTIFACTS"/*; do ln -s "$f" "$SERVE_ROOT/$TAG/$(basename "$f")"; done
  command -v python3 >/dev/null 2>&1 || iv_die "python3 is needed to serve $ARTIFACTS on loopback"
  # ⚠️ `-u` IS LOAD-BEARING. Python block-buffers stdout when it is not a tty,
  # so without it the "Serving HTTP on … port N" banner never reaches the log
  # and the port parse below waits out its whole budget and dies — measured
  # while writing this (empty server.log after 2 s, server alive and serving).
  python3 -u -m http.server --bind 127.0.0.1 --directory "$SERVE_ROOT" 0 >"$SERVE_ROOT/server.log" 2>&1 &
  SERVER_PID=$!
  PORT=""
  for _ in $(seq 1 50); do
    PORT=$(sed -n 's/.*port \([0-9][0-9]*\).*/\1/p' "$SERVE_ROOT/server.log" | head -1)
    [ -n "$PORT" ] && break
    sleep 0.1
  done
  [ -n "$PORT" ] || iv_die "the local artifact server never reported a port: $(cat "$SERVE_ROOT/server.log")"
  DL_BASE="http://127.0.0.1:$PORT"
  iv_ok "serving $ARTIFACTS as $DL_BASE/$TAG ($(ls "$ARTIFACTS" | wc -l | tr -d ' ') files)"
  # A missing checksums.txt is install.sh's documented warn-and-continue; say so
  # rather than letting a silently unverified install read as a verified one.
  if [ -f "$ARTIFACTS/checksums.txt" ] || [ -f "$ARTIFACTS/$CLI_ARCHIVE.sha256" ]; then
    iv_ok "a published checksum is present; install.sh will verify against it"
  else
    iv_say "  ⚠ no checksums.txt and no .sha256: install.sh WARNS and installs unverified (its documented behaviour)"
  fi
else
  [ -n "$TAG" ] || iv_die "give --artifacts <dir> (a local release) or --tag <tag> (a published one)"
fi

iv_step "install-sh"
iv_say "\$ KELD_RELEASE_TAG=$TAG KELD_DOWNLOAD_BASE=${DL_BASE:-<dl.keld.co>} sh scripts/install.sh"
# shellcheck disable=SC2086  # INSTALL_ARGS must word-split into two args
INSTALL_ARGS=""
if [ "$INSTALLER_CODE" = "1" ] && [ -n "$CODE" ]; then
  INSTALL_ARGS="--code $CODE"
  iv_say "  (onboarding rides install.sh --code, the documented curl|sh one-liner)"
fi
# stdin is /dev/null on purpose: the installer's own TTY probe must take the
# headless branch, which is what an MDM push and this harness both are. Handing
# it a terminal would test the interactive path and hang when nobody answers.
env KELD_RELEASE_TAG="$TAG" \
    ${DL_BASE:+KELD_DOWNLOAD_BASE="$DL_BASE"} \
    ${API_URL:+KELD_API_URL="$API_URL"} \
    KELD_INSTALL_DIR="$DEST" \
    sh "$ROOT/scripts/install.sh" $INSTALL_ARGS </dev/null 2>&1 | sed "s/^/$IV_NAME:   | /"
rc=${PIPESTATUS[0]}
if [ "$rc" != "0" ]; then
  # ⚠️ **A NON-ZERO EXIT IS DEFERRED HERE, NOT FORGIVEN — and only behind the
  # flag.** This script's own rule is that nothing is judged by an exit code,
  # because `install.sh` can exit 0 having placed files and onboarded nobody.
  # The converse is just as true and is what a container hits every time: it
  # exits 1 having placed EVERYTHING correctly, because `keld-agent install`
  # registers the unit and then tries to START it, and there is no systemd user
  # bus to start it with.
  #
  # Measured 2026-09-16, real v2.5.0 release, ubuntu:24.04 under docker, both
  # ways round: with no systemd installed at all the line is
  # `exec: "systemctl": executable file not found in $PATH`; with systemd
  # installed but no user bus it is a bare `exit status 1`. Either way the
  # binaries, the 1.2 GB sidecar tree and
  # ~/.config/systemd/user/keld-agent.service were all correctly in place.
  #
  # So `--allow-no-service-manager` could not do the job its own header claims
  # — it relaxed `verify-service`, which this die never let the run reach.
  #
  # Deferred means: the failure is STATED, the run continues, and every
  # verification below must still pass on observed state. Nothing is skipped
  # and no check is weakened; if the install really was broken, the binaries
  # will not run or the sidecar will be missing and the run dies there instead,
  # with both facts printed. Without the flag, a non-zero exit is still fatal.
  if [ -n "$ALLOW_NO_MANAGER" ]; then
    iv_say "  ⚠ scripts/install.sh exited $rc. DEFERRED, not forgiven: --allow-no-service-manager"
    iv_say "    says this machine has no service manager to start the agent with, and the"
    iv_say "    verifications below decide — on observed state, as they always do."
    INSTALL_RC=$rc
  else
    iv_die "scripts/install.sh exited $rc"
  fi
fi

# --- 2. onboard, by command ---------------------------------------------------

if [ "$SKIP_ONBOARD" = "1" ]; then
  iv_say "onboarding skipped (--skip-onboarding): only the install and the service are verified"
elif [ "$INSTALLER_CODE" = "1" ]; then
  iv_say "onboarding already ran inside install.sh --code (the same three commands, from keld-agent install)"
else
  [ -n "$CODE" ] || iv_die "no setup code (--code or \$KELD_SETUP_CODE); onboarding cannot run unattended without one"
  API_ARGS=()
  [ -n "$API_URL" ] && API_ARGS=(--api-url "$API_URL")

  iv_run "keld-login" "$DEST/keld" login --code "$CODE" ${API_ARGS[@]+"${API_ARGS[@]}"} \
    || iv_die "keld login --code was rejected (is Atlas reachable at ${API_URL:-the default}?)"
  iv_run "keld-signal-setup" "$DEST/keld" signal setup --yes ${API_ARGS[@]+"${API_ARGS[@]}"} \
    || iv_die "keld signal setup --yes failed"
  # Idempotent, and it re-points the service at the installed binary and
  # restarts it — ml_backend is read at daemon STARTUP and never re-read.
  # Same deferral, same reason: this is the command that registers the unit and
  # then starts it, so on a machine with no service manager it reports failure
  # having done the half that can be verified.
  if ! iv_run "keld-agent-install" "$DEST/keld-agent" install; then
    [ -n "$ALLOW_NO_MANAGER" ] || iv_die "keld-agent install failed"
    iv_say "  ⚠ keld-agent install exited non-zero; deferred for the same reason as install.sh"
  fi
fi

# --- 3. verify, from observed state -------------------------------------------

iv_step "verify-binaries"
iv_verify_binaries "$DEST"

iv_step "verify-sidecar"
# HARD on Linux, unlike macOS: install.sh aborts rather than finish without the
# analysis sidecar, so a missing tree here means the installer reported success
# for an install that cannot derive anything from a transcript.
SIDECAR_DIR="$DEST/keld-agent-sidecar"
[ -x "$SIDECAR_DIR/keld-agent-sidecar" ] \
  || iv_die "no $SIDECAR_DIR/keld-agent-sidecar — install.sh finished without the analysis service it refuses to finish without"
iv_ok "sidecar $(cat "$SIDECAR_DIR/VERSION" 2>/dev/null || echo '(no VERSION — predates the stamp)') at $SIDECAR_DIR"

iv_step "verify-service"
iv_verify_service_linux $ALLOW_NO_MANAGER

if [ "$SKIP_ONBOARD" != "1" ]; then
  iv_step "verify-onboarded"
  iv_verify_onboarded
fi

if [ "$STOP_SERVICE" = "1" ]; then
  iv_step "stop-service"
  if command -v systemctl >/dev/null 2>&1 && systemctl --user stop keld-agent.service >/dev/null 2>&1; then
    iv_ok "daemon stopped; the unit stays registered at $HOME/.config/systemd/user/keld-agent.service"
  else
    iv_say "  nothing to stop (no user bus, or the unit was not running)"
  fi
fi

echo
if [ "$INSTALL_RC" != "0" ]; then
  iv_say "OK — verified from observed state, but scripts/install.sh EXITED $INSTALL_RC"
  iv_say "     (no service manager on this machine; the unit file is present and was checked)"
else
  iv_say "OK — installed by scripts/install.sh, onboarded by command, verified from observed state"
fi
iv_say "bin_dir=$DEST"
iv_say "sidecar_dir=$SIDECAR_DIR"
