# Auto-update

The daemon moves itself, the `keld` CLI and the analysis sidecar to the release
Atlas names.

> **Status — 2026-08-27: client shipped, INERT.** Atlas does not serve
> `agent_release`, so no machine updates. Two Atlas-side decisions are owed
> before it can be switched on (see *Who sets the target*), one of them a
> security question.
>
> **This status is dated and does not update itself.** To re-check: does the
> `/v1/enrichment-settings` response carry an `agent_release` key? If it does,
> this block is stale — replace it with the date it went live and the decisions
> that were taken.

Spec: `docs/superpowers/specs/2026-08-27-signal-auto-update-design.md`.
Rationale and traps: *Rationale and traps* below (split out of `AGENTS.md` on
2026-09-17, last substantively changed there on 2026-08-27).

## Turning it on

Atlas serves an `agent_release` block on the existing
`GET /v1/enrichment-settings` poll:

```json
{ "agent_release": { "enabled": true, "version": "v0.4.2", "base_url": null } }
```

| Field | Meaning |
|---|---|
| `enabled` | Absent or false ⇒ **no updates**. Never defaults on. |
| `version` | A **pin, not a floor** — machines move to it in either direction. This is the rollback lever. |
| `base_url` | Asset host override. Null ⇒ the GitHub release download path. |

Roll out by moving the pin forward; roll back by moving it backward. There is no
other lever — `ml_backend` and `blocks` have no remote override, and this is the
one control plane that reaches an installed machine.

## Who sets the target

**As of 2026-08-27, nothing does.** `agent_release` rides the same per-org
settings document that already carries `include_entity_text`,
`client_telemetry` and `enrichment_schema`, so mechanically it is set wherever
those are set — an Atlas org settings record. On that date Atlas served no such
key and had no producer for one.

Two decisions were owed on the Atlas side as of that date, and neither belongs
to this repo. **If you are reading this later, check whether they were taken
before trusting the paragraph below** — an unresolved-question note that nobody
dates is indistinguishable from one nobody revisited:

1. **Who may write it** — Keld operators only, or org admins for their own fleet.
   Pinning a version is the power to decide which code runs on every machine in
   an org.
2. **Whether `base_url` is writable at all.** ⚠️ `version` + `base_url` together
   are "fetch this binary from this host and run it". The checksum gate does not
   constrain that: `checksums.txt` is fetched from the *same* `base_url`, so it
   proves the download was not corrupted in transit, not that it came from Keld.
   Whoever can write `base_url` can install arbitrary code on every machine in
   the org.

   Options, cheapest first: **(a)** Atlas never serves `base_url` and the client
   ignores it — mirrors are configured locally instead; **(b)** the client
   accepts a server-supplied `base_url` only when a local env var permits that
   host, so a mirror is locally consented rather than server-granted;
   **(c)** sign release assets and verify against a public key compiled into the
   binary, which is the only option that makes the host untrusted.

   **None of the three was implemented as of 2026-08-27** — the choice is
   Atlas's, not this repo's. Until one is chosen and recorded here with the date
   it was taken, treat write access to `agent_release` as equivalent to root on
   every machine in the org.

## Sequence

1. Settings poll delivers the pin. The decision is a pure function; only the work
   runs on a goroutine, single-flighted, so the poll never blocks.
2. Fetch `keld_<os>_<arch>.tar.gz` and `keld-agent-sidecar_<os>_<arch>.tar.gz`,
   verified against `checksums.txt` or a per-file `.sha256`. **A missing hash is
   fatal** (`install.sh` only warns — it has a human who can abort).
3. Pre-flight: run the downloaded `keld-agent --version`.
4. Swap: rename each outgoing artifact to `<name>.prev`, move the new one in.
   Staging is inside the destination, so the commit is a same-filesystem rename.
5. Write `~/.keld/update/state.json` (**before** the restart), then restart the
   service after a bounded wait for the enrichment queue to drain.
6. The next daemon start confirms or restores — before `awaitConfig`, so an
   un-onboarded machine still self-heals.

Anything failing before step 4 leaves the install untouched. Anything failing
during it is rolled back from `.prev`.

## Failure modes

| Outcome | Trigger | Result |
|---|---|---|
| `applied` | Came up as the new version within the deadline | Marker cleared, `.prev` deleted |
| `rolled_back` | Came up as the **old** version | Restore, record the failure, restart |
| `rolled_back` | Marker still pending past `KELD_UPDATE_CONFIRM_DEADLINE` | Restore regardless of running version — the new binary crashed before it could clear its own marker |
| `rollback_failed` | Restore itself failed | Reported at error severity, surfaced by `doctor`, **no restart** |

A version that rolled back is **not retried until the pin moves**. Without that,
Atlas still pins the bad version and the machine loops: swap, crash, roll back,
swap.

