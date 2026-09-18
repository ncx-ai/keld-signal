#!/usr/bin/env bash
# Clone and boot a CLEAN macOS VM for a conformance run (Apple Silicon only).
#
#   scripts/conformance/tart/up.sh [--name keld-conformance] [--image ghcr.io/...]
#
# ⚠️ **UNVERIFIED.** Nothing in this directory has been run: it needs `tart`
# installed and a ~40 GB image pull, neither of which was done on the machine
# these scripts were written on. Treat the first run as the test, and fix this
# header when it passes. See docs/conformance.md ("the macOS VM leg").
#
# Why a VM at all, when run-chain.sh already runs on a Mac: the host harness
# deliberately never calls `keld-agent install`, because KELD_HOME does not
# isolate the LaunchAgent path and a run would rewrite the developer's real
# service (lib/isolate.sh's header says so). A throwaway VM is the only place
# the packaged .pkg, its postinstall and real service registration — AC-12 —
# can be exercised on macOS without a CI runner.
set -euo pipefail

NAME=${KELD_TART_VM:-keld-conformance}
IMAGE=${KELD_TART_IMAGE:-ghcr.io/cirruslabs/macos-sequoia-base:latest}
CPUS=${KELD_TART_CPUS:-4}
MEMORY=${KELD_TART_MEMORY:-8192}
DISK=${KELD_TART_DISK:-}

while [ $# -gt 0 ]; do
  case "$1" in
    --name)  NAME=$2; shift 2 ;;
    --image) IMAGE=$2; shift 2 ;;
    --cpus)  CPUS=$2; shift 2 ;;
    --memory) MEMORY=$2; shift 2 ;;
    -h|--help) sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "up.sh: unknown flag $1" >&2; exit 2 ;;
  esac
done

command -v tart >/dev/null || {
  cat >&2 <<'MSG'
up.sh: `tart` is not installed.

  brew install cirruslabs/cli/tart

⚠️ Before that first install, read the licence note in docs/conformance.md:
Tart is free for open-source and for individual use, and requires a paid
sponsorship for commercial use above a small team. That check is a person's
decision, not this script's, which is why nothing here installs it for you.
MSG
  exit 127
}

[ "$(uname -s)" = "Darwin" ] && [ "$(uname -m)" = "arm64" ] || {
  echo "up.sh: macOS VMs run on Apple Silicon only (Virtualization.framework)" >&2
  exit 2
}

if tart list --format json 2>/dev/null | grep -q "\"$NAME\""; then
  echo "up.sh: VM $NAME already exists — reusing it (down.sh deletes it)"
else
  # ~40 GB on first pull, cached in ~/.tart/cache afterwards.
  echo "up.sh: cloning $IMAGE -> $NAME (first pull is ~40 GB)"
  tart clone "$IMAGE" "$NAME"
  tart set "$NAME" --cpu "$CPUS" --memory "$MEMORY"
  [ -n "$DISK" ] && tart set "$NAME" --disk-size "$DISK"
fi

echo "up.sh: starting $NAME (headless)"
tart run "$NAME" --no-graphics &
echo $! > "${TMPDIR:-/tmp}/keld-tart-$NAME.pid"

# The image's credentials are the cirruslabs defaults; they are the image's,
# not ours, and they only ever reach a throwaway local VM.
export TART_SSH_USERNAME=${TART_SSH_USERNAME:-admin}
export TART_SSH_PASSWORD=${TART_SSH_PASSWORD:-admin}

echo "up.sh: waiting for ssh"
for _ in $(seq 1 60); do
  if tart ssh "$NAME" -- true >/dev/null 2>&1; then
    echo "up.sh: $NAME is up ($(tart ip "$NAME" 2>/dev/null))"
    exit 0
  fi
  sleep 5
done
echo "up.sh: $NAME never answered ssh" >&2
exit 1
