# Signal auto-update — design

**Date:** 2026-08-27
**Status:** approved, implementing
**Scope:** the `keld` CLI, the `keld-agent` daemon, and the frozen analysis
sidecar all move to a version Atlas names, unattended.

## The problem

A Keld install is three artifacts that must agree — `keld`, `keld-agent` and
`keld-agent-sidecar` — and today the only thing that moves them is a human
re-running an installer. Two consequences are already documented elsewhere in
this repo and are the motivation here:

- **AGENTS.md, on `ml_backend`:** "an existing fleet's Atlas Context column
  therefore empties machine-by-machine at whatever pace people upgrade, with no
  server-side brake. If that pace ever needs controlling, the control is a
  staged rollout of the installer itself." There is no such rollout, because
  there is no mechanism that reaches an installed machine at all.
- A client fix cannot be delivered. Every defect this repo has measured and
  fixed — the `prompt.id` text-gate over-match that zeroed 1,107 events'
  correlation, the pid-only supervisor kill that stranded 2.9 GB children — sits
  in a binary a human must choose to replace.

## What is being built

The daemon learns, from Atlas, which release it should be running; if that is
not the release it is, it fetches that release's assets, verifies them, swaps
them into place, restarts its own service, and confirms the new version came up
— rolling back to the previous one if it did not.

### Non-goals

- **No GitHub polling.** The version source is Atlas and only Atlas. A client
  that resolves `releases/latest` on its own converges the whole fleet the
  moment a tag is pushed, with no brake, which is the problem being solved
  rather than a smaller version of it.
- **No signed-installer re-run on macOS.** A `.pkg` re-run needs GUI
  authorization and is therefore not unattended.
- **No model download.** The 1.9 GB GLiNER2 weights are provisioned on demand
  and are not release assets; nothing here touches them.

## 1. Trigger and cadence

The updater has **no clock of its own**. It hangs off the settings poll's
existing `onRemote(r)` hook (`daemon/daemon.go`, `pollSettings`), which is
already the one place per-org configuration reaches the daemon.

Two guards on top of that:

- **`KELD_UPDATE_MIN_INTERVAL`** (default 1h) — a floor between *attempts*, so
  a fast `KELD_SETTINGS_POLL` does not mean a fast update loop.
- **Single-flight** — a check already running is not started again. The hook
  hands the work to a goroutine and returns immediately; the settings poll is
  the loop that carries per-org config to every subsystem and must never block
  on a 190 MB download.

## 2. The Atlas contract

`settings.Remote` gains one block, following the `PIIRegions` / `Features`
precedent exactly — pointer fields, so an absent key is distinguishable from an
explicit value:

```go
// Release is the org's control over which release this machine runs.
type Release struct {
    Enabled *bool   `json:"enabled"`
    Version *string `json:"version"`
    BaseURL *string `json:"base_url"`
}
```

**An absent block means no updates.** Not "default on". This matches the rule
already written for `Features`: "an OMITTED key leaves the local base rather
than defaulting on, so a silent fleet-wide enable is not reachable from the
server." Auto-update is the most consequential thing Atlas could turn on by
omission, so it gets the strictest reading of that rule.

`Version` is a **PIN, not a floor.** The daemon moves to the named version in
*either* direction. A control plane that can only move a fleet forward is not a
brake, and the brake is the entire reason the version source is Atlas rather
than GitHub. Downgrade is therefore a first-class supported operation, not an
error case.

`BaseURL` defaults to the GitHub release download path
(`https://github.com/ncx-ai/keld-signal/releases/download`) — the same host
`install.sh` uses. It exists so an air-gapped or mirrored fleet is a server
change rather than a client one.

**Local kill switch:** `KELD_AUTOUPDATE=0` refuses updates on this machine
regardless of what Atlas says. Local refusal always wins; local *permission*
never does.

## 3. Deciding whether to act

Refusals are decided before a single byte is fetched:

| Condition | Reason code |
|---|---|
| `version.CLI == "dev"` | `dev_build` — never auto-update an unreleased binary |
| Atlas block absent, or `Enabled` false/nil | `disabled` |
| `KELD_AUTOUPDATE=0` | `disabled_local` |
| No `Version` pinned | `no_target` |
| Target == running version | `up_to_date` |
| Target in the local failed set (§6) | `failed_previously` |
| Attempted within `KELD_UPDATE_MIN_INTERVAL` | `too_soon` |

Version comparison is **string equality against the pin**, not semver ordering.
Ordering is what a floor needs; a pin needs identity, and identity has no
parsing edge cases to get wrong. Tags are normalized for a leading `v` only.

## 4. Fetch and verify

Assets, per OS/arch, exactly as published today:

- `keld_<os>_<arch>.tar.gz` — contains `keld` and `keld-agent`
- `keld-agent-sidecar_<os>_<arch>.tar.gz` — the ~190 MB one-dir frozen tree

Both are verified by SHA-256 against `checksums.txt`, falling back to the
per-file `<asset>.sha256` that CI publishes for the separately-built sidecar.

**A missing published hash is FATAL here**, and that is a deliberate divergence
from `install.sh`, which warns and continues. The installer has a human reading
its output who can abort; an unattended swap does not, so the one case where
`install.sh` degrades gracefully is the case this must refuse.

Downloads go through `internal/retry` (`retry.Do` + `IsTransient`) rather than a
hand-rolled backoff, per the repo convention.

**Staging lives inside the destination directory** (`.keld-update.XXXX`), for
the reason `install.sh` states: the commit is then a same-filesystem rename
rather than a cross-device copy of a 15,000-file tree. A staging dir that
cannot be created in the destination is a refusal, not a fallback to `/tmp` —
falling back would trade an atomic rename for a copy that can fail halfway.