A failed *restart* deliberately leaves the marker pending: the swap already
happened, so the next start must still be able to undo it.

## Why nothing happened

`update.skipped` carries `fields.reason`:

| Reason | Meaning |
|---|---|
| `dev_build` | Not a release build (`version.CLI == "dev"`) |
| `disabled` | Org has not enabled updates, or serves no block |
| `disabled_local` | `KELD_AUTOUPDATE=0` — local refusal beats remote permission |
| `no_target` | Enabled but no version pinned |
| `up_to_date` | Already on the pin |
| `failed_previously` | Applied here and rolled back; waiting for the pin to move |
| `too_soon` | Within `KELD_UPDATE_MIN_INTERVAL` of the last attempt |
| `pending_confirm` | An update is staged and awaiting its restart |

## macOS `.pkg` installs migrate

The pkg installs to root-owned `/usr/local/keld`, which an unprivileged daemon
cannot write. It installs to `~/.local/bin` instead and repoints the LaunchAgent
via `service.InstallAt` (not `service.Install`, which reads `os.Executable()` —
still the old path mid-migration). Emits `update.migrated`.

**Known limit:** `/usr/local/bin/keld` is a root-owned symlink at a PATH position
ahead of `~/.local/bin`, and cannot be rewritten. The daemon converges; a human's
`keld` may not. `keld signal doctor` reports the link with the exact `ln -sf`.

## Environment

| Variable | Default | Effect |
|---|---|---|
| `KELD_AUTOUPDATE=0` | unset | Refuse updates on this machine |
| `KELD_UPDATE_MIN_INTERVAL` | `1h` | Floor between attempts, independent of the poll rate |
| `KELD_UPDATE_CONFIRM_DEADLINE` | `15m` | How long an update may stay unproven |

## Observability

Client-events (catalog: `docs/signal-client-events.md`): `update.available`,
`update.skipped`, `update.staged`, `update.migrated`, `update.applied`,
`update.rolled_back`, `update.failed`, `update.restart_failed`.

`keld signal status` / `doctor` read `state.json` **from disk only** — never the
daemon, never a release host, and they never trigger an update. A rollback is
reported as the self-heal working, not a breakage. A machine that has never
updated reports nothing.

## Files

```
internal/agent/update/      decide, fetch, swap, confirm, migrate  (81 tests)
internal/agent/daemon/update.go   wiring: onRemote trigger + startup confirm
internal/localagent/update.go     status/doctor lines, disk-only
internal/agent/settings/release.go  the agent_release block
~/.keld/update/state.json   the marker (0600)
```

## Before first rollout

*Open as of 2026-08-27. Strike each item with the date it was closed.*

- Atlas does not serve `agent_release`; the client is inert until it does.
- **Decide the `base_url` trust question above first.** It is the one thing here
  that cannot be safely deferred past the first real rollout.
- Nothing has run against a real release. Start with one pinned machine and watch
  for `update.applied`.
- The Windows path is unit-tested (naming, zip archive) but has not executed on a
  Windows machine.

## Rationale and traps

**Auto-update (`internal/agent/update/`).** The daemon moves itself, the `keld`
CLI and the frozen sidecar to the release **Atlas names** — fetch, verify,
swap, restart, confirm, roll back. Two seams and **no new clock**: the trigger
is the settings poll's existing `onRemote` hook, and the confirm pass runs at
the top of `Run`.

