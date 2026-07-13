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
- **Orchestration extends the existing `keld-agent install` command** — that command's
  `runInstall` (`internal/agentcli/agentcli.go`) *already* is the single, TTY-aware Go
  orchestrator: it resolves the sibling `keld`, runs `keld login` → `keld signal setup`
  → `service.Install` (agent last). We add code-awareness to it rather than build a
  redundant parallel command. `install.sh`, `install.ps1`, and `onboard.command` all
  collapse to calling `keld-agent install [--code X]`. (Chosen over inlining the flow
  in each script — three shell copies — and over a new `keld onboard` command that
  would duplicate `runInstall`'s own login→setup.)
- **Scope = all three now**: `install.sh`, `install.ps1`, and the keld-atlas web
  install one-liner.

## Component 1 — extend `keld-agent install`

The orchestration already exists in `internal/agentcli/agentcli.go`:

```go
func runInstall(isTTY func() bool, resolveKeld func() (string, error),
                run stepRunner, installService func() error) error {
    if isTTY() {
        keld, _ := resolveKeld()
        run(keld, "login")
        run(keld, "signal", "setup")
    } else {
        fmt.Println("Service installed. Finish setup by running: keld login && keld signal setup")
    }
    return installService()   // agent last — the daemon needs hook.json first
}
```

`resolveKeld()` (agentcli.go:47) already finds the sibling `keld` (beside the running
`keld-agent`, then `$PATH`). We extend `runInstall` and the `install` cobra command —
**not** a new command — with three changes:

**(a) Code-aware signature.** `runInstall` gains a resolved `code string` and a
`yes bool` (plumbed from a new `--code` flag defaulting to `KELD_SETUP_CODE`, the flag
winning, and a `--yes` flag). New branch structure:

```go
switch {
case code != "":                       // headless-capable pre-auth path
    run(keld, "login", "--code", code)  // + --api-url when set
    run(keld, "signal", "setup", "--yes")
case isTTY():                           // human in a real terminal
    run(keld, "login")
    run(keld, "signal", "setup")        // + --yes when yes==true
default:                                // headless, no code → can't interactively log in
    fmt.Println("Service installed. Finish setup by running: keld login && keld signal setup")
}
return installService()
```

A login/setup error in the code or TTY branch aborts **before** `installService()`
(don't start a daemon that has no `hook.json`); the default branch is unchanged.

**(b) Stdout-based TTY detection (bug fix).** `stdinIsTTY` (agentcli.go:99) checks
**stdin**. Under `curl | sh` the script — and the `keld-agent` it spawns — inherit the
**pipe** as stdin, so a human in a real terminal is misread as headless. Switch the
detector to **stdout** (`term.IsTerminal(int(os.Stdout.Fd()))`, rename to
`stdoutIsTTY`); interactive device-flow login needs no stdin (it prints a URL and
polls), so a piped stdin never blocks it. The GUI-installer case (launchd/runhidden)
still reads correctly: those wire stdout away from a terminal too.

**(c) Flag/env plumbing + passthrough.** The `install` command gains
`--code` (default `os.Getenv("KELD_SETUP_CODE")`, flag wins), `--yes`, `--api-url`
(passed through to both `keld login` and `keld signal setup` when non-empty), and
`--json` (passed through to both, for installer UIs consuming NDJSON). The
`stepRunner` already inherits stdio, so child NDJSON/human output flows through.

New `install` signature call: `runInstall(code, yes, apiURL, jsonOut, stdoutIsTTY, resolveKeld, runStep, service.Install)`
(argument list widened; the `isTTY`/`resolveKeld`/`run`/`installService` seams stay
injectable for the existing table tests).

## Component 2 — installer scripts call `keld-agent install [--code X]`

**`scripts/install.sh`** (POSIX sh):
- Parse a code from `--code <X>` (via `sh -s -- --code X`; a simple positional scan, no
  getopts dependency) **or** `KELD_SETUP_CODE`; the argument wins. Export the resolved
  code as `KELD_SETUP_CODE` for the child (belt-and-suspenders; the `--code` flag is
  also passed explicitly).
- Keep download/extract/chmod and the Linux sidecar fetch (sidecar before the agent
  starts, so ML is provisioned).
- **Replace** the current `keld-agent install` invocation (which was Linux/systemctl-
  gated and ran with no code) with a single `"${DEST}/keld-agent" install [--code "$CODE"]`
  whenever `keld-agent` is present. `keld-agent install` now owns login→setup→service
  and the TTY/headless decision, so the script does **not** re-check the TTY. Drop the
  now-redundant "Next steps: run keld login / keld signal setup" echo (the command
  prints the finish-setup line itself in the headless-no-code case); keep the PATH
  guidance.

**`scripts/install.ps1`** (PowerShell): the same shape — a `-Code` param defaulting to
`$env:KELD_SETUP_CODE`; after download, call `keld-agent install [-Code $Code]`.
(The Windows `.pkg`/Inno GUI wizard is separate and unaffected.)

**`installers/macos/onboard.command`**: collapse the 4-step shell chain
(`keld login --code` || `keld login` → `keld signal setup --yes` → `keld-agent install`)
to prompt for the code, then a single `keld-agent install --code "$CODE"` (empty code →
`keld-agent install`, which is interactive in the Terminal's TTY). Removes the
shell-side fallback/ordering duplication (now owned by `keld-agent install`). Still
best-effort and re-runnable.

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
                          keld-agent install --code <CODE>
                            ├─ keld login --code <CODE> ─▶ POST /v1/cli/enroll ─▶ {token,principal,org} ─▶ auth.json
                            ├─ keld signal setup --yes ──▶ configure tools + hook ──▶ hook.json/manifest
                            └─ service.Install ──────────▶ register + start the daemon (last)
```

## Error handling

- **Bad/expired code** → `keld login --code` exits non-zero with the typed "invalid or
  expired setup code" message (piece 1); `runInstall`'s code branch returns that error
  **before** `service.Install`, so no daemon is started without `hook.json`. The
  installer surfaces it and prints how to re-run.
- **Setup partial/conflict** → the existing `keld signal setup --yes` skips conflicting
  tools (emitting `skipped_conflict`) and still succeeds; `runInstall` proceeds to
  `service.Install`.
- **`keld` binary missing** → `resolveKeld()` already returns
  "keld binary not found beside keld-agent or on PATH; install keld first"; surfaced as
  the command's error.
- **No code + non-TTY** → headless branch: print the finish-setup line, install the
  service, exit 0 (nothing failed).

## Testing

- **Go (`internal/agentcli/agentcli_test.go`, extend the existing table tests):** the
  new `runInstall` branch matrix — (i) code set → `run` receives
  `login --code <code>` then `signal setup --yes` then `installService`, regardless of
  TTY; (ii) no code + TTY → `login` then `signal setup` then `installService`; (iii) no
  code + no TTY → no login/setup, prints finish-setup, `installService` still called;
  (iv) a login error in the code/TTY branch aborts before `installService`. All via the
  injected `isTTY`/`resolveKeld`/`run`/`installService` seams (no real binary/service).
  Plus `--api-url`/`--yes`/`--json` passthrough assertions. Keep the existing four
  `TestRunInstall*` cases green (signatures widened).
- **install.sh:** `shellcheck`; assert the code parse (arg + env, arg-wins) and that
  `keld-agent install` is invoked with the resolved `--code` (the TTY/headless decision
  is `keld-agent install`'s responsibility, covered by the Go tests).
- **install.ps1:** `Invoke-ScriptAnalyzer` if available; param/env parse assertion.
- **Web:** vitest — the one-liner renders with the embedded code per OS and Copy works
  (extend `install-panel.test.tsx` / a new `install-command.test.tsx`).
- **Live pkg/GUI/Windows** = manual/CI (no macOS/Windows in the Linux dev env — stated
  honestly, as with the prior installer work).

## Non-goals / notes

- No change to `keld login`, `keld signal setup`, or the enroll wire contract —
  `keld-agent install` composes them via subprocess. Interactive `keld login`,
  `--json`, `--api-url`, `--no-browser` all unchanged. No new CLI command is added.
- The Windows `.pkg`/Inno GUI wizard and macOS `.pkg` postinstall structure are not
  reworked here; only `onboard.command`'s internals simplify. (The Inno wizard driving
  `keld --json` remains its own documented follow-on.)
- Downloader-self-setup only; fleet/org join-keys (a code enrolling many machines under
  an org rather than one downloader) remain the documented future addition.

## Decomposition (→ plans)

1. **keld-cli — extend `keld-agent install`** (`internal/agentcli/agentcli.go`:
   code-aware `runInstall` + `--code`/`--yes`/`--api-url`/`--json` flags + stdout TTY
   fix; extend `agentcli_test.go`). The foundation the scripts call.
2. **keld-cli — installer scripts** (`install.sh`, `install.ps1`, `onboard.command`)
   rewired to `keld-agent install [--code X]` (+ shellcheck/parse tests). Depends on 1.
3. **keld-atlas — web install one-liner** (`install-command.tsx` reusing
   `useEnrollCode` + tests). Independent of 1/2 (only needs the install URLs + code).

Pieces 1 + 2 are one keld-cli plan (sequential, same repo); piece 3 is a separate
keld-atlas plan.
