# Installing Signal unattended

Every installer Keld ships has a **silent mode**, and every onboarding step has
a **command equivalent**. This page is the list: the silent invocation per
artifact, what each one verifies, and what an MDM rollout runs.

That rule is **AC-12** of the Signal Integrations spec
(`docs/superpowers/specs/2026-09-15-signal-integrations-discovery.html`), and it
is not only about CI. A wizard cannot be driven at scale and cannot be driven by
a test: Playwright covers our own page, and nothing we own can click Apple's
Installer, Inno's pages or a native window. **An onboarding step that exists
only as a window is untestable and undeployable at once.** The wizards are
shells around these same commands.

---

## The three artifacts

| OS | artifact | silent invocation |
|---|---|---|
| macOS | `keld-<version>-<arch>.pkg` | `sudo installer -pkg keld-…-arm64.pkg -target /` |
| Linux | `keld_linux_<arch>.tar.gz` + `keld-agent-sidecar_linux_<arch>.tar.gz`, fetched by `scripts/install.sh` | `curl -fsSL …/install.sh \| sh -s -- --code <CODE>` |
| Windows | `keld-setup.exe` (Inno) | `keld-setup.exe /VERYSILENT /SUPPRESSMSGBOXES /NORESTART /LOG=<path>` |

And the three onboarding commands, identical on every OS:

```
keld login --code <CODE>        # redeem a one-time setup code; no browser
keld signal setup --yes         # point the AI tools at the daemon, with backups
keld-agent install              # register the per-user service and start it
```

`keld-agent install --code <CODE>` runs all three in one call. That is what the
installers themselves invoke, and it is the single command an MDM payload needs
once the package is on the machine.

---

## macOS — the pkg

```bash
sudo installer -pkg keld-3.0.0-arm64.pkg -target /
keld login --code "$CODE"
keld signal setup --yes
keld-agent install
```

⚠️ **The silent path does NOT use the onboarding pane, and must not be written
as if it could.** Since 2026-09 the pkg carries a custom Installer.app section
(`installers/macos/plugin/`) ordered **before** the install step: it redeems the
setup code, downloads the analysis sidecar with a progress bar, and asks which
AI tools to configure. `installer -pkg` runs no UI at all, so:

- the pane never runs and writes no handoff file;
- `scripts/postinstall` therefore sees `paired=false` and **configures no
  tools**, which is correct — it must not act on a choice nobody made;
- it still symlinks the CLIs, kicks off the sidecar fetch, and runs
  `keld-agent install`, so the machine ends up **registered and unpaired** — the
  documented `awaitConfig` idle state, not a crash;
- its `onboard.command` fallback opens a **Terminal window**, which an
  unattended machine cannot answer either.

So the three commands above are not a convenience: they are the only way to
finish a silent macOS install. `keld signal setup` is what configures the tools
the pane would otherwise have collected.

**What the pkg puts where:** `/usr/local/keld` (binaries + `VERSION` +
`onboard.command`), symlinks in `/usr/local/bin`, `/Applications/Keld
Signal.app`. The **analysis sidecar is not in the payload** — Apple's notary
scans every one of its ~15,000 files — so `postinstall` fetches it into
`~/.local/bin` in the background. A machine without it yet is *late*, not
broken: enrichment spools until it lands.

## Linux — install.sh

```bash
curl -fsSL https://atlas.keld.co/signal/install.sh \
  | sh -s -- --code "$CODE"
```

Environment it honours: `KELD_SETUP_CODE` (instead of `--code`), `KELD_API_URL`
(pair against a specific host), `KELD_INSTALL_DIR` (default `~/.local/bin`),
`KELD_RELEASES_URL` (the release mirror, default `https://dl.keld.co`), and
`KELD_RELEASE_TAG` and `KELD_DOWNLOAD_BASE` (pin a version, or install from a
mirror — which is how the conformance harness installs a local build).
`KELD_DOWNLOAD_BASE` replaces the channel level too: it is the directory that
holds `<tag>/`, e.g. `https://dl.keld.co/releases`, not `https://dl.keld.co`.

Pre-release (`-rc.N`) builds live under `prereleases/` on the mirror and expire
after 30 days, so pinning one — or a machine running one fetching its own
version's engine — stops working after that. Never put a `-` in a stable tag:
every consumer, the publish workflow included, reads it as a pre-release.

With a code it onboards non-interactively; without one it installs, registers
the service, and prints the two commands that finish it. It **aborts** if the
analysis sidecar cannot be installed — without it Keld derives nothing from a
transcript — and it refuses on a **checksum mismatch**, while a *missing*
published checksum is a warning (see the reasoning in the script).

## Windows — the Inno installer

```powershell
keld-setup.exe /VERYSILENT /SUPPRESSMSGBOXES /NORESTART /LOG=%TEMP%\keld-setup.log
keld-agent.exe install --code "%CODE%"
```

`/VERYSILENT` rather than `/SILENT`: the latter still shows a progress window,
and the payload is the frozen sidecar's ~15,000 files, so that window lives for
minutes on a machine nobody is watching.

