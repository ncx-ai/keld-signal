# Design: Windows Inno onboarding wizard (setup-code, synchronous)

**Date:** 2026-07-13
**Status:** design (approved), pending spec review
**Repo:** keld-cli — `installers/windows/keld-agent.iss` (+ AGENTS.md note)

## Problem

The Windows Inno Setup installer (`installers/windows/keld-agent.iss`) installs the
binaries and, in `[Run]`, fires `keld-agent.exe install` **headless**
(`runhidden nowait postinstall`). With the new onboarding model that headless call
hits the no-code/no-TTY branch: it registers the per-user logon task (schtasks) but
does **no** login or `signal setup`. So a Windows user finishes the installer with the
agent registered but not signed in — they must discover and run `keld login` +
`keld signal setup` themselves. There is no in-installer onboarding. macOS (via the
`.pkg` → `onboard.command`) and the `curl|sh`/`irm|iex` scripts already onboard with
the one-time setup code; Windows should match.

## Decision (locked via brainstorm)

- **Add one custom wizard page** ("Set up Keld") with a single **setup-code** field,
  then run `keld-agent install --code <CODE>` **synchronously** after file copy. The
  setup-code path is fast (a couple of HTTP round-trips), so no async NDJSON progress
  polling is needed — an exit-code check suffices. This keeps the Inno Pascal minimal
  and robust, which matters because it **cannot be compiled or run on the Linux dev
  host** (no Windows, no `iscc`); it is verified on Windows CI + manually.
- **No in-wizard browser device-flow / live NDJSON progress** (the async alternative).
  Browser sign-in stays a post-install terminal step when the user provides no code.
- **On a failed or empty code, still register the service** (run a plain
  `keld-agent install`) so onboarding failure never leaves the agent unregistered.
- **No changes to `keld`/`keld-agent` Go code** — the wizard only *drives* the existing
  `keld-agent install --code` (built in the installer-onboarding work).

## Components (all in `installers/windows/keld-agent.iss`)

1. **Custom input page.** In `InitializeWizard`, create a page after `wpReady` with
   `CreateInputQueryPage(wpReady, 'Set up Keld', 'Sign this machine in to Keld',
   'Paste the setup code from your Keld download page. It signs this machine in as
   you. Leave blank to sign in with a browser after install.')` and one field via
   `.Add('Setup code:', False)`. Store the page handle in a module-level
   `SetupCodePage: TInputQueryWizardPage`.

2. **Post-install onboarding** in `CurStepChanged(CurStep: TSetupStep)` on
   `ssPostInstall`:
   - Read `Code := Trim(SetupCodePage.Values[0])`.
   - `Agent := ExpandConstant('{app}\keld-agent.exe')`.
   - If `Code <> ''`: `Exec(Agent, 'install --code ' + Code, '', SW_HIDE,
     ewWaitUntilTerminated, ResultCode)`. If `Exec` returns False or `ResultCode <> 0`
     → `MsgBox('Setup code didn''t work. Keld is installed; finish by running "keld
     login" then "keld signal setup" in a terminal.', mbInformation, MB_OK)` **and**
     then run a plain `Exec(Agent, 'install', …)` so the service is still registered
     (`OnboardedWithCode := False`).
   - Else (empty code): `Exec(Agent, 'install', …)` (headless branch → registers the
     service only); `OnboardedWithCode := False`.
   - On the code path succeeding, set `OnboardedWithCode := True`.
   - Wrap the Exec calls with an hourglass cursor
     (`WizardForm.…` is unnecessary; a brief hidden wait is fine).

3. **Remove the `[Run]` postinstall entry.** The current
   `Filename: "{app}\keld-agent.exe"; Parameters: "install"; Flags: runhidden nowait
   postinstall` line is deleted — its work now happens in `CurStepChanged`, and
   leaving it would double-register the service and race the wizard.

4. **Finished-page message.** In `CurPageChanged(CurPageID)` on `wpFinished` (or via a
   set-once field), set the finished text to reflect the outcome:
   - `OnboardedWithCode = True` → "Keld is set up and running."
   - otherwise → "Keld is installed. Finish signing in by running: keld login  then
     keld signal setup". (Use `WizardForm.FinishedLabel.Caption := …`.)

## Data flow

```
custom page: user types code
        → (Next) stored in SetupCodePage.Values[0]
        → Inno copies files ([Files])
        → CurStepChanged(ssPostInstall):
              code<>'' → keld-agent install --code CODE  (login + signal setup --yes + service)
                          fail → MsgBox + keld-agent install (service only)
              code=''  → keld-agent install              (service only)
        → CurPageChanged(wpFinished): message reflects OnboardedWithCode
```

## Error handling

- **Bad/expired/empty code** → the machine always ends with binaries installed + the
  service registered (the fallback/empty path runs plain `install`); the user is told
  how to finish sign-in. Onboarding failure never aborts the install.
- **`Exec` fails / `keld-agent.exe` missing** (should not happen — it is in `[Files]`)
  → treated like a failed code: MsgBox + best-effort plain `install`.
- **`signal setup` conflicts** are handled inside `keld-agent install` → `keld signal
  setup --yes` (skips conflicting tools, still exits 0), so the wizard sees success.

## Security / privacy

- The setup code is a single-use, short-TTL, org+principal-bound credential (same as
  everywhere else). It is passed as a child-process argument to `keld-agent.exe`
  (visible transiently to local process listing — the pre-existing property of
  `--code`, unchanged). **Not** written to the registry, logs, or the install log by
  our code. No token/prompt text is ever surfaced by the wizard.

## Testing / verification (honest limits)

- **Cannot be built or run on the Linux dev host** — no Windows, no `iscc`, no way to
  execute Inno Pascal. Linux-side verification is limited to an eyeball syntax review
  against the Inno Setup docs (matching brace/`begin`/`end`, correct pascal-string
  quote-doubling, valid event-function signatures `InitializeWizard`,
  `CurStepChanged(CurStep: TSetupStep)`, `CurPageChanged(CurPageID: Integer)`).
- **Real verification is on Windows** (as AGENTS.md already states for this file):
  the CI runner compiles with `iscc`; a human runs the installer and confirms:
  1. the "Set up Keld" page appears after the Ready page;
  2. a valid code → agent signed in + running (no terminal step needed);
  3. a blank code → service registered, Finished page shows the manual sign-in steps;
  4. a bad code → MsgBox, then service still registered, Finished shows manual steps;
  5. no double-registration (the `[Run]` line is gone).
- **Update AGENTS.md**: change the Windows-onboarding note from the aspirational
  wording to describe the shipped setup-code page (and that headless pre-register is
  now folded into the wizard's `CurStepChanged`, no longer a separate `[Run]`).

## Non-goals

- No in-wizard browser device-flow, no live NDJSON progress UI (the async variant).
- No Go code changes. No change to the macOS/`curl|sh` installers (already done).
- Not reworking the License/Info/PATH pages or the `[Files]`/`[Registry]` layout.

## Decomposition

Single, small, self-contained change to one file (+ an AGENTS.md doc line) → **one
implementation plan, one task.**
