# Build-time obfuscation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans or subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** A build-time `KELD_OBFUSCATE=1` flag (default off, CI-set) that obfuscates the shipped sidecar Python (python-minifier locals-rename → free PyArmor → PyInstaller) and strips the Go binaries (`-s -w`), hard-failing if requested but unavailable — leaving dev builds plain.

**Architecture:** All obfuscation is a freeze-time transform in `sidecar/build-freeze.sh` + `-ldflags` in `.github/workflows/installers.yml`. No runtime deps added; no product code changes.

**Tech Stack:** bash, GitHub Actions, python-minifier (dflook), PyArmor free tier, PyInstaller, Go ldflags.

## Global Constraints

- `KELD_OBFUSCATE` default off; only CI installer/release sets it. Hard-fail (`exit 1`) if set but a stage can't run — never ship plain when obfuscation was requested.
- Locals-only rename (`rename_globals` OFF) — global rename breaks Pydantic wire fields + `multiprocessing`-spawn pickle-by-reference targets (`serve`, `_model_factory`).
- Model-identity concealment is a NON-goal (bundled deps + on-disk weights reveal GLiNER2 regardless).
- No change to `sidecar/requirements.txt` (obf tools are build-only).
- Commit trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- **Verification boundary:** the full obfuscated **Linux** freeze + the worker-spawn acceptance gate ARE locally runnable (via the Task-2 local runner: pyarmor/python-minifier/pyinstaller pip-install into the venv; the model is already at `~/.keld/models/gliner2-large-v1`). Only the **macOS/Windows** freezes require CI (no cross-OS PyInstaller). So Linux is locally verified; other OSes are CI-verified — state which when reporting.

---

## File Structure
- `sidecar/build-freeze.sh` (modify) — flag gate + `--check` mode + obfuscation stage (locals-rename → pyarmor → freeze the obfuscated tree) with hard-fail.
- `sidecar/test_build_freeze.sh` (new) — dev-runnable test of the `--check` gate.
- `scripts/obfuscated-freeze-local.sh` (new) + `make obfuscate-check` (Makefile) — **local CI mirror**: build a throwaway venv, install the build-only obf tools + pyinstaller, run `KELD_OBFUSCATE=1 build-freeze.sh`, then run the frozen sidecar and POST `/classify` (the spawn acceptance gate) — the same sequence CI runs, so the Linux path is debuggable/validatable without pushing.
- `.github/workflows/installers.yml` (modify) — install obf tools + set `KELD_OBFUSCATE=1` on the freeze; `-s -w` on the Go builds under the flag; extend the smoke step to POST `/classify` and assert a 200 result (the spawn acceptance gate). Mirrors the local runner.
- `AGENTS.md` + `CLAUDE.md` gotchas (modify) — document the flag, dev-plain/prod-obfuscated split, locals-only rationale, model-hiding non-goal, license-ready note, and the local runner.

---

## Task 1: `build-freeze.sh` gate + `--check` + obfuscation stage

**Files:** Modify `sidecar/build-freeze.sh`; Create `sidecar/test_build_freeze.sh`.

**Interfaces:**
- Produces: `build-freeze.sh [--check]`. `--check` runs ONLY the gate (validate tools iff `KELD_OBFUSCATE=1`) and exits (0 ok / 1 hard-fail), doing no pip/freeze. Full run additionally freezes (obfuscated tree when the flag is set).

- [ ] **Step 1: Write the failing test**

Create `sidecar/test_build_freeze.sh`:
```bash
#!/usr/bin/env bash
# Dev-runnable test of build-freeze.sh's obfuscation GATE (not the freeze itself).
set -u
here="$(cd "$(dirname "$0")" && pwd)"
fails=0
check() { # desc, expected_exit, env...
  local desc="$1" want="$2"; shift 2
  env "$@" bash "$here/build-freeze.sh" --check >/dev/null 2>&1
  local got=$?
  if [ "$got" = "$want" ]; then echo "PASS $desc"; else echo "FAIL $desc (exit $got, want $want)"; fails=$((fails+1)); fi
}
# Obfuscation OFF -> gate passes regardless of tools.
check "flag off -> ok" 0 KELD_OBFUSCATE=0
# Obfuscation ON but tools absent (empty PATH for pyarmor) -> hard-fail.
check "flag on, no tools -> hard-fail" 1 KELD_OBFUSCATE=1 PATH=/usr/bin:/bin PYARMOR_FORCE_MISSING=1
echo; [ "$fails" = 0 ] && echo "build-freeze gate: all passed" || { echo "$fails failed"; exit 1; }
```

