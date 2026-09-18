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

**AC-12's own page is `docs/install.md`** — the silent invocation per artifact,
what each verifies, and the MDM path. The harness never invents an installation
route: it runs `scripts/conformance/install-<os>.sh`, which run the same
commands that page documents.

---

## Three places it runs, and what each one can prove

| where | command | proves | does **not** prove |
|---|---|---|---|
| this machine | `make conformance TOOL=claude_code` | the five lanes against **the tool version you have installed** | nothing about a clean machine; the daemon runs foreground, no service is registered |
| a Linux container | `make conformance-linux TOOL=claude_code` | the same, against **`tool@latest`** on a clean filesystem | **service registration — impossible in a container**; the installers |
| a macOS VM (Tart) | `make conformance-macos-vm TOOL=claude_code PKG=…` | the packaged `.pkg` installed **unattended**, real LaunchAgent registration (AC-12) | ⚠️ **unverified — nobody has run it yet** |
| any disposable machine | `scripts/conformance/install-<os>.sh` | one artifact installed silently, onboarded by command, verified from observed state | nothing about the five lanes — that is the chain's job |
| GitHub | `.github/workflows/conformance.yml` | all of the above on three OSes, on a tool release and on a Signal release | Windows, until `run-chain.ps1` exists |

**Why the host leg never registers a service.** `KELD_HOME` isolates `~/.keld`
but **not** the service path, which `service.Install` resolves from
`os.UserHomeDir()`. A host run that called `keld-agent install` would rewrite
the developer's real LaunchAgent to point at a temp binary and restart it into
`failed`. So the harness starts the daemon in the foreground, and the installer
path belongs to the disposable machines: the VM leg and the CI runners.

---

## Installing the artifact (AC-12)

```bash
# the chain, driving a REAL release rather than a source build
make conformance TOOL=claude_code ARTIFACT=dir:./artifacts

# or one artifact on its own
KELD_CONFORM_DISPOSABLE=1 scripts/conformance/install-macos.sh --pkg keld-3.0.0-arm64.pkg --code CONFORM
KELD_CONFORM_DISPOSABLE=1 scripts/conformance/install-linux.sh --artifacts ./artifacts --code CONFORM
```

`--artifact dir:<path>` follows `--previous dir:<path>`'s shape and changes only
what the chain's **signal-install** step does: the release is installed the
unattended way, onboarded with `keld login --code` / `keld signal setup --yes` /
`keld-agent install`, verified, and then the chain runs on the **installed**
binaries. Without it the chain builds from source exactly as before.

⚠️ **The artifact install runs against the machine's own HOME, not the isolated
one**, because a package installs system-wide and a service registration is not
isolated by `KELD_HOME`. Verifying a LaunchAgent under a temp HOME would verify
one nothing would ever load. The five lanes go on being proved in the isolated
HOME immediately afterwards — that split is why the chain's shape is unchanged.

⚠️ **Every install script refuses a machine that has not declared itself
disposable** (`KELD_CONFORM_DISPOSABLE=1`, or `CI=true`), and the harness does
**not** set that variable for you: a harness that set it would have deleted the
guard rather than passed it.

**What has actually been run (2026-09-16):**

1. `install-macos.sh` and `install-linux.sh` against a **local fake release** —
   inert stand-in binaries served over loopback — covering argument handling,
   `install.sh`'s unattended path, and all four verification outcomes, including
   a run where every command exits 0 and the script still fails at
   `verify-onboarded` because no `hook.json` was written.
2. ⚠️ **`install-linux.sh --tag v2.5.0` against the REAL published release**, in
   a disposable container, on both architectures. This is the run that matters,
   and it is the one a fake release cannot substitute for — see below.
3. ⚠️ **Windows has no install script here, and does not need one.** There was
   an `install-windows.ps1`; it was deleted because it was a second, never-run
   implementation of a claim the workflow already proves on every run. The
   Windows cells install the REAL `keld-setup.exe` with `/VERYSILENT
   /SUPPRESSMSGBOXES /NORESTART` — once on chain A, and twice on chain B (old
   then new) because Windows ships the sidecar only inside that .exe, so the
   upgrade pair can only be built by installing both in order. A non-zero Inno
   exit fails the step, and exit 5 ("cancelled/declined", what a silent install
   returns rather than overwrite a running one) is named there so the next
   reader is not sent to a search engine by a number. Keeping an unexecuted
   duplicate beside that made Windows LOOK unproven when it is the best-proven
   of the three.

