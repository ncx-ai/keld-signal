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
download that moved out of the installer, and the two onboarding UIs.
See also `docs/macos-signing-and-notarization.md` and
`docs/macos-wizard-onboarding.md`.

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
  ⚠️ **postinstall still fetches on a SILENT/MDM install** (`had_handoff`
  false) — the same condition that decides whether to open `onboard.command` —
  because a machine nobody will open the page on would otherwise publish no
  blocks and never say why. A GUI install does not; the card is the
  notification. `installers/macos/plugin_test.sh` INVERTS its three old
  assertions rather than deleting them, so a download reintroduced into that
  pane fails there.
  ⚠️ **The pkg ships WITHOUT the sidecar.** Apple's notary service scans every file
  in a submission, and the frozen sidecar is ~15k files / ~190MB of torch — which
  put a real submission **4+ hours** into an unbounded queue. The pkg payload is now
  just `keld`, `keld-agent`, `onboard.command`, `VERSION` (4 files, ~2 Mach-O to
  sign instead of ~103). `onboard.command` fetches the sidecar tarball into
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

- **macOS onboarding UI:** onboarding happens INSIDE the installer wizard — a
  custom Installer.app section (`installers/macos/plugin/`, ordered before the
  Install step) that redeems the setup code, downloads the analysis sidecar with a
  progress bar, and collects which AI tools to configure. It renders the NDJSON
  emitted by `keld … --json` and reimplements none of it.
  ⚠️ **A pane CANNOT be placed after the Install step** (measured 2026-09-14,
  macOS 26.5.2: it enters with `installStarted=0` and the plugin's host process
  stops when installation completes), which is why everything interactive is
  pre-install and `scripts/postinstall` does every destructive step afterwards.
  ⚠️ **Two failure modes here are completely silent** — a `SectionOrder` entry
  missing `.bundle` loads nothing, and a bundle signed before its executable was
  recompiled fails to load with no diagnostic at all. Both are pinned by
  `installers/macos/plugin_test.sh`. `postinstall` falls back to opening
  `onboard.command` only when BOTH the pane never ran at all (no handoff file
  was ever written) AND the machine ended up unconfigured (no `hook.json`) —
  not on either alone: gating on `hook.json` by itself would also fire for a
  person who ran the pane and deliberately chose "Set up later", and opening a
  Terminal at someone who just made that choice is exactly what this branch
  exists to stop. `installers/macos/onboard.command` is retained for the
  pane-never-ran fallback and for MDM; it is no longer opened on the success
  path. It is staged into the payload by `build-pkg.sh` (alongside `keld`,
  `keld-agent` and `VERSION`) and, on the fallback path, opened via
  `launchctl asuser <uid> sudo -u <user> open "$PREFIX/onboard.command"` — the
  same asuser idiom every other user-side postinstall command uses, so the
  script runs in the console user's own GUI session rather than root's.
  See `docs/macos-wizard-onboarding.md`.
- **Windows onboarding UI:** `installers/windows/onboard.cmd`, staged into the
  payload by the `.iss` `[Files]` section and opened by the post-install `[Run]`
  step with `postinstall shellexec skipifsilent`. It is the sibling of macOS's
  `onboard.command` and does the same three things: prompt for the one-time setup
  code, run `keld-agent install --code "$CODE"` (falling back to `--yes` browser
  login), and report success from OBSERVED STATE — an `ingest_token` in
  `hook.json` — never from an exit code. `skipifsilent` is there so an MDM
  `/SILENT` push does not block on a console waiting for a human; such a machine
  is finished by `keld-agent install --code <CODE>` from the management tool.
  ⚠️ **This bullet used to describe an Inno `[Code]` wizard page driving `keld
  --json` with a WinAPI timer and async NDJSON polling, and said its "UX is
  human-verified on Windows". THAT PAGE NEVER EXISTED** — `git log` on the `.iss`
  shows two commits and neither added it. What was actually there was `[Run]
  keld-agent.exe install` with `runhidden nowait`: an interactive login in a
  window nobody could see, on a step Inno neither waited for nor could report.
  Every Windows machine registered its logon task and then idled on
  `awaitConfig` forever — nothing collected, nothing said. A doc describing
  unbuilt code as built is what kept that invisible, which is why the correction
  is stated rather than quietly swapped. **Do not re-add `runhidden` to that
  `[Run]` line.** The wizard page is a nicer UX and remains a legitimate future
  change; it is an aspiration, not a description.
  ⚠️ **Not verified on Windows.** `iscc` compiling the `.iss` in CI proves
  `onboard.cmd` is staged (a missing `Source:` is a compile error) and nothing
  more; no CI check can confirm a console appeared and a human pasted a code.
