# Windows installer onboarding — Inno custom wizard page

**Date:** 2026-07-09
**Status:** Approved
**Sub-project B of:** "Keld Signal installer surfaces auth/setup in-installer"
**Depends on:** sub-project A (`keld --json` machine interface — shipped).

## Context

`keld-setup.exe` (Inno Setup) installs the client and registers the logon-task
agent, but leaves auth + telemetry setup manual. This sub-project embeds the
device-authorization + tool-setup flow directly in the installer wizard as a custom
post-install page that drives `keld login --json` / `keld signal setup --json`.

Unlike macOS (where we shipped a standalone app because `.pkg` can't host live UI),
Inno Setup natively supports custom wizard pages, so the flow lives entirely in the
existing `.iss` `[Code]` section — no new binary or toolchain.

## Goals

1. After files install, the wizard walks the user through login + tool setup with a
   native page (code shown live, browser opened, per-tool progress).
2. Reuse the tested `keld --json` contract; keep Pascal minimal and defensive.
3. Best-effort: the page never blocks the install; the service is registered
   regardless (via the existing headless `keld-agent install`).

## Non-goals

- macOS (sub-project C — shipped) / Linux.
- Any change to the `keld --json` contract.
- Code signing (SmartScreen) — a separate follow-up already noted in the README.

## Design

### Wizard structure

- `[Setup]/[Files]/[Registry]` unchanged.
- `[Run]` keeps `keld-agent.exe install` (`runhidden nowait postinstall`): headless,
  so the shipped TTY guard registers the service only (no hung login). This runs as
  part of installation, so the service is registered before/independent of the
  onboarding page.
- A custom page created with `CreateCustomPage(wpInstalling, 'Set up Keld', …)` —
  appears after files are copied (so `keld.exe` exists) and before Finished.

### Onboarding state machine (Pascal `[Code]`)

Driven by a WinAPI timer (`SetTimer` + Inno `CreateCallback`) so the wizard UI stays
responsive (no blocking loop):

- On `CurPageChanged(onboardPage)`: start **auth** — `ShellExec` (async, `ewNoWait`)
  a `cmd /c "keld login --json --no-browser > {tmp}\keld_login.ndjson"`, start the
  timer.
- Timer tick: read the temp file, parse any new NDJSON lines:
  - `device_code` → show `user_code` prominently; enable **Open browser**
    (`ShellExec('open', verification_url,…)`).
  - `authorized` → stop reading login; start **setup**:
    `cmd /c "keld signal setup --json > {tmp}\keld_setup.ndjson"`.
  - `tool` → append `display — action` to a progress memo.
  - `done` → success state; kill the timer; enable Next.
  - `error` → show message; offer **Retry** (restart the current phase).
- Guards: an overall timeout (e.g. ~5 min) → failure state with the manual
  fallback text; a **Skip** button that kills the timer and lets the user continue.
- On failure/skip the page shows: "Finish setup later by running: `keld login` then
  `keld signal setup`." The install always completes; the service is already
  registered.

### Parsing helpers (no JSON lib in Pascal; output is controlled)

- `GetEventKind(line): string` — value of `"event"`.
- `GetJsonStr(line, key): string` — value of a string field via `Pos`/`Copy`.
- `GetJsonInt(line, key): Integer` — for `configured`.
- Line tracking: remember how many lines already consumed; parse only new ones each
  tick (the file grows append-only).

### Robustness

- Every `ShellExec`/file read is guarded; a missing/locked temp file is a no-op tick.
- Unknown event kinds are ignored (forward-compatible).
- The timer is always killed on page-leave / wizard close to avoid dangling callbacks.

## Files

- `installers/windows/keld-agent.iss` — add `[Code]` onboarding page + timer + parse
  helpers (keep existing sections).
- `README.md`, `AGENTS.md` — note the Windows onboarding page.

## Verification

**Constraint: no Inno/`iscc` on the Linux dev box.**

- Local: structural inspection of the `.iss` — sections balanced, every referenced
  identifier defined, `[Code]` self-consistent. (No compile possible here.)
- CI: `installers.yml` Windows job runs `iscc` (incl. the `workflow_dispatch` dry
  run via `make release-dry`), so a **compile** error fails CI.
- Human: run `keld-setup.exe` on Windows — confirm the page shows the code, opens
  the browser, completes setup, handles skip/retry, and that the service registers
  regardless. Not claimable from this environment.

**Known blind-authoring risk:** the timer/`CreateCallback` + async file-polling
pattern is the riskiest part; CI catches compile errors, not runtime behavior.
