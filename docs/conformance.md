# Conformance — proving an integration against the real tool

The conformance harness installs a **real** AI coding tool, onboards Signal the
way an installer does, points the tool at a **mock model** and Signal at a
**mock Atlas** on loopback, sends one scripted prompt, and asserts five
checkpoints against the isolated daemon:

| # | checkpoint | read from |
|---|---|---|
| 1 | transcript written | the tool's transcript root under the isolated `HOME` |
| 2 | pointer received | an enrichment at the mock Atlas whose `corr_id` is a prompt id from **this** run |
| 3 | store rows | `refseries.db`, through `sqlite3` (or `python3`'s module) |
| 4 | telemetry forwarded | the mock Atlas's `/v1/logs` + `/v1/metrics` counters |
| 5 | block or enrichment published | the mock Atlas's counters |

It is the answer to a specific failure class this repo has already paid for
twice: a green `keld signal doctor` beside a silent lane, because every fact
either half could reach was fine and **neither half knew what the other was**.

Contract: **AC-10** (it runs and exits 0), **AC-11** (the matrix and the
deduplicated issue), **AC-12** (every installer has an unattended mode). Spec:
`docs/superpowers/specs/2026-09-15-signal-integrations-discovery.html`,
§ "Conformance matrix — how it runs on GitHub".

---

## Three places it runs, and what each one can prove

| where | command | proves | does **not** prove |
|---|---|---|---|
| this machine | `make conformance TOOL=claude_code` | the five lanes against **the tool version you have installed** | nothing about a clean machine; the daemon runs foreground, no service is registered |
| a Linux container | `make conformance-linux TOOL=claude_code` | the same, against **`tool@latest`** on a clean filesystem | **service registration — impossible in a container**; the installers |
| a macOS VM (Tart) | `make conformance-macos-vm TOOL=claude_code` | the packaged `.pkg` installed **unattended**, real LaunchAgent registration (AC-12) | ⚠️ **unverified — nobody has run it yet** |
| GitHub | `.github/workflows/conformance.yml` | all of the above on three OSes, on a tool release and on a Signal release | Windows, until `run-chain.ps1` exists |

**Why the host leg never registers a service.** `KELD_HOME` isolates `~/.keld`
but **not** the service path, which `service.Install` resolves from
`os.UserHomeDir()`. A host run that called `keld-agent install` would rewrite
the developer's real LaunchAgent to point at a temp binary and restart it into
`failed`. So the harness starts the daemon in the foreground, and the installer
path belongs to the disposable machines: the VM leg and the CI runners.

---

## The Linux container leg

```bash
make conformance-linux TOOL=claude_code        # CHAIN=A SEED=42 optional
```

`scripts/conformance/compose/` — ubuntu 24.04, Node 22, Python 3.12, `sqlite3`,
the Go toolchain, the repo mounted **read-only**, everything written going to
named volumes (`/work`, `/cache`, the fetched sidecar).

`sqlite3` is there for a reason, not for comfort: checkpoint 3 shells out to it,
and without a reader on `PATH` `gather.go` returns *"no sqlite reader"* — an
error, not zero rows, so the run would read as broken for the wrong reason.

⚠️ **Service registration is not exercised, and the entrypoint says so on every
run.** There is no launchd and no systemd user bus in a container. A green
container result covers the tool, the adapters and the five lanes; it does not
cover installing Signal.

⚠️ **On an Apple Silicon Mac this leg cannot complete a chain.** The only
published Linux sidecar is `keld-agent-sidecar_linux_amd64.tar.gz`
(installers.yml freezes one, in a manylinux_2_28 amd64 image), so an arm64
container has nothing to resolve. Run it on an amd64 Linux host — which is what
`ubuntu-latest` is — or set `DOCKER_DEFAULT_PLATFORM=linux/amd64` and accept
qemu, where torch is impractical.

**What was actually verified on a Mac (2026-09-15):** the image builds; inside
it `node v22.23.2`, `Python 3.12.3`, `sqlite3 3.45.1`, `go1.27.1`; `/repo` is
read-only and `/work` writable; the entrypoint prints the service-registration
caveat and hands over to `run-chain.sh`, which stops at sidecar resolution as
designed. A complete chain inside the container has **not** been run here.

## The macOS VM leg (Tart)

```bash
make conformance-macos-vm TOOL=claude_code [PKG=path/to/keld-x.y.z-arm64.pkg]
```

`scripts/conformance/tart/{up,run,down}.sh`: clone
`ghcr.io/cirruslabs/macos-sequoia-base:latest`, boot it headless, stream the
repo in over `tart ssh`, install the `.pkg` **unattended**
(`installer -pkg … -target /` — if that ever needs a click, the installer has
failed AC-12), run the chain, copy the evidence out, delete the VM.

⚠️ **UNVERIFIED. Nothing in that directory has been run.** It needs `tart`
installed and a large image pull, neither of which was done. Treat the first
run as the test.

Before that first run, two things are a person's decision and not the script's:

- **Disk.** The base image is ~40 GB after pull (cached under `~/.tart/cache`,
  plus a per-VM clone). `down.sh` deletes the VM; the cache stays.
- **Licence.** Tart is free for open source and individual use and requires a
  paid sponsorship for commercial use beyond a small team. Check that before
  `brew install cirruslabs/cli/tart`. Nothing here installs it for you.

The VM is **deleted by default**, including on failure, because the point of it
is that it is clean: a reused VM already has a Signal install, a tool install
and a LaunchAgent from last time — the exact state chain A claims not to start
from. `--keep` overrides it and says so. The run's own output is copied out
*before* the delete, so the evidence outlives the VM.

---

## GitHub: `.github/workflows/conformance.yml`

⚠️ **It is committed with its triggers in place but is NOT ON YET.** GitHub runs
`schedule` (and dispatch) from the **default branch's** copy of a workflow, so
this file does nothing while it lives on `feat/integrations-ci` / `release/v3`.
It starts the day the branch merges to the default branch — which is also why it
is safe to land now.

| trigger | what runs | why |
|---|---|---|
| `schedule` (daily, 06:17 UTC) | `version-watch.sh`: `npm view <pkg> version` per tool against `.conformance/last-tested.json`. A change starts **chain A on ubuntu-latest for that tool only**; on a pass, the new version is committed to the file. | The tool-release canary. It **replaces the nightly full matrix** — a run with nothing new to test proves nothing new and spends the money in the wrong place. |
| `workflow_run` of `installers.yml`, tag `v*`, conclusion success | the full matrix: `os ∈ {ubuntu-latest, macos-14, windows-latest} × chain ∈ {A, B}`, against **that run's artifacts** | installers and service registration are OS-specific; this is the Signal-release gate. |
| `workflow_dispatch` | inputs `tool`, `version`, `os` (incl. `all`), `chain` (incl. `all`), `seed`, `force_fail` | a manual run, a pinned-version rerun, one OS, or the forced failure that proves the issue step (AC-11). |

Also: `fail-fast: false`, `timeout-minutes: 25`, `concurrency` (never
`cancel-in-progress` — a chain killed half way tells you nothing),
`permissions: issues: write` on the chain job, a `GITHUB_STEP_SUMMARY` table per
cell, and `upload-artifact` **on failure only**.

**The artifact fallback is not belt-and-braces, it is required.**
`installers.yml` uploads workflow artifacts on its **dry-run dispatch** path;
on a real release it attaches the pkg/exe/sidecar to the **GitHub release**
instead. So the job tries `download-artifact` for that run and then
`gh release download` for the tag, and neither failing stops the chain.

**What may be uploaded is an exhaustive list, never a directory.** Daemon log,
mock model and mock Atlas logs, the verdicts, the isolated config files, and
`home/.claude/projects/**/*.jsonl` — which under an isolated `HOME` can only be
the transcript of **our own** scripted prompt ("reply with one word"). No user
transcript is reachable from that path, and that is a property of the isolation
rather than of a filter.

⚠️ **The Windows leg is expected to fail and is marked `continue-on-error`.**
`scripts/conformance/run-chain.ps1` does not exist yet (task A.3's Windows half),
so the leg calls it, says exactly that, and goes amber. A permanently red leg
nobody reads is worse; deleting the leg would hide that Windows is unproven.

### `.conformance/last-tested.json`

The versions conformance has **proven**, and the state the canary compares
against. A commit there means a chain went green at that version — never that
somebody looked. `harness: "supported"` means the tool is in
`scripts/conformance/lib/tools.sh` and may be dispatched; `"pending"` tools are
watched and reported but never run, because `run-chain.sh` exits 2 on them.
`version: null` is **first sight: record, do not dispatch** — a version nobody
has proven is not a change.

```bash
scripts/conformance/version-watch.sh              # the report (~2 s, four npm calls)
scripts/conformance/version-watch.sh --matrix     # the matrix JSON the workflow consumes
scripts/conformance/version-watch.sh --record claude_code 2.1.272
```

### Filing issues, and why the dedup is the feature

`scripts/conformance/file-issue.sh` titles the issue

```
conformance: <tool> <version> <os> <chain>/<step> <checkpoint>
```

and that title **is** the key: `gh issue list --search "<title>" in:title
--state all`, then a local **exact-title** comparison decides. GitHub's search
is a tokenising, eventually-consistent index — a near-miss accepted as a hit
comments on the wrong issue; a near-miss rejected costs one duplicate. An
existing issue gets a **comment** (the history of "it broke again, here" is the
useful part) and is reopened if closed. A step that files a fresh issue every
run is worse than no step at all: the fifth duplicate is what stops people
reading the first.

Test it without touching the real repo — either `--dry-run`, or a fake `gh`
first on `PATH`:

```bash
export FAKE_GH_STORE=/tmp/issues.json PATH=/tmp/fakegh/bin:$PATH
scripts/conformance/file-issue.sh --tool claude_code --version 2.1.272 \
  --os ubuntu-latest --chain A --step after-signal --checkpoint pointer --seed 4211
# -> action=created
scripts/conformance/file-issue.sh ... same flags ...
# -> action=updated   (one issue, one comment)
```

### Cost

GitHub-hosted minutes on a private repo, at the spec's 2026-01-01 rates —
Linux **$0.006**/min, Windows **$0.010**/min, macOS **$0.062**/min. Against
included minutes a Windows minute counts 2× and a macOS minute 10×.

| trigger | runner-minutes | cost |
|---|---|---|
| daily version watch | ~1-2 Linux (measured: the script itself is **1.8 s**; the rest is checkout + setup-node) | **$0.18-0.36 / month** |
| a tool release (canary fires) | plan + 1 Linux cell ≈ 21 | **≈ $0.13** per release |
| a Signal release (6 cells × ~20) | 40 Linux + 40 macOS + 40 Windows | **≈ $3.12**, of which macOS is **$2.48** |

At two tool releases a week and two Signal releases a month: **≈ $8/month**,
against ≈ $105 for the nightly full matrix this replaces. Today the two Windows
cells fail in about a minute (no `run-chain.ps1`), so a Signal release actually
costs nearer **$2.75** until that lands — and a self-hosted Mac would take the
macOS leg to zero, which is where the money is.

---

## Troubleshooting

- **"no analysis sidecar"** — the harness needs one of: the `make sidecar` venv,
  a frozen sidecar under `~/.local/bin/keld-agent-sidecar`, or
  `KELD_CONFORM_PYTHON` pointed at an interpreter that can run `sidecar/serve.py`.
  It refuses rather than running without one, because checkpoint 3 would then be
  meaningless.
- **The tool is not found** — a local run uses the tool **as installed on this
  machine** (`KELD_CONFORM_CLAUDE_BIN` overrides). `KELD_CONFORM_INSTALL=1`
  npm-installs `@latest` into an isolated prefix instead; the container and CI
  legs set it.
- **A failing checkpoint's last line is machine-parseable** — `{tool, chain,
  step, checkpoint, seed}`. The **seed** is what reproduces the run:
  `make conformance TOOL=<tool> CHAIN=<chain> SEED=<seed>`.