- [ ] **Step 2: Run to verify it fails**

Run: `bash sidecar/test_build_freeze.sh`
Expected: FAIL — `build-freeze.sh` doesn't accept `--check` / gate not implemented (non-1/0 or wrong exits).

- [ ] **Step 3: Implement the gate + obfuscation in `build-freeze.sh`**

Replace `sidecar/build-freeze.sh` with:
```bash
#!/usr/bin/env bash
# Freeze the sidecar into dist/keld-agent-sidecar/. Run per-OS in CI (needs the
# target OS's Python 3.12). With KELD_OBFUSCATE=1, the shipped bytecode is
# locals-renamed (python-minifier) then encrypted (PyArmor free tier) before
# PyInstaller freezes it; unset builds plain. Hard-fails if obfuscation is
# requested but unavailable — never ships plain when asked to obfuscate.
set -euo pipefail
cd "$(dirname "$0")/.."
OBF="${KELD_OBFUSCATE:-0}"
PY="${PYTHON:-python}"

_have_obf_tools() {
  [ -z "${PYARMOR_FORCE_MISSING:-}" ] || return 1   # test hook
  command -v pyarmor >/dev/null 2>&1 && "$PY" -c 'import python_minifier' >/dev/null 2>&1
}

# Gate: if obfuscation is requested, the tools must be present. Fast + side-effect
# free so `--check` can test it without the heavy freeze.
if [ "$OBF" = "1" ] && ! _have_obf_tools; then
  echo "ERROR: KELD_OBFUSCATE=1 but python-minifier/pyarmor unavailable — refusing to ship plain code" >&2
  exit 1
fi
if [ "${1:-}" = "--check" ]; then
  echo "build-freeze gate ok (KELD_OBFUSCATE=$OBF)"; exit 0
fi

"$PY" -m pip install --upgrade pip pyinstaller
"$PY" -m pip install -r sidecar/requirements.txt

if [ "$OBF" = "1" ]; then
  echo "obfuscating sidecar (locals-rename -> pyarmor)…"
  rm -rf build/obf
  # 1) locals-only rename, preserving layout (app/ package + serve.py).
  "$PY" -m python_minifier --no-rename-globals --output build/obf/serve.py sidecar/serve.py
  mkdir -p build/obf/app
  for f in sidecar/app/*.py; do
    "$PY" -m python_minifier --no-rename-globals --output "build/obf/app/$(basename "$f")" "$f"
  done
  # 2) PyArmor free-tier bytecode encryption over the renamed tree, in place.
  pyarmor gen -O build/obf_pyarmor -r build/obf/app build/obf/serve.py
  # 3) overlay the obfuscated app/serve + pyarmor_runtime onto the sidecar dir so
  #    the existing .spec freezes the obfuscated code unchanged. CI checkout is
  #    disposable, so overwriting in place is fine.
  cp -R build/obf_pyarmor/* sidecar/
fi

pyinstaller --clean --noconfirm sidecar/keld-agent-sidecar.spec
echo "frozen -> dist/keld-agent-sidecar/ (obfuscated=$OBF)"
```
> Note: the exact `pyarmor gen` flags + how the `pyarmor_runtime` package is picked up by the PyInstaller `.spec` are **CI-iterated** — this is the correct-by-construction shape; the acceptance gate (Task 2) is what proves it. If the `.spec` doesn't auto-include `pyarmor_runtime`, add it to the spec's `hiddenimports`/`datas` during CI iteration.

- [ ] **Step 4: Run to verify the gate passes**

Run: `bash sidecar/test_build_freeze.sh`
Expected: PASS — `flag off -> ok` (exit 0), `flag on, no tools -> hard-fail` (exit 1).

