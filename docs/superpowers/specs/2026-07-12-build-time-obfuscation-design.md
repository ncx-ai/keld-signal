# Design: build-time obfuscation for the prod distribution

**Date:** 2026-07-12
**Status:** approved (design), pending implementation plan

## Problem

Shipped Keld client artifacts (the Go binaries + the PyInstaller-frozen Python
sidecar) currently contain readily-recoverable source: PyInstaller bundles are
unpacked with `pyinstxtractor` and the `.pyc` decompiled, and the Go binaries
retain full symbol/debug info. We want to raise the cost of casually copying the
**client's own code logic** — while keeping local dev builds fully plain and
debuggable. The switch must be build-time so the same tree produces a debuggable
dev build and an obfuscated prod-distribution build.

## Goals

- A single build-time flag `KELD_OBFUSCATE=1` (default off) that, when set,
  obfuscates the shipped sidecar Python and strips the Go binaries.
- Dev/local/PR builds stay plain (unset flag); only the CI installer/release
  workflow sets it, so **every shipped artifact is obfuscated** and nothing local
  needs to change.
- **Hard-fail:** if the flag is set but any obfuscation stage can't run (tool
  missing, error), the build exits non-zero — never silently ship plain code.
- Free-tier tooling now, structured so a paid PyArmor license drops in later with
  no rework.

## Non-goals (explicit)

- **Hiding which base model is used (GLiNER2/DeBERTa) is NOT a goal and is not
  achieved here.** The `gliner2`/`torch`/`transformers` packages are bundled
  verbatim by PyInstaller; the HF repo id `fastino/gliner2-large-v1` survives as a
  string literal (free-tier PyArmor does not encrypt strings; Go `-s -w` strips
  symbols, not strings); and the downloaded weights at
  `~/.keld/models/gliner2-large-v1/` (config declares the DeBERTa/extractor
  architecture) are inspectable on-disk regardless. Model-identity concealment
  would be a separate, much larger effort in tension with the on-device design;
  out of scope.
- Obfuscating the Go source beyond symbol stripping (it already compiles to
  native).
- A paid PyArmor license now (design is license-ready).
- Global-identifier renaming (see Approach — locals-only, to stay safe with
  Pydantic wire fields + `multiprocessing`-spawn pickle-by-reference).

## Approach

### Python obfuscation pipeline (in `sidecar/build-freeze.sh`, flag-gated)

When `KELD_OBFUSCATE=1`:
```
sidecar/app + serve.py
  → python-minifier   (rename_locals only; rename_globals OFF)
  → pyarmor gen        (free tier: bytecode encryption + anti-tamper runtime)
  → PyInstaller        (freeze the obfuscated tree → keld-agent-sidecar)
```
When unset: freeze the plain source exactly as today.

- **python-minifier** (dflook, MIT, AST-based, Python-3.12-safe) renames only
  function/comprehension-scope locals. Globals, module names, class attributes
  (Pydantic fields), and the spawn targets (`app.worker.serve`, `_model_factory`)
  are left intact — renaming any of those breaks the daemon↔sidecar wire contract
  or the `multiprocessing`-spawn pickle-by-reference. No preserve-list to
  maintain.
- **PyArmor free tier** (`pyarmor gen`) encrypts the (already locals-renamed)
  bytecode and adds its runtime. Only *our* modules are obfuscated; bundled
  third-party packages are untouched (and out of scope per non-goals).
- **License-ready:** a paid license (registered in CI via a secret) later enables
  RFT/BCC/expiry modes through the same `pyarmor gen` invocation — no pipeline
  rework.
- **Hard-fail:** if `KELD_OBFUSCATE=1` and `python-minifier` or `pyarmor` is
  absent or returns non-zero, `build-freeze.sh` exits non-zero before PyInstaller
  runs.
- Build-only deps: `python-minifier` and `pyarmor` are installed in the CI freeze
  step (like `pyinstaller` already is), NOT added to the sidecar runtime
  `requirements.txt`.

### Go stripping (in `.github/workflows/installers.yml`, flag-gated)

