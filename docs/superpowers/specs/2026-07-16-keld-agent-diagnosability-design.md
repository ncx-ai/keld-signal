# Make keld-agent daemon failures diagnosable

**Date:** 2026-07-16
**Status:** Approved (design)
**Scope:** Two independent bug fixes, one spec, one PR (two commits).

## Problem

A misconfigured `keld-agent` dies with exit code 1, empty stdout, and empty
stderr — a silent death. Under launchd it is worse: the job flaps invisibly
(observed `runs = 706`, `last exit code = 1`) with no log output anywhere,
because the install plist declares no log destinations.

This was hit in practice: a stale CLI token meant `keld signal setup` never
wrote `~/.keld/hook.json`, so `daemon.Run` returned "not configured" — but the
message was swallowed and the operator saw only exit 1 with no output.

There are two independent root causes:

1. **Errors are silenced.** `agentcli.Execute()` maps any returned error to
   exit 1 but never prints it, and the root command sets `SilenceErrors: true`,
   so cobra does not print it either. (`hook.LoadConfig` also returns a `nil`
   error on a missing file, so even the `log.Printf` in `daemon.Run` never
   fires — hence *completely* empty stderr.)
2. **launchd has nowhere to log.** `LaunchAgentPlist()` omits
   `StandardOutPath`/`StandardErrorPath`, so a crash-looping daemon's output
   goes nowhere.

The two fixes are complementary but independently correct: Fix 1 makes the
message exist; Fix 2 gives it a destination under launchd.

## Fix 1 — Surface the error (visibility)

**File:** `internal/agentcli/agentcli.go`

**Root cause:** `Execute()` (agentcli.go:266) returns exit 1 without printing
the error; `SilenceErrors: true` (agentcli.go:183) stops cobra from printing it.

**Change:** print the returned error to stderr in `Execute()`:

```go
func Execute() int {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
```

This fixes the whole class of silent exit-1 across every subcommand. We keep
`SilenceUsage: true` and `SilenceErrors: true` so operational errors do not dump
cobra usage text; we simply print the error ourselves, once, to stderr.

**Unconfigured behavior:** unchanged in shape — when `cfg.Endpoint == "" ||
cfg.IngestToken == ""`, `daemon.Run` logs a clear message and returns the
existing error `"keld-agent: not configured (run \`keld login\` / setup
first)"`, which now reaches stderr. The daemon does **not** stay up and poll for
config; it exits and the operator re-runs setup + restart. (Self-healing poll
was considered and rejected as larger scope.)

**Visibility interaction:** in the foreground (`keld-agent run`) the message now
shows immediately. Under launchd it becomes visible only because of Fix 2, which
gives stderr a file destination.

## Fix 2 — Give launchd a place to log (macOS)

**Files:** `internal/paths/paths.go`, `internal/agent/service/service.go`,
`internal/agent/service/service_darwin.go`

**Root cause:** `LaunchAgentPlist()` (service.go:10) omits `StandardOutPath` and
`StandardErrorPath`, so launchd discards the daemon's output.

### 2a. New paths helpers

Add to `internal/paths/paths.go`, alongside `KeldHome()` and `DebugLogPath()`:

- `AgentLogDir()` → `~/.keld/logs`
- `AgentStdoutLog()` → `~/.keld/logs/agent.out.log`
- `AgentStderrLog()` → `~/.keld/logs/agent.err.log`

### 2b. Plist template

`LaunchAgentPlist(execPath)` gains the two keys, sourced from `paths`:

```xml
<key>StandardOutPath</key><string>~/.keld/logs/agent.out.log</string>
<key>StandardErrorPath</key><string>~/.keld/logs/agent.err.log</string>
```

(Absolute paths — launchd does not expand `~`; the code substitutes the real
home via the `paths` helpers.)

`Install()` (service_darwin.go) does `os.MkdirAll(paths.AgentLogDir(), 0o755)`
before writing the plist.

### 2c. Rollout to existing installs

Already-installed agents carry the old plist (no log keys). `Start()` and
`Restart()` currently only `kickstart`, which does not adopt a changed plist.

**Change:** make `Start()` and `Restart()` plist-aware:

1. Read the on-disk plist at `plistPath()`.
2. Compare to `LaunchAgentPlist(exe)` for the current executable.
3. If they differ (or the file is missing), rewrite it and reload via
   `launchctl bootout` + `bootstrap` (which actually adopts a changed plist),
   then `kickstart` as before.
4. If identical, behavior is unchanged (bare `kickstart` / `kickstart -k`).

Result: an already-installed agent picks up the log paths the next time the
operator runs `keld-agent restart` — no manual reinstall required.

### Scope

- **darwin only.** Linux runs under a systemd user unit, so stdout/stderr
  already land in the journal (`journalctl --user -u keld-agent`) — not
  affected. Windows uses a logon scheduled task with no stdout capture; that is
  an **explicit out-of-scope follow-up**, not addressed here.
- **No log rotation.** launchd does not rotate these files, so `agent.err.log`
  can grow unbounded across a crash-loop. Left out deliberately to keep the fix
  small; rotation is a separate future change. Documented as a known risk.

## Testing (TDD — tests written before implementation)

- **`internal/agentcli`** — `Execute()` prints a returned error to stderr and
  returns 1; the success path stays silent and returns 0. (Inject a root
  command whose `RunE` returns a known error; capture stderr.)
- **`internal/agent/service` (darwin)** — `LaunchAgentPlist()` output contains
  both log-path keys pointing at the `paths.AgentStdoutLog()` /
  `AgentStderrLog()` values; a stale-plist fixture triggers rewrite + reload in
  `Start()`/`Restart()`, while an identical plist does not (assert via a seam
  over the plist read/compare rather than shelling out to `launchctl`).
- **`internal/paths`** — new helpers return the expected `~/.keld/logs/...`
  values relative to `KeldHome()`.

## Verification (drive the real flow on macOS)

1. Temporarily move `~/.keld/hook.json` aside to reproduce the unconfigured
   state.
2. `keld-agent run` — confirm it now prints
   `keld-agent: not configured (...)` to stderr and exits 1 (previously silent).
3. `keld-agent restart` — confirm the plist is rewritten with log paths and that
   `~/.keld/logs/agent.err.log` now contains the message.
4. Restore `hook.json`, restart, confirm the daemon comes up healthy
   (`listening on 127.0.0.1:<port>`) and no longer flaps.

## Delivery

- One branch, **two commits** (Fix 1, then Fix 2).
- Single PR: "make daemon failures diagnosable."
- The two GitHub issues drafted during the debugging session (silent exit-1;
  plist has no log paths) can be filed and referenced/closed by this PR.
  Filing the issues is left to the maintainer and is not part of this
  implementation plan.
