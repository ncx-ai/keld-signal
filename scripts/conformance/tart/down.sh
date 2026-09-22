#!/usr/bin/env bash
# Stop and DELETE the conformance VM. ⚠️ UNVERIFIED — see up.sh's header.
#
#   scripts/conformance/tart/down.sh [--name keld-conformance] [--keep]
#
# Deleting is the default because the point of the VM is that it is clean: a
# reused VM has a Signal install, a tool install and a LaunchAgent from the last
# run, which is exactly the state chain A claims not to start from.
set -euo pipefail

NAME=${KELD_TART_VM:-keld-conformance}
KEEP=0
while [ $# -gt 0 ]; do
  case "$1" in
    --name) NAME=$2; shift 2 ;;
    --keep) KEEP=1; shift ;;
    -h|--help) sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "down.sh: unknown flag $1" >&2; exit 2 ;;
  esac
done

command -v tart >/dev/null || { echo "down.sh: tart is not installed"; exit 0; }

tart stop "$NAME" >/dev/null 2>&1 || true
pidfile="${TMPDIR:-/tmp}/keld-tart-$NAME.pid"
if [ -f "$pidfile" ]; then
  kill "$(cat "$pidfile")" 2>/dev/null || true
  rm -f "$pidfile"
fi

if [ "$KEEP" = "1" ]; then
  echo "down.sh: $NAME stopped and KEPT (it is no longer clean; delete it before trusting a chain A run)"
  exit 0
fi
tart delete "$NAME" 2>/dev/null || true
echo "down.sh: $NAME deleted"
