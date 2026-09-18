# Handoff — macOS wizard onboarding

**Written 2026-09-15.** Everything below was observed on a real machine unless it
says otherwise. Where a fact was measured, the measurement is here rather than
just its conclusion, because most of these cost a full build-and-click to learn.

## Where things stand

| | state |
|---|---|
| `keld-signal` | **merged** — PR #31, `main` at `e1c2c74`. All checks green. |
| `keld-atlas` | **open** — PR #216 (the compact approval page the pane embeds). |
| This branch | `feat/installer-api-url` — an optional `api_url` input so a CI dry run can target a dev Atlas. |
| Installed on the dev Mac | v2.5.0 from a local build, paired to `http://localhost:8000`. |

Atlas #216's CI is red on `tests/test_billing_period.py::test_a_credit_landing_through_credit_attempt_invalidates_todays_partial_cache`. **That is inherited** — `main` fails the same test (run 34910328853); this PR runs `1 failed, 3233 passed` against main's `1 failed, 3232 passed`. Noted in a PR comment. Unowned.

## Build a pkg you can actually test with

Needs: macOS, the Command Line Tools, and a built `Keld Signal.app` — or, as here, a stub standing in for it (no Rust toolchain on the dev Mac).

```bash
# 1. binaries
go build -ldflags "-X github.com/ncx-ai/keld-signal/internal/version.CLI=v2.5.0" -o /tmp/stage/keld ./cmd/keld
go build -ldflags "-X github.com/ncx-ai/keld-signal/internal/version.CLI=v2.5.0" -o /tmp/stage/keld-agent ./cmd/keld-agent

# 2. a stub app component (Contents/MacOS/<exe> + Info.plist with CFBundlePackageType=APPL)
#    build-pkg.sh hard-fails without one, deliberately.

# 3. the pkg, aimed at a local Atlas
KELD_API_URL=http://localhost:8000 \
KELD_NOTARY_REQUIRED=0 \
KELD_APP_BUNDLE="/tmp/stub/Keld Signal.app" \
  bash installers/macos/build-pkg.sh v2.5.0 /tmp/stage arm64
```

It prints `! plugin targets http://localhost:8000 … NOT a release build` when the target is baked in. **Move the pkg and verify the destination's timestamp before testing it** — a chained `build && mv && open` silently tested a stale pkg twice here, and two rounds of "no different" were spent on it.

`v2.5.0` rather than `0.0.0-dryrun` on purpose: a dry-run version skips `--tag` and resolves `releases/latest`, so it never exercises the version-matched sidecar download — the path that used to 404 on every tagged build.

From CI, with this branch's change: run the `installers` workflow with `api_url` set. Useful only for a *shared* dev host; `localhost` in a CI artifact means whatever machine opens it.

## Things that are true and will waste your day if you assume otherwise

- **A pane cannot run after the Install step.** Ordered after `Install` it enters with `installStarted=0`, and the plugin's host process stops the moment installation completes. Everything interactive therefore happens pre-install, and `postinstall` does everything that rewrites files.
- **Built-in sections are referenced by NAME; only third-party bundles by filename.** `Introduction`, `ReadMe`, `License`, `Target`, `PackageSelection`, `Install`, `Summary` — note the one whose file is `TargetSelect.bundle` is named just `Target`. Getting this backwards scrambled the sidebar AND failed the install at the end with an empty message and `IFDInstallController state = 0`, having written no payload.
- **Auto Layout does not run in an installer pane.** The view is hosted in an `NSNextStepFrame.ViewBridge.jail`. Measured: a stack view 446pt wide inside a 418pt pane, children at x=0/2/134/303 with unrelated widths, `edgeInsets` ignored. `KeldPaneView` lays out explicit frames; only the content view clamps itself to the host bounds (applied to a nested instance that rule means "fill my parent" and drew the tool checkboxes over the account section).
- **Environment variables do not reach the pane.** Installer.app inherits them — they appear in `/var/log/install.log` — but the plugin's XPC service starts clean. Hence the build-time stamp.
- **`NSTask` refuses stream-property writes after launch.** `t.standardOutput = nil` in a termination handler raises `NSInvalidArgumentException`, uncaught inside a dispatch block, i.e. SIGTRAP: the wizard says "the installer encountered an error". Setting `terminationHandler` is fine; the stream properties are not.
- **A stale code signature makes the plugin fail to load with no diagnostic at all.** Sign after compiling, and verify.

## What no automated check can tell you

Every defect that actually mattered here was found by a human clicking: the crash, the clipped layout, the failed install. `make pkg-plugin-check` compiles, signs and verifies but never *runs* the pane; no CI job can drive a wizard. The runbook is `docs/macos-wizard-onboarding.md`, and it says plainly that the dry-run path skips `--tag` and so proves nothing about the download.

Before any release: build a **tagged** pkg and click it through on a real Mac.

## Verifying an install actually worked

```bash
cat /usr/local/keld/VERSION                       # matches the pkg
launchctl print gui/$(id -u)/co.keld.agent        # state = running, plist under ~/Library/LaunchAgents
cat ~/.local/bin/keld-agent-sidecar/VERSION       # matches the daemon — no skew
python3 -c "import json;print(json.load(open('$HOME/.keld/hook.json'))['endpoint'])"
ls ~/.keld/state/installer-handoff.json           # should NOT exist: postinstall consumed it
ls -d ~/.local/bin/.keld-update.* 2>/dev/null     # should be empty: no staging leaked
```

The daemon's ledger (`GET /v1/ledger` on the port in `~/.keld/agent.json`, with `x-keld-agent-secret`) should read `daemon ok / sidecar ok / store ok`.

Two invariants worth re-checking, because only a real install can:

- the tools must hold the **loopback secret**, not the Atlas ingest token — compare Claude Code's `OTEL_EXPORTER_OTLP_HEADERS` against `hook.json`'s `ingest_token`; they must differ
- the LaunchAgent plist must be under the user's home, not `/var/root` — that is what `sudo -H` buys, and nothing else proves it

## Config backups

They are **not** beside the config. `BackupConfig` writes `~/.keld/backups/<tool>/<basename>`, and it is **pristine-once**: an existing backup is never overwritten, so the copy on disk is the genuinely pre-keld original. Verified in an isolated `HOME`: on a real change it captures the pre-write content exactly, and the rewrite merges rather than replaces.

## Open, in rough priority order

1. **Manual click-through of a tagged build** — the only untested path that ships.
2. **Atlas #216 needs review/merge**; until it lands, the pane falls back to `verification_url` and renders the browser-shaped page in the panel.
3. **Notarization of a plugin-bearing pkg** has never run. Exercise it with a signed `workflow_dispatch`, which receives secrets without cutting a release.
4. **Windows parity.** `onboard.cmd` has the identical defect, and everything new here except the ObjC lives in Go behind `--json`.
5. **The desktop app is pre-alpha** and deliberately not part of this flow — though the pkg still installs it. Worth deciding whether public builds should ship it at all.
6. **The tool list shows only installed tools**, so someone who installs Codex next week gets no prompt. Listing undetected tools unticked would cover it.