## 5. The swap

Destinations are resolved from the running process, never guessed:

- binaries → `filepath.Dir(os.Executable())`
- sidecar → the directory `sidecarBinPath()` resolved, preserving whichever of
  the flat or nested layouts is already on disk

A write probe (create + remove a temp file) chooses between two paths.

### 5a. Writable destination (curl install, Windows Inno)

Displace each target to `<name>.prev` by rename, then move the staged file in.
Rename over a running binary keeps the live process's inode on Unix. On Windows
a running `.exe` cannot be *overwritten* but can be *renamed*, which is why the
order is displace-then-place and not place-over.

The sidecar tree is swapped first, the binaries second, so the shortest window
in which the two disagree is at the end and is closed by the restart.

### 5b. Non-writable destination — the macOS `.pkg`

The pkg stages to `/usr/local/keld` (root-owned) and its `postinstall`
additionally creates root-owned symlinks `/usr/local/bin/{keld,keld-agent}` and
**repoints any `~/.local/bin/keld` to a symlink back at the root copy**.

So on such a machine the daemon migrates: the new release installs into
`~/.local/bin` and the LaunchAgent plist is rewritten to name that path.

Two consequences are implemented rather than hoped away:

1. `~/.local/bin/keld` may already exist as a **root-owned symlink**. It is
   unlinked and replaced with a real file. This works because the containing
   directory is user-owned — deletion is governed by the directory, not the
   link.
2. `/usr/local/bin/keld` **cannot be rewritten** and still points at the stale
   binary, and `/usr/local/bin` typically precedes `~/.local/bin` on PATH. The
   daemon converges; the CLI a human types does not. `keld signal doctor`
   reports the stale symlink with the exact `ln -sf` fix rather than the install
   silently disagreeing with itself.

## 6. Confirm, and roll back

**Pre-flight:** the staged `keld-agent` is executed with `--version` before any
swap. This costs milliseconds and catches a wrong-architecture or
correctly-hashed-but-unrunnable payload while the machine is still healthy.

**State:** `~/.keld/update/state.json`, mode 0600:

```json
{
  "from": "v0.4.1", "to": "v0.4.2",
  "install_dir": "/home/u/.local/bin",
  "prev": ["/home/u/.local/bin/keld.prev", "..."],
  "pending_confirm": true,
  "attempted_at": "2026-08-27T10:00:00Z",
  "failed_versions": ["v0.4.0"]
}
```

Then `service.Restart()`.

**The new daemon's startup pass** reads that file before anything else starts:

- `pending_confirm` and `version.CLI == to` → the new binary is running. Clear
  the marker, delete the `.prev` copies, emit `update.applied`.
- `pending_confirm` and `version.CLI == from` → the restart did not take the new
  binary. Restore from `.prev`, record `to` in `failed_versions`, restart.
- `pending_confirm` and `attempted_at` older than `KELD_UPDATE_CONFIRM_DEADLINE`
  (default 15m) → same rollback. This is the case where the new binary crashed
  before it could reach this code at all: nothing cleared the marker, so the
  *next* start — whichever binary the service manager brings up — sees a stale
  pending marker and undoes the swap.

**`failed_versions` is load-bearing and is an addition beyond auto-rollback
alone.** Atlas still pins the bad version, so without it the next poll re-applies
it: swap, crash, roll back, swap. A version that has rolled back is never
retried until the pin moves. This is the update-loop equivalent of
`KELD_ENRICH_MAX_ATTEMPTS` quarantining a job rather than retrying it forever.

**Quiescence:** the restart waits, bounded, for the enrichment queue to drain.
The spool makes a mid-flight restart survivable, not free.

## 7. Reporting

Client-events (`internal/agent/clientevents`), documented in
`docs/signal-client-events.md`:

`update.available` · `update.staged` · `update.applied` · `update.rolled_back` ·
`update.failed` · `update.skipped` (carrying the reason code from §3)

`keld signal status` / `doctor` report the running version, the Atlas target,
the last attempt and its outcome, and any stale root-owned symlink from §5b.
Neither command triggers an update — the same rule
`internal/localagent/models.go` already follows for model presence.

## 8. Testing

Every OS-specific and network-specific dependency is injected, so the failure
modes are exercisable on Linux CI:

- an HTTP client (`httptest` server serving assets, hashes, errors, hangs)
- a `Restarter` interface
- a clock
- an exec probe
- a root directory, so `/usr/local/keld`-shaped layouts and root-owned symlinks
  are synthesized under `t.TempDir()`

Required coverage, unhappy paths first:

**Fetch:** checksum mismatch · truncated body · missing published hash (fatal) ·
404 · 5xx then success · 5xx exhausted · timeout mid-transfer · malformed
`checksums.txt` · asset that is not a valid tarball.

**Filesystem:** non-writable destination · non-writable staging · staging dir
creation refused · partially-extracted sidecar tree · rename failure mid-swap,
with `.prev` restoring the machine · `.prev` restore itself failing (the
worst case: report loudly, do not restart).

**Decision:** dev build · disabled · absent Atlas block · `enabled:false` ·
no version pinned · target == current · downgrade pin · failed-version set ·
min-interval floor · malformed payload · two concurrent triggers (single-flight).

**Confirm/rollback:** happy confirm · wrong-version-running rollback · stale
marker deadline rollback · rollback loop guard · `service.Restart()` failure ·
corrupt `state.json`.

**macOS shapes:** root-owned symlink at `~/.local/bin/keld` replaced by a real
file · plist rewritten to the migrated path · stale `/usr/local/bin` symlink
detected and reported.