- [ ] **Step 5: Commit**
```bash
chmod +x sidecar/test_build_freeze.sh
git add sidecar/build-freeze.sh sidecar/test_build_freeze.sh
git commit -m "feat(build): KELD_OBFUSCATE obfuscation gate in build-freeze.sh (hard-fail + --check)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Local CI-mirror runner (`scripts/obfuscated-freeze-local.sh` + `make obfuscate-check`)

Run the exact obfuscated-freeze + spawn-acceptance sequence CI runs, but locally on Linux — so the pipeline is debuggable without pushing, and the Linux slice is actually validated.

**Files:** Create `scripts/obfuscated-freeze-local.sh`; Modify `Makefile`.

**Interfaces:** Consumes Task-1 `build-freeze.sh`. Produces `make obfuscate-check`.

- [ ] **Step 1: Write the runner**

Create `scripts/obfuscated-freeze-local.sh`:
```bash
#!/usr/bin/env bash
# Local mirror of the CI obfuscated freeze + worker-spawn acceptance gate (Linux).
# Builds a throwaway venv with the build-only obf tools, freezes with
# KELD_OBFUSCATE=1, runs the frozen sidecar, and POSTs a real /classify (which
# spawns the worker child that must import the obfuscated modules).
set -euo pipefail
cd "$(dirname "$0")/.."
BV="$(mktemp -d)/bvenv"
PY312="${PYTHON312:-python3.12}"
"$PY312" -m venv "$BV"
"$BV/bin/pip" install --quiet --upgrade pip
"$BV/bin/pip" install --quiet python-minifier pyarmor
PORT="${PORT:-8399}"
MODEL="${KELD_GLINER2_DIR:-$HOME/.keld/models/gliner2-large-v1}"

KELD_OBFUSCATE=1 PYTHON="$BV/bin/python" bash sidecar/build-freeze.sh

BIN=dist/keld-agent-sidecar/keld-agent-sidecar
echo "== confirm obfuscation: source-like strings should NOT be greppable in the frozen app =="
if grep -rql "rename_globals\|_maintenance_trim" dist/keld-agent-sidecar/ 2>/dev/null; then
  echo "WARN: found plaintext identifiers in the frozen bundle"; fi

echo "== spawn acceptance gate =="
KELD_GLINER2_DIR="$MODEL" "$BIN" --port "$PORT" --host 127.0.0.1 &
SPID=$!; trap 'kill $SPID 2>/dev/null || true' EXIT
for i in $(seq 1 120); do curl -sf "http://127.0.0.1:$PORT/health" | grep -q '"ok"' && break; sleep 2; done
resp=$(curl -sf -X POST "http://127.0.0.1:$PORT/classify" -H 'Content-Type: application/json' \
  -d '{"text":"debug the login bug","tasks":{"task_type":["debug","other"]}}') \
  || { echo "FAIL: obfuscated worker classify failed (spawn/import broke?)"; exit 1; }
echo "$resp" | grep -q '"task_type"' || { echo "FAIL: no result: $resp"; exit 1; }
echo "PASS: obfuscated Linux freeze spawns a worker and returns: $resp"
```

- [ ] **Step 2: Add the make target**

In `Makefile`, add:
```make
.PHONY: obfuscate-check
obfuscate-check:   ## run the obfuscated freeze + worker-spawn gate locally (Linux)
	bash scripts/obfuscated-freeze-local.sh
```

- [ ] **Step 3: Run it (Linux local validation)**

Run: `chmod +x scripts/obfuscated-freeze-local.sh && make obfuscate-check`
Expected: `PASS: obfuscated Linux freeze spawns a worker and returns: {...task_type...}`. This is heavy (installs pyarmor/pyinstaller, freezes ~torch, loads the model) — minutes, CPU-bound; run niced. If the spawn gate fails, apply the spec fallback (exclude `worker.py`/`main.py` from obfuscation) and re-run.

- [ ] **Step 4: Commit**
```bash
git add scripts/obfuscated-freeze-local.sh Makefile
git commit -m "build: local CI-mirror runner for the obfuscated freeze + worker-spawn gate

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: `installers.yml` — Go strip, obf tools, spawn acceptance gate

**Files:** Modify `.github/workflows/installers.yml`.

- [ ] **Step 1: Install obf tools + set the flag on the freeze**

