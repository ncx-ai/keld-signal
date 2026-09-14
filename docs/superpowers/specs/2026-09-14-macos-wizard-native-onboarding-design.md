# macOS wizard-native onboarding — eliminating the Terminal from the pkg

**Status:** design, 2026-09-14. Implements: no shell, no browser, no app — the
whole install and setup happens inside the Installer.app wizard.
**Scope:** macOS pkg only. Windows is out of scope and designed for (§11).

## 1. The problem

`installers/macos/scripts/postinstall` ends by opening a **Terminal window**
running `onboard.command`, which is where three things happen:

1. `fetch_sidecar` — downloads the ~190 MB analysis sidecar into `~/.local/bin`,
   version-compared against the pkg's staged `VERSION`, SHA-256 verified.
2. `read -r CODE` — **blocks on a human** pasting a setup code, then runs
   `keld-agent install --code`, which is `keld login --code` → `keld signal setup`
   (rewrites Claude Code / Codex / Gemini configs) → `service.InstallAt`
   (LaunchAgent + `launchctl bootstrap`).
3. Reports success from observed state (an `ingest_token` in `hook.json`).

On a first install **nothing else registers the LaunchAgent** — `postinstall`'s
`launchctl kickstart` is a no-op until the job exists. So the Terminal is not
cosmetic; it is load-bearing.

A wizard exists to be the setup UI. Handing the person a shell — or a browser
tab, or a second app — at the end of a GUI install is the defect.

## 2. Decision

**One custom Installer.app pane, placed before the Install step, driving the
existing `keld` binary over its existing `--json` NDJSON interface.**

The pane collects what only a human can supply and shows the work that takes
time; `postinstall` then does every privileged and destructive step silently
after the person has committed to the install.

⚠️ **Nothing about auth, tool configuration or download verification is
reimplemented in ObjC.** The plugin is a renderer. `internal/cli`'s
`SetupOpts.Emit` / `--json` seam — built for exactly this and never yet
consumed by a native installer — is the interface.

## 3. What was measured (probe, 2026-09-14, macOS 26.5.2 / build 25F84)

A throwaway plugin was built and run against a real pkg. Results, including the
two things that turned out to be false:

| # | Claim | Result |
|---|---|---|
| 1 | Custom wizard panes still work | **YES.** `InstallerPlugins.framework` headers ship in the current SDK with no deprecation annotations; `productbuild --plugins` is in the current man page; Installer.app's own panes (`Introduction.bundle`, `Install.bundle`, `Summary.bundle`) are peers of ours. |
| 2 | Buildable without Xcode | **YES.** `clang -bundle -framework InstallerPlugins`, ~90 lines of ObjC, **no nib** — override `-firstPane` and build the view programmatically (`InstallerSection.h` sanctions this: a subclass may override `willLoadMainNib` "if no default nib is specified"). |
| 3 | The pane can do real work | **YES.** Logged `uid=502` (the real user), **not sandboxed** — `InstallerRemotePluginService.xpc`'s only entitlement is `disable-library-validation`. It read `~/.keld/agent.json`, made an `NSURLSession` request to `127.0.0.1` and got an HTTP response, and loaded a page in a `WKWebView`. |
| 4 | The pane can drive `keld` | **YES.** `NSTASK OK status=0 output=keld version 0.23.0` — spawned the installed binary and captured stdout. This is what makes §2's "renderer, not reimplementation" possible. |
| 5 | A pane can run **after** the install | **NO.** With correct naming and the section ordered between `Install.bundle` and `Summary.bundle`, the pane entered with `installStarted=0`, exited still `installStarted=0`, the install then ran, and the wizard closed with no further pane. The plugin's process stops ticking the moment installation completes. |

⚠️ **Two measurements produced a WRONG conclusion first, and both are recorded
because either one silently wastes a day.**

