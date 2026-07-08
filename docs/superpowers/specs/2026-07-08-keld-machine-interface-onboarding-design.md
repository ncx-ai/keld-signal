# `keld` machine-interface for installer-driven onboarding

**Date:** 2026-07-08
**Status:** Approved
**Sub-project A of:** "Keld Signal installer surfaces auth/setup in-installer"

## Context

The Keld Signal installer (a single package delivering **both** the `keld` CLI and
the `keld-agent` daemon + sidecar) will surface the device-authorization workflow
**inside native installer pages** — a custom Inno Setup wizard page on Windows and
a native InstallerPlugin pane on macOS. Those pages must display the verification
URL + user code live, react when the user approves in their browser, and then
configure the detected tools.

Rather than re-implement device-flow polling and tool-config editing in Pascal and
ObjC/Swift, the native pages **drive the existing `keld` binary through a
machine-readable interface**. All auth/masking/setup logic stays in one tested Go
place; the pages stay thin.

This spec covers **sub-project A only**: the Go machine-interface on `keld`. Two
later, independent efforts consume it: (B) the Windows Inno pages and (C) the macOS
InstallerPlugin. A is a prerequisite for both and is fully testable on its own.

## Goals

1. A stable, structured, non-interactive interface a controlling process can drive
   to run login and tool setup and observe progress.
2. Zero behavior change to existing interactive `keld login` / `keld signal setup`
   invocations.
3. Fix the regression merged earlier: `keld-agent install` shells out to
   *interactive* login/setup, which hangs / pops a contextless browser when a GUI
   installer runs it headless.

## Non-goals (later sub-projects)

- The Windows Inno wizard pages.
- The macOS InstallerPlugin bundle.
- Rewiring the GUI installers' `[Run]` / `postinstall` to the reduced service-only
  `keld-agent install` and to the pages.
- Any new runtime dependency (TTY detection is done with the standard library).

## Interface contract

Output is **NDJSON on stdout**: one JSON object per line, each with an `event`
string discriminator. In JSON mode stdout carries **only** events — no human text.
A non-zero exit accompanies a terminal `error` event.

### `keld login --json [--no-browser]`

Emitted in order:

```json
{"event":"device_code","verification_url":"https://atlas.keld.co/device","user_code":"WXYZ-1234","expires_in":900,"interval":5}
{"event":"authorized","principal":"dg@keld.co","org":"acme"}
```

- `device_code` is emitted immediately after the device flow starts, so the page
  can render the code without waiting for approval.