In the "Freeze sidecar" step, install the build-only tools and pass the flag (guard on a workflow condition — e.g. always on for the installer/release workflow, or a workflow input; default the env to `1` for this workflow):
```yaml
      - name: Freeze sidecar
        shell: bash
        env:
          KELD_OBFUSCATE: "1"
        run: |
          python -m pip install --quiet python-minifier pyarmor
          bash sidecar/build-freeze.sh
```

- [ ] **Step 2: Strip the Go binaries under the flag**

In the binary-build step (installers.yml:~89-90), add stripping to the existing ldflags. If the builds don't yet inject the version, add both:
```bash
          LDFLAGS="-s -w -X github.com/ncx-ai/keld-signal/internal/version.CLI=$VER"
          GOOS=$goos GOARCH=$arch go build -ldflags "$LDFLAGS" -o "stage/keld${ext}" ./cmd/keld
          GOOS=$goos GOARCH=$arch go build -ldflags "$LDFLAGS" -o "stage/keld-agent${ext}" ./cmd/keld-agent
```
(Confirm `$VER` is available in that step; reuse whatever the workflow already computes. If none, `VER=${GITHUB_REF_NAME#v}` or `dev`.)

- [ ] **Step 3: Extend the smoke step to the spawn acceptance gate**

After the `/health` wait (installers.yml:~61-65), add a real enrichment that forces a worker spawn (the model is already provided via the cached `HF_HOME`):
```bash
          # Acceptance gate: a real classify spawns the worker child, which must
          # import the obfuscated app.worker/app.main in the spawned process.
          resp=$(curl -sf -X POST http://127.0.0.1:8399/classify \
            -H 'Content-Type: application/json' \
            -d '{"text":"debug the login bug","tasks":{"task_type":["debug","other"]}}') \
            || { echo "obfuscated worker classify failed (spawn/import broke?)"; exit 1; }
          echo "$resp" | grep -q '"task_type"' \
            || { echo "classify returned no result: $resp"; exit 1; }
          echo "obfuscated worker spawn + inference OK"
```

- [ ] **Step 4: Commit**
```bash
git add .github/workflows/installers.yml
git commit -m "ci(installers): obfuscate freeze + strip Go binaries; smoke-test obfuscated worker spawn

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```
> This task is CI-validated only. Locally: confirm the YAML is well-formed (`python -c 'import yaml,sys; yaml.safe_load(open(".github/workflows/installers.yml"))'`) and that a stripped local Go build runs: `go build -ldflags "-s -w -X github.com/ncx-ai/keld-signal/internal/version.CLI=t" -o /tmp/keld-strip ./cmd/keld && /tmp/keld-strip --version`.

---

## Task 4: Docs

**Files:** Modify `AGENTS.md` (+ `CLAUDE.md` gotcha if apt).

- [ ] **Step 1: Document the flag**
Add to AGENTS.md (distribution/packaging section): `KELD_OBFUSCATE=1` (CI-set, default off) obfuscates the shipped sidecar (python-minifier locals-rename → free PyArmor → PyInstaller) and strips the Go binaries (`-s -w`); dev builds are plain/debuggable; hard-fails if requested but tools are unavailable. Note locals-only (globals/Pydantic-fields/spawn-targets preserved), the CI spawn acceptance gate, that it protects code logic **not** model identity (GLiNER2 is discoverable regardless), and that it's license-ready for paid PyArmor.

- [ ] **Step 2: Commit**
```bash
git add AGENTS.md CLAUDE.md
git commit -m "docs: document KELD_OBFUSCATE build-time obfuscation

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review
- Spec coverage: gate + hard-fail + `--check` test (T1) ✓; python pipeline locals-rename→pyarmor→freeze (T1) ✓; local CI-mirror runner + Linux spawn-gate validation (T2) ✓; Go `-s -w` (T3) ✓; CI spawn acceptance gate (T3 smoke) ✓; docs incl. non-goal + local runner (T4) ✓; license-ready (free-tier `pyarmor gen`, T1) ✓.
- Placeholders: the `pyarmor gen` flag details + spec `pyarmor_runtime` inclusion are explicitly flagged as CI-iterated, not vague TODOs.
- Consistency: `KELD_OBFUSCATE`, `--check`, `PYARMOR_FORCE_MISSING` test hook used consistently across build-freeze.sh + its test.
- Verification boundary stated per task (dev-verifiable vs CI-only).
