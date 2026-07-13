# Design: token-aware CLI installers via a `keld onboard` command

**Date:** 2026-07-13
**Status:** design (approved), pending spec review
**Repos:** keld-cli (`keld onboard` command + `install.sh`/`install.ps1`/`onboard.command`)
+ keld-atlas (`services/web` install one-liner)

## Problem

The pre-authenticated onboarding feature (one-time setup code — see
`2026-07-13-signal-preauth-onboarding-design.md`) is wired into the **macOS `.pkg`**
path only (`installers/macos/onboard.command` runs `keld login --code` →
`keld signal setup --yes` → `keld-agent install`). The `curl | sh` (`install.sh`,
Linux/macOS-CLI) and `irm | iex` (`install.ps1`, Windows) installers do **not**
incorporate the code at all: `install.sh` today downloads the binaries, runs
`keld-agent install` **immediately** (before any login), then merely prints
"Next steps: `keld login` / `keld signal setup`". Two gaps:

1. **No pre-auth**: the pasteable setup code can't flow into the shell installers.
2. **Agent-before-login**: the agent is registered before onboarding, contrary to
   the design principle that onboarding precedes starting the background agent.

We want the shell installers to onboard the user directly — login (via code when
present, interactive device flow otherwise), `signal setup`, then start the agent
**last** — matching the macOS pkg flow, without triplicating that flow in shell.

## Decisions (locked via brainstorm)

- **Code input = both** an env var `KELD_SETUP_CODE` **and** a `--code` argument
  (`sh -s -- --code X` / PowerShell `-Code`); the explicit argument wins over the
  env var. (`curl | sh` makes positional args awkward, so the env var is the primary
  ergonomic path; the arg is offered for explicitness/parity.)
- **The installer onboards *for* the user** in both cases: with a code →
  `login --code` (headless); without a code → interactive `keld login` (device flow)
  → `keld signal setup --yes` → start the agent. Code vs no-code differ **only** in
  the login step.
- **Orchestration lives in a new Go command `keld onboard`** — a single, TTY-aware,
  unit-testable source of truth. `install.sh`, `install.ps1`, and `onboard.command`
  all collapse to calling it. (Chosen over inlining the flow in each script, which
  would create three shell copies of the fallback + TTY logic and drift against the
  codebase mandate to keep auth/setup logic Go-side.)
- **Scope = all three now**: `install.sh`, `install.ps1`, and the keld-atlas web
  install one-liner.

## Component 1 — `keld onboard` (new CLI command)

New `internal/cli/onboard.go`, registered on the root command
(`root.AddCommand(newOnboardCmd())` in `root.go`). It is a thin orchestrator —
it **composes** existing logic and reimplements none of it.

```
keld onboard [--code <CODE>] [--api-url <URL>] [--yes] [--no-browser] [--json]
```

Flags: `--code` (default from `KELD_SETUP_CODE` if the flag is empty; the flag wins);
`--api-url`, `--no-browser`, `--json` (same meanings as `keld login`); `--yes`
(passed to setup; `--json` implies it). 

Sequence:

1. **Login.** If a code is resolved (flag or env) → `auth.LoginWithCode(client, code)`
   (piece 1). Else → the existing interactive device-flow `Login` via its `onStart`
   seam. Honors `--api-url`/`--no-browser`; under `--json` emits the login events
   already defined for `keld login --json`.
2. **Setup.** Invoke the existing setup path (the `runSetup` seam in `setup.go`) with
   `--yes` semantics (non-interactive) and, under `--json`, `SetupOpts.Emit` wired to
   the machine event stream — i.e. exactly what `keld signal setup --yes [--json]`
   does today, called in-process.
3. **Agent (last).** Resolve the sibling `keld-agent` binary — first next to
   `os.Executable()`'s directory, then `$PATH` (the same "resolve a sibling binary"
   shape the daemon uses to find the sidecar). Run `keld-agent install`. If the
   binary is not found or the install fails, print the exact command to finish
   manually and continue (non-fatal) — the login+setup already succeeded.

**TTY-awareness** (reuse the existing `term.IsTerminal` guard, per AGENTS.md — not
`os.ModeCharDevice`). Detection keys on **stdout (fd 1)**, *not* stdin: under
`curl | sh` the script — and the `keld` it spawns — inherit the **pipe** as stdin, so
`term.IsTerminal(stdin)` is false even for a human at a real terminal; stdout is still
the terminal. (Interactive device-flow login needs no stdin — it prints a URL and
polls — so a piped stdin does not block it.)

- **Code present** → every step is non-interactive; run the full flow headless.
- **No code + stdout is a TTY** → interactive login (device flow) → setup → agent.
  This is the human `curl | sh` in a real terminal.
- **No code + stdout not a TTY** (e.g. CI piping `curl | sh` to a log) → **skip**
  onboarding (interactive login cannot proceed), print the next-steps block, and exit
  0. Never hang waiting on a browser that no one will open. `onboard` owns this
  decision and the next-steps output, so the installer scripts do not re-check the TTY.

`--json` emits NDJSON on stdout using the existing onboarding event scaffolding
(`onboard_events.go`) so an installer UI can render progress; human mode prints the
current human text.

## Component 2 — installer scripts call `keld onboard`

**`scripts/install.sh`** (POSIX sh):
- Parse a code from `--code <X>` (via `sh -s -- --code X`; simple positional scan, no
  getopts dependency) **or** `KELD_SETUP_CODE`; the argument wins.
