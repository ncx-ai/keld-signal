# Packaging and installers: signing, notarization, the sidecar's version, onboarding

> **Provenance — split out of `AGENTS.md` on 2026-09-28** (at `4d2b6d0`), verbatim.
> The material below was last substantively changed in `AGENTS.md` on **2026-09-23**
> (best effort: the newest commit `git log -L` finds over the moved line ranges).
> Every measurement here is stated as it was when written; the split re-verified
> **none** of them. Treat an undated figure as "true when measured, unknown now",
> and re-measure before acting on one.

AGENTS.md → *Gotchas* keeps the one-line rule for each of these. This file
carries the full entries — the notarization stall, the pkg that ships without
the sidecar and the three weeks of blocks its presence check cost, the engine
download that moved out of the installer, and — since 2026-09-29 — the absence
of any onboarding UI in the installers (the last two bullets).
See also `docs/macos-signing-and-notarization.md` and `docs/install.md`;
`docs/macos-wizard-onboarding.md` is kept as the superseded history.

- **macOS signing needs TWO certs, and notarization is decoupled from the release.**
  `installers/macos/build-pkg.sh` signs **every** Mach-O in the payload with the
  *Developer ID Application* cert — not just the three entrypoints, because the
  frozen sidecar is a one-dir tree of ~15k files / ~100 native libs and
  notarization rejects the whole submission over a single unsigned one — then signs
  the pkg itself with the *Developer ID Installer* cert. CI imports both p12s into
  a throwaway keychain and **derives the identity names from it** (a hand-typed
  name fails at `productsign` with an opaque error). Bundle the **G2 intermediate**
  in each p12 or a clean runner can't build a chain to a trusted root.
  ⚠️ **AND NOTHING IN THE INSTALLER DOWNLOADS IT ANY MORE — THE PAGE DOES.**
  The wizard pane used to fetch the ~300 MB engine and hold Continue until the
  fetch SETTLED (succeeded or failed). Three things were wrong, and the third
  broke a real install on 2026-09-21: it puts a large download in front of
  somebody who has not finished installing, on a release host measured
  answering **504 on three of four full pulls** with a 30-minute client timeout
  per attempt; the engine is not needed to FINISH installing (telemetry works
  without it, enrichment spools); and rendering its progress from the
  XPC-hosted pane drove a layout pass that pegged the plugin's main thread —
  sampled on the stuck installer, **302 of 553 samples** in
  `updateNextEnabled → KeldPaneView layout → heightFor:width:`, with the
  download **already finished and staged on disk**. The person watched
  "Downloading the analysis engine" for as long as they were willing to wait
  for something that had succeeded.
  ⚠️ **AND IT IS AUTOMATIC — THE PAGE REPORTS, IT DOES NOT ASK.** The first cut
  of this put a Download/Update button on the page, which is the installer's
  question moved one screen along: a mismatched engine is version SKEW, not a
  preference, and every hour that button goes unclicked is an hour of work that
  cuts no blocks. `engineManager.autoStart` fetches once per daemon run
  (bounded, because a fetch that failed will fail the same way in thirty seconds
  and a clock would turn a flaky host into a download loop); the page shows a
  one-line bar with progress and names the version when it lands; only a
  FAILURE offers Try again, which is the one place a human choice exists again.
  ⚠️ **The fetch is PINNED to the daemon's own release**, and shipping it
  unpinned in `v3.0.5-rc.4` installed a stale engine within the hour: an empty
  tag resolves `releases/latest`, every `-rc.N` is a PRERELEASE, and that
  endpoint excludes them — so a 3.0.5-rc.4 daemon fetched **v3.0.4** and the
  page then correctly reported the engine it had just installed as out of date.
  `postinstall` and `onboard.command` already pinned; this was the fourth caller
  of a rule the other three encoded privately, which is the argument for
  `internal/sidecarinstall` owning it.
  The daemon owns it now: `GET /v1/engine` reports whether one is NEEDED
  (`ml_backend` ≠ "off"), what is installed, and whether it is outdated — read
  from DISK, never by probing the running service, so a present-but-starting
  engine never reads as absent — and `POST /v1/engine/install` starts one fetch
  and answers **202 immediately** while the page polls. `dev` on either half
  answers "not outdated", the same cannot-tell refusal `version.Skew`,
  `localagent.ModelState` and the doctor check all make. The page shows
  **nothing at all** on a healthy machine (`ui/app.js` · `engineNotice`), so
  nobody is handed a 300 MB button they have no reason to press. The install
  logic itself moved to `internal/sidecarinstall` so the CLI command and the
  daemon run ONE definition rather than the daemon shelling out to a binary.
  ⚠️ **postinstall still fetches on a SILENT/MDM install** — the same condition
  that decides whether to open Signal — because a machine nobody will open the
  page on would otherwise publish no blocks and never say why. A GUI install
  does not; the card is the notification. ⚠️ **Since 2026-09-29 that condition
  is how the install was STARTED, not `had_handoff`** (the pane that wrote the
  handoff is gone): GUI means `COMMAND_LINE_INSTALL` is unset (`man installer`:
  "Set when performing an installation using the installer command") **and**
  Installer.app is running, so an MDM agent driving installd directly also
  counts as silent. Pinned by running the real script under stubs in
  `installers/macos/postinstall_test.sh`.
  ⚠️ **The pkg ships WITHOUT the sidecar.** Apple's notary service scans every file
  in a submission, and the frozen sidecar is ~15k files / ~190MB of torch — which
  put a real submission **4+ hours** into an unbounded queue. The pkg payload is now
  just `keld`, `keld-agent`, `VERSION` (3 files since `onboard.command` was deleted
  on 2026-09-29; ~2 Mach-O to sign instead of ~103). What follows describes
  `onboard.command` as it was: it fetched the sidecar tarball into
  **`~/.local/bin`** — a well-known `sidecarBinPath()` dir that is user-writable, so
  no sudo prompt, and the same place `install.sh` puts it. It fetches **before**
  `keld-agent install`, because that command starts the daemon and the sidecar
  should exist by then. Pinned to the pkg's own release via the staged `VERSION`
  file (falls back to the latest-release API for dry-run builds), Apple-Silicon-only,
  and non-fatal on failure: telemetry still works, enrichment jobs spool, re-running
  the script retries.
  ⚠️ **AND IT SKIPPED ON PRESENCE, WHICH COST ~3 WEEKS OF BLOCKS.** `fetch_sidecar`
  used to return early whenever any sidecar directory existed, so a pkg upgrade over
  an earlier install kept whatever sidecar was already there — measured on a real
  machine, a **2.3.0 daemon against an Aug 11 sidecar**. That sidecar predates
  `/blocks` entirely, so it answered **404**, which the emitter read as "no blocks
  closed yet": telemetry flowed, **zero blocks published**, and `keld signal doctor`
  reported no problems throughout, correctly — every fact either side could reach was
  fine, because **neither half knew what the other was**. The two halves ship as
  separate artifacts on separate cadences and nothing compared them.
  The fix is one stamp and three readers. `sidecar/build-freeze.sh` writes
  `dist/keld-agent-sidecar/VERSION` from `KELD_VERSION` (⚠️ at the tree ROOT, **not**
  via PyInstaller `datas`, which land under `_internal/` — a shell script must read
  it, so a PyInstaller layout change must not silently turn every comparison into
  "no version"); `onboard.command` compares it against the pkg's own `VERSION` and
  **replaces on mismatch, or when the tree carries no VERSION at all** (which predates
  the stamp and is therefore stale by definition); the sidecar returns it on
  `/health`; and the daemon compares it against `version.CLI` once per run, emitting
  `sidecar.version_skew` (warn, floor-exempt) plus one log line, with `keld signal
  doctor` saying the same thing on demand.
  ⚠️ **`dev` ON EITHER HALF MEANS "CANNOT TELL", NEVER SKEW** (`version.Skew` returns
  `known=false`): a source checkout, `make sidecar`'s venv wrapper and any local
  freeze have no VERSION, and a check that fires on every developer machine is one
  nobody reads on the machine that matters. Same refusal `localagent.ModelState` and
  `TelemetryState` make. An **unreachable** sidecar is likewise silent here — that is
  `sidecar.unavailable`'s job, and describing one failure twice under two names is how
  a fleet view stops meaning anything.
  ⚠️ **THE INSTALLER HALF IS macOS-ONLY; THE DETECTION HALF IS NOT.** Windows bundles
  the sidecar in the Inno payload (`ignoreversion recursesubdirs`) and `install.sh`
  replaces it unconditionally, so only the pkg — which cannot carry it past
  notarization — produces skew by construction. The daemon/doctor check runs
  everywhere anyway: a hand-placed sidecar, an interrupted update or a restored
  `.prev` produces the same state without the known cause.
  ⚠️ **`enrich.BlocksAnswer` EXISTS FOR THIS**, and a bare `ok` bool is what hid it:
  `BlocksCharacterised` now answers with a struct carrying `RouteUnsupported`, so a
  404 is distinguishable from "the store is behind". The emitter still **HOLDS the
  cursor** either way — the work becomes doable when the sidecar catches up — so what
  changed is what is SAID, not what is done. That generalizes the reading
  `/attribute` already had. Spec:
  `docs/superpowers/specs/2026-09-04-sidecar-version-skew-discovery.md`.
  ⚠️ **Notarization is a HARD GATE — a release cannot ship un-notarized.**
  `KELD_NOTARY_REQUIRED` defaults to **1**, so `build-pkg.sh` fails unless Apple
  returns `Accepted`; the workflow relaxes it to 0 only for the documented
  **no-secrets** path (forks/dry runs without the Apple secrets, which are meant to
  produce unsigned non-distributable output). The earlier design shipped regardless
  of verdict, which was wrong: "unstapled but valid online" only holds once a ticket
  **exists**, and with no verdict there is no ticket, so Gatekeeper blocks the
  installer outright. That hedge existed because Apple returned *zero* verdicts for
  days (one submission sat 5h32m — no error, no log, no queue position, service
  healthy); it resolved 2026-08-06 **account-side**, and verdicts now land in ~25s
  (23s v0.20.0, 24s v0.21.0), so tolerating "no verdict" buys nothing.
  `KELD_NOTARY_TIMEOUT` (default 15m) is now the **stall tolerance before failing**,
  ~36x observed latency. A rejection (`Invalid`) fails for a different reason — a
  broken payload, which waiting won't fix. The submission id is still written to
  `<pkg>.notarization-id` + the run summary first, so a failed build can be stapled
  or diagnosed without log archaeology. `staple.yml` sweeps daily as a backstop.
  Invariants pinned by `installers/macos/build_pkg_notarization_test.sh` (static
  assertions — the gate can't execute off macOS).

- **macOS: the pkg has NO Keld screen (since 2026-09-29).** It installs and asks
  nothing — no sign-in, no setup code, no tool picker
  (`docs/superpowers/specs/2026-09-29-signal-web-signin-discovery.html`, AC-10,
  D10). `scripts/postinstall` symlinks the CLIs, registers the agent as the
  console user (`launchctl asuser <uid> sudo -u <user> -H`), and then:
  - **GUI install** (`COMMAND_LINE_INSTALL` unset AND Installer.app running):
    opens `/Applications/Keld Signal.app`, after registration so its first frame
    finds a daemon. Signal asks the one real question on first open — sign in
    with Atlas, or use locally only — and the daemon's auto-setup
    (`internal/agent/integrations`, `auto_setup_integrations`, default ON)
    configures the AI tools it detects. No engine fetch here; the daemon owns it.
  - **Command-line / MDM install**: fetches the engine through a self-removing
    launchd job and opens nothing. Pairing is `keld-agent install --code <CODE>`
    afterwards, exactly as before (`docs/install.md`).
  What was removed: the Installer.app section (`installers/macos/plugin/`,
  `KeldSetup.bundle`, its `SectionOrder`, the build/sign step and
  `productbuild --plugins`), the handoff file and the tool-setup step that read
  it, and the `onboard.command` Terminal fallback. `plugin_test.sh`,
  `onboard_command_test.sh` and `build_pkg_notarization_test.sh` are INVERTED
  rather than deleted, so any of them coming back fails CI. The measured facts
  that shaped the pane (a section after Install never appears; a missing
  `.bundle` or a stale signature fails silently) are kept in
  `docs/macos-wizard-onboarding.md`, marked superseded.
  ⚠️ **Only statically verified.** No CI job can click Installer.app; the
  `pgrep -x Installer` check and `open -a` from postinstall's sandbox need one
  real double-click install to confirm (AC-10's manual run).
- **Windows: the installer asks nothing either (since 2026-09-29).** The Inno
  `[Code]` "Set up Keld" page — setup code, a device-flow sign-in with Atlas's
  approval page embedded through `keld-wizard-host --panel`, and a tool
  picker — is removed, and so is `onboard.cmd`. (This bullet used to say that
  page "never existed"; it was then built, and is now gone again, which is why
  this is dated.) `ssPostInstall` registers the agent through `RunQuiet`
  (`keld-wizard-host --run`, so no console) with `--headless`, unconditionally,
  then waits up to 10 s for the NEW daemon's `agent.json` — earlier, `keld
  signal open` finds no daemon on a first install or the previous one's port on
  an upgrade. The one `[Run]` entry is `keld.exe signal open` with `postinstall
  shellexec skipifsilent nowait`: a ticked checkbox on the Finished page, and
  nothing at all on a `/SILENT` or `/VERYSILENT` push, which is paired with
  `keld-agent install --code <CODE>` from the management tool.
  ⚠️ **Do not add `runhidden` to that line** — it is how `onboard.cmd` once ran an
  interactive login where nobody could see it, and every Windows machine idled
  forever. `keld-wizard-host.exe` stays installed: `RunQuiet` and the KeldAgent
  logon task (`--spawn`) both use it. Pinned by
  `installers/windows/keld_agent_iss_test.sh`.
  ⚠️ **Not verified on Windows.** `iscc` compiling the `.iss` in CI proves it
  parses and stages; it cannot show that the Finished page opened Signal.
  `keld.exe` is a console binary, so the page-open step likely flashes a console
  for the moment it runs — unmeasured.
