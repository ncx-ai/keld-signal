#!/usr/bin/env bash
# Run one conformance chain inside a clean macOS VM. ⚠️ UNVERIFIED — see up.sh.
#
#   scripts/conformance/tart/run.sh --tool claude_code [--chain A] [--seed N] [--keep]
#
# up.sh -> copy the repo in -> run-chain.sh over `tart ssh` -> pull the artifacts
# back -> down.sh. The VM is deleted even on failure unless --keep is given; the
# run's own output (daemon log, mock logs, verdicts) is copied out FIRST, so
# deleting the VM never deletes the evidence.
#
# What this leg proves that the host leg cannot: the packaged installer path and
# real service registration (AC-12). The host harness runs the daemon in the
# foreground on purpose — KELD_HOME does not isolate the LaunchAgent path, so a
# host run that called `keld-agent install` would rewrite the developer's own
# service. In a throwaway VM that is exactly what we want to happen.
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)

NAME=${KELD_TART_VM:-keld-conformance}
TOOL=""
CHAIN="A"
SEED=""
KEEP=0
INSTALLER=${KELD_CONFORM_PKG:-}   # a .pkg to test unattended; empty = source build

while [ $# -gt 0 ]; do
  case "$1" in
    --tool) TOOL=$2; shift 2 ;;
    --chain) CHAIN=$2; shift 2 ;;
    --seed) SEED=$2; shift 2 ;;
    --name) NAME=$2; shift 2 ;;
    --pkg) INSTALLER=$2; shift 2 ;;
    --keep) KEEP=1; shift ;;
    -h|--help) sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "run.sh: unknown flag $1" >&2; exit 2 ;;
  esac
done
[ -n "$TOOL" ] || { echo "run.sh: --tool is required" >&2; exit 2; }

export TART_SSH_USERNAME=${TART_SSH_USERNAME:-admin}
export TART_SSH_PASSWORD=${TART_SSH_PASSWORD:-admin}
OUT=${KELD_CONFORM_OUT:-$ROOT/.conformance/vm-out}

cleanup() {
  local rc=$?
  # Evidence first, VM second.
  mkdir -p "$OUT"
  tart ssh "$NAME" -- "tar -czf /tmp/conformance-out.tgz -C /tmp keld-conformance 2>/dev/null || true" || true
  if tart ssh "$NAME" -- "test -s /tmp/conformance-out.tgz"; then
    tart ssh "$NAME" -- "cat /tmp/conformance-out.tgz" > "$OUT/conformance-out.tgz" || true
    echo "run.sh: artifacts at $OUT/conformance-out.tgz"
  fi
  if [ "$KEEP" = "1" ]; then
    echo "run.sh: --keep given; VM $NAME left running"
  else
    "$HERE/down.sh" --name "$NAME" || true
  fi
  exit $rc
}

"$HERE/up.sh" --name "$NAME"
trap cleanup EXIT

echo "run.sh: copying the repo into $NAME"
# `tart ssh` pipes stdin, so a tar stream is the copy mechanism — no scp key
# management and no shared folder to mount.
git -C "$ROOT" ls-files -z | tar -czf - --null -T - -C "$ROOT" \
  | tart ssh "$NAME" -- "rm -rf ~/keld-signal && mkdir -p ~/keld-signal && tar -xzf - -C ~/keld-signal"

echo "run.sh: installing the toolchain in the VM"
tart ssh "$NAME" -- bash -lc '
  set -e
  command -v brew >/dev/null || /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)" </dev/null
  eval "$(/opt/homebrew/bin/brew shellenv)"
  brew install -q go node@22 python@3.12 sqlite || true
'

if [ -n "$INSTALLER" ]; then
  echo "run.sh: staging $INSTALLER and installing it unattended (AC-12)"
  base=$(basename "$INSTALLER")
  tart ssh "$NAME" -- "cat > ~/$base" < "$INSTALLER"
  # The unattended invocation AC-12 requires. `-target /` is the whole of it;
  # if this ever needs a click, the installer has failed the criterion.
  tart ssh "$NAME" -- "echo admin | sudo -S installer -pkg ~/$base -target /"
fi

echo "run.sh: chain $CHAIN, tool $TOOL"
tart ssh "$NAME" -- bash -lc "
  set -e
  eval \"\$(/opt/homebrew/bin/brew shellenv)\"
  export PATH=\"\$(brew --prefix node@22)/bin:\$(brew --prefix python@3.12)/bin:\$PATH\"
  export KELD_CONFORM_INSTALL=1
  cd ~/keld-signal
  bash scripts/conformance/run-chain.sh --tool '$TOOL' --chain '$CHAIN' ${SEED:+--seed '$SEED'} --work /tmp/keld-conformance
"