⚠️ **What the silent path runs, and what it deliberately does not.** The `.iss`
has two `[Run]` entries. The first — `keld-agent.exe install --headless` — has
no `skipifsilent` and **always** runs: it writes the v2 config, registers the
`KeldAgent` logon task, starts the daemon and prompts for nothing. It is
unconditional because putting registration behind the postinstall checkbox meant
an MDM `/SILENT` push installed the files and **registered nothing**. The
second — `onboard.cmd` — *is* `skipifsilent`, so no console opens at a human who
is not there. A silently-installed machine is therefore registered and unpaired,
and `keld-agent install --code <CODE>` from the management tool finishes it.

Unlike macOS, the Windows payload **bundles** the sidecar, so there is nothing
to fetch afterwards.

---

## Verifying — from observed state, never an exit code

`keld-agent install` can exit 0 having only registered the service. Every
Windows machine did exactly that for a whole release: the task was created, the
daemon idled on `awaitConfig`, nothing was collected and nothing said so. So a
deployment is checked by reading state, not statuses:

| what | macOS | Linux | Windows |
|---|---|---|---|
| binaries present **and runnable** | `/usr/local/keld/keld --version` | `~/.local/bin/keld --version` | `%LOCALAPPDATA%\Programs\keld\keld.exe --version` |
| service registered | `~/Library/LaunchAgents/co.keld.agent.plist` + `launchctl print gui/$(id -u)/co.keld.agent` | `~/.config/systemd/user/keld-agent.service` + `systemctl --user is-enabled keld-agent.service` | `Get-ScheduledTask KeldAgent` (or `schtasks /Query /TN KeldAgent`) |
| **onboarded** | `~/.keld/hook.json` holds a non-empty `ingest_token` | same | same (`%USERPROFILE%\.keld\hook.json`) |
| analysis sidecar | `~/.local/bin/keld-agent-sidecar` — fetched in the background, so *late* is not *failed* | same, but **required**: install.sh aborts without it | bundled under `%LOCALAPPDATA%\Programs\keld` |

`keld signal doctor` reports the same facts for a human, plus tool wiring and
version skew.

**Running the binary matters.** A wrong-architecture build is present, hashes
correctly and cannot execute — which is why the auto-updater runs
`keld-agent --version` on a staged binary before any swap, and why these checks
do the same rather than `test -x`.

---

## The scripts that do all of this

| script | what it does |
|---|---|
| `scripts/conformance/install-macos.sh` | `sudo installer -pkg … -target /` → the three commands → verify |
| `scripts/conformance/install-linux.sh` | `scripts/install.sh`, run verbatim (a local artifacts dir is served on loopback, so the installer under test is the file users run) → the three commands → verify |

Each prints its step, fails loudly with the step named, and ends by printing
`bin_dir=` / `sidecar_dir=`. They are what the conformance chain uses
(`run-chain.sh --artifact dir:<path>`) and what `.github/workflows/conformance.yml`
runs on a Signal release.

⚠️ **They refuse to run on a machine that has not declared itself disposable**
(`KELD_CONFORM_DISPOSABLE=1`, or `CI=true`). A package install and a service
registration are machine-wide: `KELD_HOME` isolates `~/.keld` but **not** the
service path, which `service.Install` resolves from `os.UserHomeDir()` — on a
developer's machine this repoints the LaunchAgent they actually use. Run them in
a VM (`scripts/conformance/tart/`), a container
(`scripts/conformance/compose/`), or CI.

⚠️ **Verified state, as of 2026-09-16.** The macOS and Linux scripts have been
run end to end against a **local fake release** (inert stand-in binaries), which
proves the argument handling, the loopback artifact server, `install.sh`'s
unattended path and all four verification outcomes — including the important
one: with every command exiting 0 and no `hook.json` written, the script still
fails, at `verify-onboarded`. They have **not** yet been run against a real pkg,
and the CI step that would do it fires only on a Signal release. The first CI
run on a Signal release is the test.

⚠️ **Windows is not in that table and is not a gap.** `keld-setup.exe` is run
`/VERYSILENT` by `.github/workflows/conformance.yml` itself, on both chains of
every Windows cell — chain B installs two published releases in order, because
Windows ships the sidecar only inside that .exe. One installer owner per job, so
there is deliberately no Windows sibling of the two scripts above.

---

## MDM / fleet rollout

1. Distribute the artifact (`.pkg` via your MDM's package payload, `keld-setup.exe`
   with `/VERYSILENT`, or `install.sh` through your config-management tool).
2. Hand each machine a one-time setup code and run
   `keld-agent install --code <CODE>` **as the logged-in user** — not as root.
   The service is per-user (LaunchAgent / systemd `--user` / a logon task) and
   `~/.keld` is the user's; running it as root registers it in root's home,
   where it never loads.
3. Check `~/.keld/hook.json` for an `ingest_token`. That is the only fact that
   says the machine will collect anything.

`--code` also reads `$KELD_SETUP_CODE`, so a payload can pass it as an
environment variable rather than on a command line other users can see.