⚠️ **The version source is ATLAS, NOT `releases/latest`, and that is the whole
point rather than a preference.** This file already states the problem for
`ml_backend`: "an existing fleet's Atlas Context column therefore empties
machine-by-machine at whatever pace people upgrade, **with no server-side
brake**. If that pace ever needs controlling, the control is a staged rollout
of the installer itself." A client that resolves `releases/latest` itself
converges the entire fleet the moment a tag is pushed — that is the same defect
with a faster fuse, not a smaller version of it. So `settings.Remote.Release`
(`agent_release`) carries `{enabled, version, base_url}`, pointer-fielded like
`PIIRegions`/`Features`, and **an absent block means NO UPDATE** — the
strictest reading of the omitted-key rule, because unattended binary
replacement is the last thing that should be reachable from the server by
omission. `KELD_AUTOUPDATE=0` refuses locally; **local refusal wins, local
permission never does.** Atlas does not serve the key yet, so the seam exists
and nothing moves until it does.
⚠️ **`version` is a PIN, not a floor**, and the daemon moves to it in EITHER
direction. A control plane that can only move a fleet forward is not a brake,
and the brake is the entire reason for choosing Atlas — so a downgrade is a
first-class supported operation, not an error case. Comparison is identity
after normalizing one leading `v`, never semver ordering: ordering is what a
floor needs, and it has parsing edge cases a pin does not.
⚠️ **A MISSING PUBLISHED SHA-256 IS FATAL HERE, and `install.sh` warns and
continues.** The divergence is deliberate and is the one asymmetry worth
remembering: the installer has a human reading its output who can abort, and an
unattended swap does not. So the single case where the installer degrades
gracefully is the case this must refuse. Staging lives **inside** the
destination (no `/tmp` fallback) for the reason `install.sh` gives — the commit
must be a same-filesystem rename, not a cross-device copy of the sidecar's
~15,000 files.
⚠️ **THE macOS `.pkg` CANNOT BE UPDATED IN PLACE, AND ITS SYMLINKS ARE THE
TRAP.** The pkg stages to a root-owned `/usr/local/keld`, so an unprivileged
daemon migrates: it installs to `~/.local/bin` and repoints the LaunchAgent via
**`service.InstallAt`** — new, because `service.Install` reads
`os.Executable()`, which at that moment is still the OLD path, and using it
would leave launchd starting the stale binary forever while the update reported
success. Two more consequences, each implemented rather than hoped away.
`installers/macos/scripts/postinstall` **repoints `~/.local/bin/keld` to a
root-owned SYMLINK back at `/usr/local/keld/keld`**, so `Swap.Replace` uses
`os.Lstat`, never `os.Stat` — following the link would displace the pkg's own
binary, which we do not own, while the link itself sits in a user-owned
directory and is ours to replace. And `/usr/local/bin/keld` (root-owned,
usually ahead of `~/.local/bin` on PATH) **cannot be rewritten at all**: the
daemon converges and the CLI a human types does not, so
`keld signal doctor` names that link with the exact `ln -sf` rather than
letting the install quietly disagree with itself about its own version.
⚠️ **AUTO-ROLLBACK ALONE IS UNSTABLE — `failed_versions` is what closes the
loop.** `~/.keld/update/state.json` is written **before** the restart, and
cleared only by a daemon that came up as the new version. Three outcomes, and
the third is the one that makes a bad release self-healing rather than a
bricked fleet: past `KELD_UPDATE_CONFIRM_DEADLINE` (15m) the swap is undone
**whichever version is running**, because a binary that crashed on startup
never got far enough to clear its own marker — the stale marker IS the crash
report. Then, since Atlas still pins the bad version, without a memory of the
failure the next poll re-applies it: swap, crash, roll back, swap. A
rolled-back version is therefore never retried **until the pin moves** — the
update-loop equivalent of `KELD_ENRICH_MAX_ATTEMPTS` quarantining a job rather
than retrying it forever. Two refusals keep the recovery honest: a **failed
restart leaves the marker PENDING** on purpose (the swap already happened, so
the next start must still be able to undo it), and a **rollback that cannot
restore does NOT restart** — the machine is in an unknown state and bouncing
the service can only obscure it, so it reports at error severity and stops.
A pre-flight `keld-agent --version` runs on the staged binary **before** any
swap: a wrong-architecture build hashes correctly and cannot run, and catching
that costs milliseconds rather than a restart, a rollback and a second restart.
`Maybe` takes its decision **inline** (a pure function over already-loaded
state) and hands only the work to a single-flighted goroutine — the settings
poll carries per-org config to every other subsystem and must never block on a
190 MB download. Reporting is disk-only in `internal/localagent/update.go`,
the same rule `models.go` follows: a CLI that cannot reach the daemon does not
thereby know an update failed.
⚠️ **WHO SETS THE PIN IS AN OPEN ATLAS-SIDE QUESTION, AND `base_url` IS THE
SHARP EDGE.** **As of 2026-08-27** nothing serves `agent_release` and there is
no producer for it, so the client is inert — a date, because an open question
recorded without one reads as current forever and is indistinguishable from one
nobody revisited. Before one exists: `version` + `base_url` together
say "fetch this binary from this host and run it", and the checksum gate does
NOT constrain that — `checksums.txt` is fetched from the SAME `base_url`, so it
proves the transfer was not corrupted, never that the bytes came from Keld.
Write access to `agent_release` is therefore equivalent to root on every machine
in the org. Three ways out, cheapest first: Atlas never serves `base_url` and
the client ignores it (mirrors configured locally); a server-supplied
`base_url` is honoured only when a local env var permits that host; or release
assets are signed and verified against a key compiled into the binary — the only
one that makes the host untrusted. **None was implemented on that date.** Choose
before the first real rollout, not after, and record the choice WITH THE DATE it
was taken — here and in `docs/auto-update.md`, whose status block is dated for
the same reason. Reference: `docs/auto-update.md`. Spec:
`docs/superpowers/specs/2026-08-27-signal-auto-update-design.md`.

