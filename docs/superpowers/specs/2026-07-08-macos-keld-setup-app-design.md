# macOS "Keld Setup" onboarding app

**Date:** 2026-07-08
**Status:** Approved
**Sub-project C of:** "Keld Signal installer surfaces auth/setup in-installer"
**Depends on:** sub-project A (the `keld --json` machine interface — shipped).

## Context

The macOS `.pkg` installs the whole Keld Signal client (binaries + sidecar) and
registers the per-user agent, but leaves auth + telemetry setup as a manual
follow-up. This sub-project adds a small native macOS app, launched automatically
by the installer, that walks the user through device-authorization login and tool
setup by driving the `keld login --json` / `keld signal setup --json` interface.

**Delivery decision:** a **standalone auto-launched app**, not an Installer.app
pane plugin. It owns its own process (can poll and open the browser freely) and is
far less fragile than the `InstallerPlugins` route — important because none of this
can be built or tested on the Linux dev box (see Verification).

## Goals

1. After the `.pkg` installs, the user is walked through login + tool setup by a
   native macOS UI with no terminal.
2. Reuse the tested Go `--json` contract; keep the Swift thin and defensive.
3. Best-effort: the UI never blocks or breaks the install; service registration is
   independent and always happens.

## Non-goals

- Windows (sub-project B) — separate effort.
- Any change to the `keld --json` contract (shipped, frozen for this work).
- An Installer.app pane plugin.

## Design

### The app — `KeldSetup.app` (SwiftUI, macOS 13+)

A single-window SwiftUI app driving an onboarding state machine:

- **Auth.** Spawn `keld login --json --no-browser` (`Foundation.Process`), read
  stdout line-by-line, JSON-decode each line.
  - `device_code` → show `user_code` prominently + an **Open browser** button
    (`NSWorkspace.shared.open(verification_url)`) + a "Waiting for approval…"
    indicator.
  - `authorized` → advance to setup.
  - `error` → show `message` + a **Retry** button (re-spawn login).
- **Setup.** Spawn `keld signal setup --json`; render a checklist from `tool`
  events (`configured` / `already_configured` / `skipped_conflict`); on `done`
  show an "all set" screen. `error` → show message + Retry.
- **`keld` resolution:** run `/usr/local/bin/keld` (the postinstall symlink),
  falling back to `/usr/local/keld/keld`.
- **Defensive:** unknown event kinds are ignored; a non-zero subprocess exit with
  no `error` line surfaces a generic failure; closing the window exits cleanly.
  Nothing the app does is required for the service to run.

Structure the parsing as a pure function `func decodeEvent(_ line: String) ->
OnboardEvent?` over an `enum OnboardEvent` so the wire mapping is isolated and, if
a Mac is available, unit-testable with `swift test`.

### Build & packaging (CI-only, on the macOS runner)

- Source: `installers/macos/KeldSetup/` as a **Swift Package** (executable target
  `KeldSetup`) — no `.xcodeproj`.
- `installers/macos/build-app.sh <stage-dir>`: runs `swift build -c release`,
  creates `<stage-dir>/KeldSetup.app/Contents/{MacOS/KeldSetup, Info.plist}` from
  a repo `Info.plist` template, copying the built binary in. No-op with a clear
  message if `swift` is absent (so the pkg can still build binaries-only if ever
  needed).
- `installers/macos/build-pkg.sh`: call `build-app.sh stage` **before** `pkgbuild`
  so the app ships in the payload at `/usr/local/keld/KeldSetup.app`; add the app
  to the existing `codesign` loop (deep-sign with `APPLE_DEVELOPER_ID_APP` when
  present). Whole-pkg notarization already covers it.
- `Info.plist`: `CFBundleIdentifier co.keld.setup`, `CFBundleName "Keld Setup"`,
  `CFBundleExecutable KeldSetup`, `LSMinimumSystemVersion 13.0`,
  `NSHighResolutionCapable true`.

### postinstall wiring

`installers/macos/scripts/postinstall` becomes:
1. Symlink `keld`/`keld-agent` into `/usr/local/bin` (unchanged).
2. `launchctl asuser <uid> sudo -u <user> keld-agent install` — now **headless**
   (no TTY) → registers the LaunchAgent service only (the shipped TTY guard), no
   hung interactive login.
3. `launchctl asuser <uid> sudo -u <user> open /usr/local/keld/KeldSetup.app` —
   launch onboarding in the user's GUI session. Best-effort (`|| true`).

## Files

- `installers/macos/KeldSetup/Package.swift` (create)
- `installers/macos/KeldSetup/Sources/KeldSetup/*.swift` (create)
- `installers/macos/KeldSetup/Info.plist` (create — bundle template)
- `installers/macos/build-app.sh` (create)
- `installers/macos/build-pkg.sh` (modify — build + stage + sign the app)
- `installers/macos/scripts/postinstall` (modify — headless install + launch app)

## Verification

**Hard constraint: no Swift toolchain on the dev box (Linux).** So:

- Locally verifiable: shell scripts parse (`bash -n`), `Info.plist` and
  `Package.swift` are well-formed, the Go build/tests remain green, and the
  postinstall logic reads correctly.
- CI-verified: the macOS installer job (`installers.yml`, incl. the unsigned
  `workflow_dispatch` dry run) runs `build-pkg.sh`, which now **compiles** the app
  via `swift build` — a Swift break fails CI.
- Human-verified (explicit sign-off, not claimable from here): run the produced
  `.pkg` on a Mac and confirm the app launches post-install, shows the code, opens
  the browser, completes setup, and that the service is registered regardless.

The plan will not claim the macOS UX "works" from this environment — only that it
compiles in CI and that the shell/packaging wiring is correct.
