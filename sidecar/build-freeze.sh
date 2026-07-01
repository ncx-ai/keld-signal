#!/usr/bin/env bash
# Freeze the sidecar into dist/keld-agent-sidecar/. Run per-OS in CI (needs the
# target OS's Python 3.12). NOT runnable for other OSes from a single machine.
set -euo pipefail
cd "$(dirname "$0")/.."
python -m pip install --upgrade pip pyinstaller
python -m pip install -r sidecar/requirements.txt
pyinstaller --clean --noconfirm sidecar/keld-agent-sidecar.spec
echo "frozen -> dist/keld-agent-sidecar/"