The distribution `go build` steps add `-s -w` to `-ldflags` when
`KELD_OBFUSCATE=1`, composed with the existing `-X …version.CLI=$VER`:
```
-ldflags "-s -w -X github.com/ncx-ai/keld-signal/internal/version.CLI=$VER"
```
`-s -w` drops the symbol table + DWARF; it preserves `-X` string values and Go
runtime stack traces. Dev `make build-binaries` stays unstripped.

## Acceptance gate (the real risk)

The inference worker uses `multiprocessing` **spawn**, which re-imports
`app.worker`/`app.main` in the child. Locals-rename + PyArmor bytecode-encryption
+ spawn all interact, and the PyArmor runtime must be importable in the spawned
child. Therefore the CI smoke-test of the **obfuscated frozen** sidecar MUST drive
a real enrichment — POST `/classify`, which triggers a worker spawn, and assert a
**200 with a real result** — not merely probe startup/`/health`. A startup-only
check would green-light an obfuscated sidecar that can't spawn its worker.

**Fallback** if spawn cannot survive obfuscation of the spawn-imported modules:
exclude `worker.py` and `main.py` from the python-minifier/PyArmor step (they are
the least IP-sensitive glue), obfuscating the rest. The plan encodes this as the
documented fallback, chosen only if the CI acceptance gate fails.

## What is verifiable where

- **Dev env (Go/Linux, this repo):** the flag plumbing + hard-fail is testable
  without the per-OS toolchain — a `build-freeze.sh` run with `KELD_OBFUSCATE=1`
  and the tools absent must `exit 1`; unset must take the unchanged plain path.
  Go: a stripped `go build -ldflags "-s -w -X …"` produces a binary that runs and
  whose `keld --version` still reports the injected version.
- **CI only (per-OS runners):** the actual obfuscated freeze + the
  worker-spawn acceptance smoke test. PyArmor/python-minifier + the per-OS
  PyInstaller toolchain are not in the dev env, so this is CI/human-verified — the
  same posture as the existing frozen-sidecar and installer-UX steps. The
  implementer must NOT claim the obfuscated freeze is locally verified.

## Components / files

- `sidecar/build-freeze.sh` — flag-gated obfuscation stage + hard-fail; installs
  build-only `python-minifier`/`pyarmor`.
- `.github/workflows/installers.yml` — pass `KELD_OBFUSCATE=1` on the
  installer/release build; add `-s -w` to the Go builds under the flag; extend the
  post-freeze smoke step to POST `/classify` and assert a 200 result.
- (No change to `sidecar/requirements.txt` — runtime deps unchanged.)
- `AGENTS.md` + packaging gotchas — document `KELD_OBFUSCATE`, the
  dev-plain/prod-obfuscated split, locals-only rename rationale, the model-hiding
  non-goal, and the license-ready note.

## Error handling

`build-freeze.sh` runs `set -euo pipefail`; each obfuscation stage's non-zero exit
propagates. An explicit precondition check (`command -v pyarmor`, python-minifier
import) emits a clear message and exits 1 when the flag is set. Go stripping can't
"fail" beyond a normal build error.

## Testing

- `build-freeze.sh` flag logic: a dry-run harness (or a `--check`/no-op mode) that,
  with `KELD_OBFUSCATE=1` and a tool forced absent, asserts exit 1; unset asserts
  the plain path is selected. Runnable in the dev env (no real freeze).
- Go: assert the stripped binary runs + `--version` is correct (dev env).
- CI acceptance: obfuscated frozen sidecar → POST `/classify` → 200 with a
  populated result (spawn survives obfuscation). Human/CI-verified.

## Risks / notes

- **Biggest risk:** obfuscation breaks `multiprocessing`-spawn worker startup —
  mitigated by the mandatory spawn acceptance gate + the documented
  exclude-`worker.py`/`main.py` fallback.
- Free-tier PyArmor protection is deterrence, not secrecy (bypassable by a
  determined RE); paired with license/ToS. This is accepted (goal is raising
  casual-copy cost).
- No runtime dependency added; the sidecar's runtime venv/`requirements.txt` is
  unchanged — obfuscation is entirely a freeze-time transform.