- Keep download/extract/chmod and the Linux sidecar fetch (sidecar before onboard, so
  ML is provisioned by the time the agent starts).
- **Remove** the current early `keld-agent install` block.
- Then **always** run `"${DEST}/keld" onboard [--code "$CODE"]` (agent-start happens
  last, inside `onboard`). The script does **not** re-check the TTY — `onboard` owns
  the no-code/non-TTY skip and prints next-steps itself (back-compat for automated/CI
  installs). The script's own trailing message is limited to PATH guidance.

**`scripts/install.ps1`** (PowerShell): the same shape — a `-Code` param defaulting to
`$env:KELD_SETUP_CODE`; after download, always call `keld onboard [-Code]` (which owns
the no-code/non-interactive skip, same as install.sh). (Windows service pre-registration behavior noted in
AGENTS.md is unaffected by this script; the `.pkg`/Inno GUI paths are separate.)

**`installers/macos/onboard.command`**: collapse the 4-step shell chain
(`login --code` || `login` → `signal setup --yes` → `keld-agent install`) to prompt
for the code, then a single `keld onboard [--code "$CODE"]` — removing the shell-side
fallback/ordering duplication (now owned by the Go command). Still best-effort and
re-runnable.

## Component 3 — Atlas web install one-liner (keld-atlas `services/web`)

On the install panel (`components/signal/install-panel.tsx`), beside the existing
setup-code block, add a copyable **install command** with the code embedded, per OS:

- macOS / Linux: `KELD_SETUP_CODE=<CODE> curl -fsSL <install.sh URL> | sh`
- Windows: `$env:KELD_SETUP_CODE="<CODE>"; irm <install.ps1 URL> | iex`

A small `install-command.tsx` block consuming the **same** `useEnrollCode` hook (no
new API surface); OS chosen via a simple tab/toggle (or navigator hint), `keld-*`
tokens, a Copy button. Copy: "Run this in a terminal — it installs Keld and signs this
machine in as you."

## Data flow

```
download page (authed)                  installer                         Atlas
  useEnrollCode ── code ──▶ shown in one-liner
                                   │ user copies + runs
                                   ▼
                        install.sh / install.ps1
                          download keld + keld-agent (+ sidecar)
                          keld onboard --code <CODE>
                            ├─ auth.LoginWithCode ──▶ POST /v1/cli/enroll ──▶ {token,principal,org}  ──▶ auth.json
                            ├─ signal setup --yes ──▶ configure tools + hook ──▶ hook.json/manifest
                            └─ keld-agent install ──▶ register + start the daemon
```

## Error handling

- **Bad/expired code** → `LoginWithCode` returns the typed "invalid or expired setup
  code" error; `onboard` surfaces it and, in a TTY, falls back to interactive login;
  headless, it exits non-zero with that message (installer prints how to re-run).
- **Setup partial/conflict** → the existing setup emits per-tool
  `configured`/`already_configured`/`skipped_conflict`; `onboard` does not abort the
  agent step on a skipped tool.
- **`keld-agent` missing** → non-fatal; print the manual `keld-agent install` command.
- **No code + non-TTY** → skip onboarding, print next steps, exit 0 (nothing failed).

## Testing

- **Go (`onboard_test.go`):** code→`LoginWithCode` vs interactive dispatch (assert the
  right auth path per `--code`/env, arg-wins); the agent-binary resolver (found beside
  the exe / found on PATH / missing → prints instructions, non-fatal); the TTY-guard
  branch (no code + non-TTY → skip signal); `--json` event stream shape. Auth calls hit
  an `httptest` stub (mirror `login_test.go`); the `keld-agent` exec is stubbed via an
  injected runner seam so no real binary/service is touched.
- **install.sh:** `shellcheck`; assert the code parse (arg + env, arg-wins) and that
  `keld onboard` is invoked with the resolved code (the no-code/non-TTY skip is
  `onboard`'s responsibility, covered by the Go tests — the script always calls it).
- **install.ps1:** `Invoke-ScriptAnalyzer` if available; param/env parse assertion.
- **Web:** vitest — the one-liner renders with the embedded code per OS and Copy works
  (extend `install-panel.test.tsx` / a new `install-command.test.tsx`).
- **Live pkg/GUI/Windows** = manual/CI (no macOS/Windows in the Linux dev env — stated
  honestly, as with the prior installer work).

## Non-goals / notes

- No change to `keld login`, `keld signal setup`, or the enroll wire contract —
  `onboard` composes them. Interactive `keld login`, `--json`, `--api-url`,
  `--no-browser` all unchanged.
- The Windows `.pkg`/Inno GUI wizard and macOS `.pkg` postinstall structure are not
  reworked here; only `onboard.command`'s internals simplify. (The Inno wizard driving
  `keld --json` remains its own documented follow-on.)
- Downloader-self-setup only; fleet/org join-keys (a code enrolling many machines under
  an org rather than one downloader) remain the documented future addition.

## Decomposition (→ plans)

1. **keld-cli — `keld onboard` command** (`internal/cli/onboard.go` + agent resolver +
   tests). The foundation the scripts call.
2. **keld-cli — installer scripts** (`install.sh`, `install.ps1`, `onboard.command`)
   rewired to `keld onboard` (+ shellcheck/parse tests). Depends on 1.
3. **keld-atlas — web install one-liner** (`install-command.tsx` reusing
   `useEnrollCode` + tests). Independent of 1/2 (only needs the install URLs + code).
