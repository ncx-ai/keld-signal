# macOS onboarding — the wizard pane

**Status:** implemented 2026-09-14. Spec:
`docs/superpowers/specs/2026-09-14-macos-wizard-native-onboarding-design.md`.

Installing Keld on macOS involves no Terminal, no browser and no second app. The
pkg carries a custom Installer.app section (`installers/macos/plugin/`) that runs
BEFORE the payload is installed and:

1. redeems the setup code (`keld login --code --json`),
2. downloads and verifies the analysis sidecar with a progress bar
   (`keld signal install-sidecar --json --stage-only`),
3. collects which AI tools to configure (`keld signal setup --dry-run --json`).

`scripts/postinstall` then commits the sidecar, applies the tool configuration
(`--bin-path /usr/local/keld/keld`), and registers the agent — all as the console
user, via `launchctl asuser <uid> sudo -u <user> -H`.

## Things that fail SILENTLY here

- **A `SectionOrder` entry without `.bundle`** does not load its section. No error,
  no log line. `installers/macos/plugin_test.sh` asserts against it.
- **A bundle signed before its executable was recompiled** does not load. Same
  silence. `build-plugin.sh` signs last and verifies; `make pkg-plugin-check`
  re-runs that locally.
- **A pane ordered after `Install.bundle`** never appears — the plugin's host
  process stops when installation completes. Measured, not assumed.
- **`sudo` without `-H`** writes the LaunchAgent into `/var/root`.

The fallback is two-part, not one: `postinstall` opens `onboard.command` (the
pre-wizard Terminal flow, kept for exactly this) only when BOTH the pane never
ran at all (no handoff file was ever written) AND the machine ended up
unconfigured (no `hook.json`). Gating on `hook.json` alone would also fire for
someone who ran the pane and deliberately clicked "Set up later" — opening a
Terminal at a person who just made that choice is exactly what the two-part
condition exists to avoid.

## Verifying a build (no production release)

1. `make release-dry` — builds installers as workflow artifacts. **No tag, no
   release**, version `0.0.0-dryrun`. Nothing published, so no live machine can
   resolve it (`install.sh` reads *latest release*; `agent_release` is not served).
2. Download the macOS artifact, run it on a test Mac (`make scaleway-up` for a
   cloud Apple-silicon host).
3. Pair with an **atlas-dev** setup code (`atlas-dev.keld.co/ABCD-EFGH`) so no
   machine lands in the production org.
4. Check, in order:
   - the "Set Up Keld" pane appears after Introduction/License;
   - a bad code is refused inline and **Continue stays disabled**;
   - a good code turns the row green and enables Continue;
   - the engine progress bar advances and does not gate Continue;
   - the tool list shows what is installed, ticked;
   - after the install: `~/.keld/hook.json` holds an `ingest_token`,
     `launchctl print gui/$(id -u)/co.keld.agent` shows the job,
     `~/.local/bin/keld-agent-sidecar/VERSION` matches the pkg,
     and **no Terminal window ever opened**.
5. Signing and notarization: a `workflow_dispatch` run receives the Apple secrets,
   so signing can be exercised without cutting a release. Verdicts have been
   landing in ~25s.
6. ⚠️ **This step is only meaningful on a TAGGED build.** `make release-dry`'s
   `0.0.0-dryrun` version takes the no-tag branch in both the pane
   (`startSidecarDownload`) and `postinstall`'s fallback fetch — it never
   builds a `--tag` argument at all, so a dry run proves nothing about tag
   resolution and cannot catch a tag built wrong (e.g. a doubled `v`). On an
   actual tagged build (a real release, or a `workflow_dispatch` run against a
   tag), confirm: the engine progress row actually reaches **100%** rather
   than falling back to "Could not download it" partway through, and
   `~/.local/bin/keld-agent-sidecar/VERSION` equals `/usr/local/keld/VERSION`
   exactly (not merely present) — the two must match because a mismatch is
   the version-skew failure this whole path exists to prevent.
