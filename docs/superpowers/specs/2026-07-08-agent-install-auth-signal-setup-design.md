# `keld-agent install` — run CLI auth + signal setup

**Date:** 2026-07-08
**Status:** Approved

## Problem

`keld-agent install` today only registers the OS service (`service.Install()`).
The daemon it installs is inert until two things exist under `~/.keld`:

- `auth.json` — written by `keld login` (device authorization).
- `hook.json` — written by `keld signal setup` (telemetry onboarding).

The daemon starts on `enable --now` and **refuses to run** while `hook.json` is
missing (`internal/agent/daemon/daemon.go:303-305`). Nothing in the install flow
wires those two steps in — the shell scripts and pkg installers only *print*
`keld login` / `keld signal setup` as manual next steps. Result: a fresh
`keld-agent install` leaves the user with a running-but-refusing daemon and a
manual two-command follow-up.

Goal: `keld-agent install` should get the user fully set up in one command.

## Scope

**In scope:** the `keld-agent install` cobra command
(`internal/agentcli/agentcli.go`) only.

**Out of scope (deliberately, per "just set the user up"):**

- No `--no-login` / `--skip-setup` flags.
- No skip-if-already-done idempotency checks — both steps run every install.
- No changes to `scripts/install.sh`, `scripts/install.ps1`, or the native pkg /
  Inno Setup installers under `installers/`.
- No refactor of the CLI's `login` / `signal setup` internals — they are invoked
  as-is via subprocess.

## Design

The `install` command's `RunE` changes from a single `service.Install()` call to a
short linear orchestration of four steps, aborting the install on the first
failure:

1. **Resolve the `keld` binary.** Reuse the daemon's existing "beside the
   executable" discovery pattern (`resolveSidecar` / `sidecarBinPath`,
   `internal/agent/daemon/daemon.go:233-295`): look in the same directory as the
   running `keld-agent` executable first, then fall back to
   `exec.LookPath("keld")`. If neither yields a `keld` binary, abort with a clear
   error — we cannot set the user up without it.
2. **`keld login`.** Shell out with `Stdin`/`Stdout`/`Stderr` inherited from the
   parent process, so the device-authorization browser flow and its printed
   verification code / user code work in the user's terminal.
3. **`keld signal setup`.** Shell out with inherited stdio, so tool detection,
   config diffs, and confirmation prompts work interactively.
4. **`service.Install()`.** Register and enable/start the OS service — **last**.

### Why service install is last

The daemon starts immediately on `enable --now` and refuses to run without
`hook.json`. Running login → setup → service means the daemon comes up already
authenticated and configured, rather than starting into a "not configured"
refusal that the user then has to restart past.

### Invocation mechanism

Shell out to the `keld` binary (chosen over in-process calls). Rationale: the
login device flow and `signal setup` (tool adapters, diff rendering, interactive
conflict resolution) are tangled into the `internal/cli` cobra layer;
`signal setup`'s orchestrator (`runSetup`, `internal/cli/setup.go:33`) is
unexported and interactive. Subprocessing preserves their full UX with zero
duplication, at the cost of a runtime dependency on the `keld` binary being
present beside `keld-agent` (which the shell/pkg installers already place there).

## Testability

Keep the command body thin and introduce two seams:

- `resolveKeld() (string, error)` — mirrors the sidecar resolver. Unit-testable by
  placing a fake `keld` binary beside a fake executable path and asserting it is
  found; and asserting a clear error when absent.
- A **runner seam** — a function value (e.g. `runStep func(name string, args ...string) error`)
  that the command uses to invoke `keld login` and `keld signal setup`. Production
  wires it to an `exec.Command` with inherited stdio; a test injects a fake that
  records invocations, letting a test assert the exact step sequence
  (`keld login` → `keld signal setup` → service install) without spawning
  subprocesses or touching the OS service.

## Files

- `internal/agentcli/agentcli.go` — modify the `install` command; add
  `resolveKeld` + the runner seam.
- `internal/agentcli/*_test.go` — new/updated tests for `resolveKeld` and the
  step sequence.

## Verification

- `go test ./...` passes.
- Manual: on a machine with `keld` beside `keld-agent`, `keld-agent install`
  walks through login, then setup, then brings up the service already configured.