### ⚠️ What the REAL release found, and the fake one could not

**A fake release builds whatever archive it is asked for, so it can never be
missing one.** That is not a smaller version of the test — it is a different
test, one with no failure mode. Run against the real thing, two defects
surfaced immediately:

**1. Linux arm64 cannot be installed at all, on any published release.**
`scripts/install.sh` accepts `arm64|aarch64` and then downloads
`keld-agent-sidecar_linux_arm64.tar.gz`, which **is not published** — v2.5.0 and
v3.0.0-rc.3 both ship `keld_linux_arm64.tar.gz` (the CLI) with no arm64 sidecar
beside it. The installer treats the sidecar as mandatory and aborts, so the run
ends with the binaries on disk, nothing onboarded, exit 1:

```
  ✓ keld + keld-agent          → /work/bin
curl: (22) The requested URL returned error: 404
keld: analysis sidecar install failed — ... Aborting.
  URL: .../v2.5.0/keld-agent-sidecar_linux_arm64.tar.gz
```

This is **exactly** the failure `scripts/verify-release-assets.sh` was written
for after v0.20.0 ("every Linux curl|sh install hard-failed until the job was
re-run four days later") — and the gate does not catch it, because
`keld-agent-sidecar_linux_arm64.tar.gz` is deliberately absent from its
`expected` manifest. So the gate reports the release COMPLETE while an
advertised platform cannot install. Three ways out, and the choice is not this
document's: publish the arm64 sidecar, stop publishing the arm64 CLI, or make
`install.sh` refuse arm64 Linux by name instead of by 404.

**2. `--allow-no-service-manager` could not rescue a container, which is what it
was written for — now fixed.** Its header says a container has no systemd user
bus and the flag downgrades the "is it loaded" half of the check. But it only
affected `verify-service`, and `install.sh` exited 1 long before that:
`keld-agent install` registers the unit and then STARTS it. Measured both ways
round: with no systemd installed, `exec: "systemctl": executable file not found
in $PATH`; with systemd installed but no user bus, a bare `exit status 1`.

A non-zero exit from `install.sh` is now DEFERRED behind that flag rather than
fatal — stated out loud, with every verification below still required to pass on
observed state. That is the script's own rule applied symmetrically: it already
refuses to trust exit 0 ("install.sh can exit 0 having placed files and
onboarded nobody"), and the converse is just as true — it can exit 1 having
placed everything correctly. Nothing is skipped and no check is weakened; the
closing line says `EXITED 1` rather than claiming a clean install. Without the
flag a non-zero exit is still fatal.

**3. Onboarding needs a tool to configure.** With no AI tool on the machine,
`keld signal setup --yes` answers "No supported tools detected", writes no
`hook.json`, and `verify-onboarded` fails — correctly. A container therefore
needs one planted (or installed, as the Compose leg does).

### The real release, installed end to end (2026-09-16, linux/amd64)

With those three understood, `install-linux.sh --tag v2.5.0` against the REAL
published release completes and exits 0:

```
  ✓ keld + keld-agent          → /work/bin
  ✓ analysis sidecar           → /work/bin/keld-agent-sidecar
  ✓ /work/bin/keld — keld version 2.5.0
  ✓ /work/bin/keld-agent — keld-agent version 2.5.0
  ✓ sidecar v2.5.0 at /work/bin/keld-agent-sidecar
  ✓ ~/.config/systemd/user/keld-agent.service exists and names keld-agent
  ✓ systemctl --user is-enabled keld-agent.service -> enabled
    systemctl --user is-active  -> Failed to connect to bus: No medium found
  ✓ ~/.keld/hook.json holds an ingest token (20 chars, not printed)
OK — verified from observed state, but scripts/install.sh EXITED 1
```

Real statically linked ELF binaries, the real 1.2 GB sidecar tree, the unit file
**enabled**, and a real ingest token written by a real onboarding against the
mock Atlas. What is still NOT proven anywhere: the macOS `.pkg` — installing one
rewrites the developer's own LaunchAgent, so it needs a VM or CI, and the CI
step that runs it fires only on a Signal release.

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