- `--no-browser` suppresses the automatic browser open (the page owns its "Open
  browser" button; it has the URL from the first event). Without it, JSON mode
  still auto-opens (parity with the human default).
- On failure (timeout, transport error): `{"event":"error","message":"…"}` and
  exit 1.

### `keld signal setup --json`

`--json` implies non-interactive: `--yes` is forced, conflicts auto-skip, no diffs
are rendered.

```json
{"event":"tool","name":"claude_code","display":"Claude Code","action":"configured","path":"/…/settings.json","backup":"/…/settings.json.20260708.bak"}
{"event":"tool","name":"codex","display":"Codex","action":"skipped_conflict","path":"/…/config.toml"}
{"event":"done","configured":1,"endpoint":"https://ingest.keld.co/…"}
```

- `action` ∈ `configured` | `already_configured` | `skipped_conflict`.
- `backup` is present only when a backup was written.
- Terminal event is `done` with the count of tools configured. On failure:
  `{"event":"error","message":"…"}` and exit 1.

## Design

### `internal/auth` — device-code reporting seam

The device-code display is currently a `console.Print` inside `Login`
(`device.go:25-28`). Extract it into an injected callback so the JSON path can emit
structured output instead:

- `Login(c, openBrowser, sleep, opener, onStart func(*api.DeviceStart))` — invokes
  `onStart(ds)` immediately after `DeviceStart()` succeeds, replacing the inline
  print.
- `RequireAuthReport(noLogin, openBrowser, force bool, onStart func(*api.DeviceStart)) (*AuthData, error)`
  — the reporting variant.
- `RequireAuth(noLogin, openBrowser, force bool)` keeps its signature and delegates
  to `RequireAuthReport` with a default `onStart` that prints the existing human
  message. All existing callers are unchanged.

The `--json` login command injects a JSON-emitting `onStart` and passes
`openBrowser = !noBrowser`. In JSON mode nothing else in `Login` writes to stdout
(the "(Opening your browser…)" line is guarded by `openBrowser`, which is off when
the page manages the browser).

### `internal/cli` — setup event sink

Add `Emit func(SetupEvent)` to `SetupOpts`. In `runSetup`:

- When `Emit == nil` (default): behavior is byte-for-byte unchanged — every
  existing `console.*` / `diffview.Render` call runs as today.
- When `Emit != nil`: those human-output calls are suppressed (guarded by
  `if opts.Emit == nil`) and `Emit` is called at the **existing** decision points —
  `configured` (`setup.go:99/150`), `already_configured` (`:88`),
  `skipped_conflict` (`:61/84`), and a final `done`.

The `--json` setup command sets `Yes: true` and wires `Emit` to an NDJSON encoder.
`Confirm` / `ResolveConflict` are not consulted in JSON mode.

Event structs (`deviceCodeEvent`, `authorizedEvent`, `toolEvent`, `doneEvent`,
`errorEvent`) and a tiny NDJSON encoder live in `internal/cli` (both commands are
package `cli`).

### `internal/agentcli` — TTY guard on `keld-agent install`

`runInstall` gains an injected `isTTY func() bool`:

- Production `isTTY`: `fi, err := os.Stdin.Stat(); return err == nil && fi.Mode()&os.ModeCharDevice != 0`. For the Windows `runhidden` / macOS GUI-session invocation stdin is not a char device, so this returns false — no new dependency.
- `isTTY()==false`: skip `keld login` / `keld signal setup`; run `service.Install()`
  only; print `Service installed. Finish setup by running: keld login && keld signal setup`.
- `isTTY()==true`: unchanged — resolve `keld`, run login, run signal setup, then
  install the service.

## Files

- `internal/auth/device.go` — `onStart` seam, `RequireAuthReport`, default reporter.
- `internal/cli/login.go` — `--json` / `--no-browser` flags + JSON path.
- `internal/cli/setup.go` — `SetupOpts.Emit`, guarded `runSetup`, `--json` flag.
- `internal/cli/onboard_events.go` (create) — event structs + NDJSON encoder.
- `internal/agentcli/agentcli.go` — `isTTY` seam in `runInstall`, production wiring.
- Corresponding `*_test.go` files.

## Testing (pure Go, hermetic)

- `auth`: `Login` with a fake `api.Client` + captured `onStart` asserts the
  `DeviceStart` values reach the callback and the `authorized` result is saved;
  poll-timeout error path.
- `cli` login `--json`: fake auth seam → assert exact NDJSON lines (`device_code`,
  `authorized`) and the `error` line + non-zero exit.
- `cli` setup `--json`: drive `runSetup` with fake adapters (configured /
  already-configured / conflict) and an `Emit` collector → assert the event
  sequence and `done` count; assert the human path is unchanged when `Emit == nil`.
- `agentcli`: extend `runInstall` tests — `isTTY=false` runs **only** service
  install (no login/setup); `isTTY=true` runs the full sequence.

## Verification

- `go test ./...` passes (minus the environment-dependent daemon tests already
  addressed).
- Manual: `keld login --json --no-browser` prints a `device_code` line immediately;
  `keld signal setup --json` prints `tool` + `done` lines; piping either through a
  JSON reader parses cleanly. `keld-agent install </dev/null` (no TTY) registers the
  service only and prints the finish-setup note.