- **`SectionOrder` entries are bundle FILENAMES WITH `.bundle`.** A bare name
  (`KeldSetup`) does not load the section at all — no pane, no error, no log
  line. Worse, a list whose *built-in* entries are bare (`Introduction`,
  `Install`) matches nothing, leaving our section the only one in the order, so
  it renders FIRST. That is what made an early run look like "a post-install
  pane works" when the section simply was not where the plist said.
- **A STALE CODE SIGNATURE MAKES THE PLUGIN FAIL TO LOAD SILENTLY.** Signing the
  bundle and then recompiling the Mach-O inside it produced three runs with no
  pane, no error and nothing in `log show`. This is the same failure class as the
  sidecar version skew: both halves healthy, no diagnostic, nothing collected.
  §6 turns it into a build-time gate and §8 into a runtime fallback.

**Consequence of #5, which shapes everything else:** every interactive or
long-running step must happen BEFORE the payload is installed. There is no
post-install UI surface in a pkg. (There will be one when the desktop app
ships; see §11.)

## 4. The flow

```
Introduction → License → [ Set Up Keld ]  → Install → Done
                          ^ our pane        ^ postinstall, silent
```

**The pane**, top to bottom:

1. **Your setup code** — a text field. On submit the pane runs the embedded
   `keld login --code <CODE> --api-url <host> --json`, parses the NDJSON, and
   renders `authorized` or the real error. **Continue is disabled until the code
   is accepted** (`InstallerPane.nextEnabled`), so a bad code is caught *before*
   the install rather than after it — strictly better than today, where the
   Terminal asks afterwards and a failed code degrades to a printed hint.
   A **Set up later** button explicitly enables Continue without a code; the
   machine then installs and idles on `awaitConfig`, which is an existing,
   documented, recoverable state.
2. **Analysis engine** — a determinate progress bar over the ~190 MB download,
   run as `keld signal install-sidecar --json` (§5.4). Failure is **non-fatal
   and stated**; Continue is never gated on it.
3. **Your AI tools** — the detected tools with checkboxes, all ticked by
   default. The pane only COLLECTS this choice (see §7).

**`postinstall`** (root, no window, no prompts) then:

1. symlinks + stray-copy repointing — unchanged;
2. moves the staged sidecar into `~/.local/bin` (a rename, §5.4);
3. runs `keld signal setup --yes --bin-path /usr/local/keld/keld --tool …` for
   the ticked tools, **as the console user**;
4. runs `keld-agent install` — writes `agent-config.json`, registers the
   LaunchAgent, starts the daemon;
5. if the handoff file is absent, falls back to today's behaviour (§8).

⚠️ **Steps 3-4 must run as the user with `-H`.** `service_darwin.go` builds the
plist path from `os.UserHomeDir()`, which on Unix reads `$HOME`; `sudo -u "$user"`
from root leaves `HOME=/var/root`, so the LaunchAgent would be written into root's
home and never load. Same for `keld signal setup`, which writes `~/.keld` and the
user's tool configs. The invocation is
`launchctl asuser "$uid" sudo -u "$user" -H …`, the session-handoff mechanism
`postinstall` already uses to open `onboard.command`.

## 5. Components

### 5.1 `installers/macos/plugin/` — the pane (new)

- `KeldSetup.m` — `InstallerSection` subclass (overrides `title`, `firstPane`)
  and one `InstallerPane` subclass. No nib, no Xcode project.
- `Info.plist` — `NSPrincipalClass`, `InstallerSectionTitle`, `CFBundlePackageType BNDL`.
- `InstallerSections.plist` — the order, **with `.bundle` on every entry**:
  `Introduction.bundle, ReadMe.bundle, License.bundle, TargetSelect.bundle,
  PackageSelection.bundle, KeldSetup.bundle, Install.bundle, Summary.bundle`.
- `Contents/Resources/keld` — a copy of the CLI (12.9 MB), so the pane has
  something to drive before the payload exists on disk.

