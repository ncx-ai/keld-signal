#!/usr/bin/env bash
# Freeze + smoke the Linux sidecar INSIDE the manylinux_2_28 image.
#
# Runs as the container-side half of installers.yml's `linux-sidecar` job. That job
# used to be a `container:` job, which meant GitHub had to acquire a container-type
# hosted runner — and when it couldn't ("The job was not acquired by Runner of type
# hosted even after multiple attempts": runner_name empty, zero steps, cancelled
# after 15m), v0.20.0 published without its Linux sidecar tarball and every Linux
# curl|sh install hard-failed. The job now runs natively and invokes this script via
# `docker run` instead, which keeps the identical glibc 2.28 build baseline — that is
# a property of the image the freeze runs in, not of how the image is invoked.
#
# Expects: CWD is the repo root (mounted at /work), running as root (dnf needs it),
# HOST_UID/HOST_GID set to the host user so outputs can be hand back.
set -euo pipefail

# manylinux's /opt/python CPython is built statically (no libpython.so), which
# PyInstaller cannot freeze against. manylinux_2_28 is AlmaLinux 8, whose python3.12
# RPM IS built --enable-shared and links glibc 2.28 — exactly the shared interpreter
# + old baseline we need. Reference /usr/bin/python3.12 by absolute path throughout:
# the image keeps its static /opt/python/cp312-cp312/bin first on PATH, so bare
# `python3.12` would resolve to the static interpreter we can't freeze against.
PY=/usr/bin/python3.12

export KELD_OBFUSCATE="${KELD_OBFUSCATE:-1}"  # shipped artifacts are obfuscated; dev builds stay plain
export PYTHON="$PY"
# torch's default Linux wheel hard-depends on the CUDA 13 stack (cuda-toolkit,
# nvidia-*), so the frozen bundle needs libcudart.so.13 at runtime and won't load on
# CPU-only machines — which is every device the sidecar runs on (GLiNER2 runs on CPU;
# macOS/Windows already ship CPU torch). Pin the CPU build: pre-install it from the
# CPU index, and expose that index to build-freeze.sh's own requirements install (the
# `+cpu` local version outranks the plain PyPI wheel, so gliner2's torch dep stays CPU).
export PIP_EXTRA_INDEX_URL="${PIP_EXTRA_INDEX_URL:-https://download.pytorch.org/whl/cpu}"
export HF_HOME="${HF_HOME:-/work/hf-cache}"
mkdir -p "$HF_HOME"

echo "::group::install shared CPython 3.12 (glibc 2.28)"
dnf install -y python3.12 python3.12-devel
"$PY" -m ensurepip --upgrade
"$PY" -m pip install --upgrade pip
echo "::endgroup::"

echo "::group::freeze sidecar (obfuscated)"
"$PY" -m pip install --quiet torch --index-url https://download.pytorch.org/whl/cpu
"$PY" -m pip install --quiet python-minifier pyarmor
bash sidecar/build-freeze.sh
echo "::endgroup::"

echo "::group::smoke the frozen sidecar"
BIN="dist/keld-agent-sidecar/keld-agent-sidecar"
"$BIN" --port 8399 --host 127.0.0.1 &
for _ in $(seq 1 180); do
  if curl -sf http://127.0.0.1:8399/health | grep -q '"ok"'; then ok=1; break; fi
  sleep 2
done
[ -n "${ok:-}" ] || { echo "sidecar did not become healthy"; exit 1; }
# Real /classify spawns the inference worker child, which must import the (obfuscated)
# modules from the frozen bundle — the only CI coverage for frozen worker spawn. It
# also proves the glibc-2.28-linked binary actually runs (here, inside the 2.28 image).
#
# The timeout is deliberately generous. This first /classify also pays for the model
# download (~1.8 GB) whenever the HF cache misses, so a tight cap bounds the DOWNLOAD
# rather than the inference it is meant to check. At the previous -m 120 the step was a
# coin flip: the same cold download took 100s on one run (passed, 20s of margin) and
# 116s on the next (failed), which is how v0.21.0 shipped without its Linux sidecar.
resp=$(curl -sf -m 900 -X POST http://127.0.0.1:8399/classify \
  -H 'Content-Type: application/json' \
  -d '{"text":"debug the login bug","tasks":{"task_type":["debug","other"]}}') \
  || { echo "frozen worker /classify failed — spawn/import/bundle broke"; exit 1; }
echo "$resp" | grep -q '"task_type"' \
  || { echo "frozen worker returned no result: $resp"; exit 1; }
echo "frozen worker spawn + inference OK (glibc 2.28 baseline)"
echo "::endgroup::"

# dnf needs root, so this container runs as root. Hand the build outputs back to the
# host UID/GID or the host-side tar and upload-artifact trip over root-owned files.
if [ -n "${HOST_UID:-}" ] && [ -n "${HOST_GID:-}" ]; then
  chown -R "$HOST_UID:$HOST_GID" dist "$HF_HOME"
  echo "handed dist/ + $HF_HOME back to ${HOST_UID}:${HOST_GID}"
fi