The pane's only logic is: run a process, parse NDJSON lines, update three rows,
write one handoff file. Every decision it renders is made by Go.

### 5.2 `keld signal setup --bin-path` (new flag)

`keldBinaryPath()` pins the RUNNING binary into each tool's hook command.
Invoked from inside the plugin bundle that path is a temporary location that
ceases to exist after the wizard closes, so every hook would break silently.
`--bin-path` overrides it with `/usr/local/keld/keld`. Without this flag the
whole design writes broken hooks — it is not a convenience.

### 5.3 Tool selection passthrough

`keld signal setup` already takes `--tool` (`internal/cli/setup.go:362`); the pane records the ticked names and
`postinstall` passes them. `tools.Select` already errors on an unknown name.

### 5.4 `keld signal install-sidecar --json` (new subcommand)

Wraps `internal/agent/update`'s `Fetcher` (which already downloads and SHA-256
verifies this exact asset) plus `StageDir`/`DetectSidecarLayout`. Emits NDJSON
progress events. It **deletes the third copy of this logic** — today it is Go in
`update/fetch.go`, shell in `onboard.command`, and shell again in `install.sh`.

Staging goes INSIDE `~/.local/bin` for the reason `update.StageDir` already
documents: the commit must be a same-filesystem rename, not a cross-device copy
of ~15,000 files.

⚠️ **The missing-checksum policy here is the INSTALLER's, not auto-update's.**
`docs/auto-update.md` treats an absent published SHA-256 as fatal because the
swap is unattended; `install.sh` warns and continues because a human is reading.
This path has a human watching a progress bar, so it follows `install.sh`:
absent hash ⇒ stated warning, mismatch ⇒ always fatal.

### 5.5 `build-pkg.sh`

- stage the plugin dir, copy `keld` into its `Resources`;
- **sign the plugin AFTER compiling it, then `codesign --verify --strict`** —
  §3's silent-load failure;
- add the plugin to the `sign-macho.sh` sweep: it is handed to
  `productbuild --plugins` from a directory outside `$STAGE`, so the existing
  sweep does not see it, and an unsigned Mach-O anywhere in a submission fails
  notarization;
- pass `--plugins`.

`distribution.xml` is unchanged.

### 5.6 `postinstall`

Rewritten per §4. It stops opening `onboard.command` on the success path.

## 6. The handoff contract

The pane writes `~/.keld/state/installer-handoff.json` (0600, created as the
user):

```json
{ "version": "3.0.0-rc.5", "paired": true, "tools": ["claude_code", "codex"],
  "sidecar_staged": "/Users/x/.local/bin/.keld-sidecar.AbC123" }
```

⚠️ **The setup code itself is NEVER written to disk.** The pane redeems it
immediately and records only whether pairing succeeded — `auth.json`, written by
`keld login`, is the single credential artifact, exactly as today.

`postinstall` deletes the file after consuming it.

## 7. Ordering rule: nothing destructive before the commit point

The pane runs before the install, so a person can still cancel. Therefore:

- **Allowed in the pane:** redeeming the code (writes `auth.json` — a token, and
  a cancelled install leaves it inert), and downloading the sidecar tarball into
  a staging dir. Both are discardable.
- **Not allowed in the pane:** rewriting a tool's `settings.json`, writing
  `hook.json`, registering a LaunchAgent. Those happen in `postinstall`, after
  the person has committed.

The cost is that per-tool results are not visible (the pane says "will
configure", not "configured"), which is the price of #5 in §3 and is stated
rather than hidden.

## 8. Failure modes

| Failure | Behaviour |
|---|---|
| Plugin fails to load (stale signature, bad plist) | **`postinstall` finds no handoff file and opens `onboard.command`, exactly as today.** The pane's own silent-failure mode is what makes this mandatory rather than tidy. |
| Person clicks **Set up later** | Install + service registration proceed; the daemon idles on `awaitConfig` and adopts config the moment it appears. Re-running the pkg finishes setup. |
| Code rejected | Stated inline from the NDJSON error; Continue stays disabled; **Set up later** is the escape. |
| Sidecar download fails / no network | Stated, non-fatal. `postinstall` retries it in the background. Enrichment spools until it lands — the posture the daemon already has. |
| Checksum mismatch | Fatal to the fetch, never installed (§5.4). |
| Install cancelled after pairing | `auth.json` exists, nothing else. Harmless; a later install reuses it. |

## 9. Privacy

Unchanged, and worth stating because a new process now runs during install: the
pane reads no prompt text, no transcript and no span. It handles one setup code
(never persisted), a list of tool names, and a download. `postinstall` runs the
same Go paths that already exist. Nothing new crosses the wire.

## 10. Testing

**What CI can verify** (static assertions, the `onboard_command_test.sh` /
`build_pkg_notarization_test.sh` precedent):

- every `SectionOrder` entry ends in `.bundle` — §3's silent-nonload;
- `build-pkg.sh` signs the plugin **after** building it and verifies it;
- the plugin is in the signing sweep;
- `postinstall` runs its user-side commands via `sudo -u … -H`;
- `postinstall` retains the no-handoff fallback to `onboard.command`;
- a new `make pkg-plugin-check` compiles the plugin and `codesign --verify`s it
  (macOS-only; skipped elsewhere).

**Go tests:** `--bin-path` reaches `SetupParams.BinPath`; `install-sidecar`'s
fetch/verify/commit against a fake release server (`fetch_test.go`'s pattern).

⚠️ **What no CI check can verify:** that the pane appeared and a human could use
it. `iscc` compiling an `.iss` proved nothing equivalent on Windows and this is
the same limit. The wizard is driven by hand on a real Mac before each release —
`make scaleway-up` (Scaleway Apple silicon) exists for it.

## 11. Rollout, without touching production

- **`make release-dry`** (`gh workflow run installers.yml`) builds installers as
  **workflow artifacts** — no tag, no GitHub release, version `0.0.0-dryrun`.
  Nothing a live machine resolves: `install.sh` reads *latest release*, and
  `agent_release` (the auto-update pin) is not served by Atlas at all.
- `workflow_dispatch` **does** receive secrets, which per that workflow's own
  comment is "the way to exercise signing without cutting a release" — so the one
  unverified item (does a plugin-bearing pkg notarize) is answered in a dry run.
  Notarization is an automated scan, measured at **23 s / 24 s** on the last two
  releases; the 5h32m stall was a ~15k-file submission and resolved 2026-08-06.
  This adds two Mach-Os, the same reasoning `build-pkg.sh` already recorded for
  the app bundle.
- Two dry-run side effects, stated so they are not rediscovered: a
  `0.0.0-dryrun` pkg falls back to *latest release* for the sidecar asset (a
  read-only pull), and `version.Skew` reads `dev` ⇒ **"cannot tell"**, so a test
  machine cannot emit false skew into the fleet.
- Pair test machines with an **atlas-dev** setup code (the code carries its host)
  so no phantom machine lands in the production org.

## 12. Out of scope, and what stays

- **`onboard.command` is NOT deleted.** It remains the fallback (§8), the MDM
  path, and the thing that keeps this change from being able to break the
  install that works today.
- **`keld-agent install --code` is unchanged** — MDM and `install.sh` depend on it.
- **Windows.** `onboard.cmd` has the identical defect. Everything new here except
  the ObjC lives in Go behind `--json`, so the Windows half is an Inno wizard page
  consuming the same events. Not built here.
- **The desktop app.** Pre-alpha, deliberately not part of this flow and not
  named in any user-visible string. When it ships it restores the possibility of
  POST-install UI, which §3 #5 rules out for the wizard — at which point the
  progress of a still-running download can be shown after the wizard closes.
