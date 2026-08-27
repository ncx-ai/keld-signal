# What the installers must do for v2

The installers were built for v1: fetch two Go binaries and a frozen GLiNER2 sidecar, onboard,
register a service. v2 is a different product — `ml_backend:"deterministic"` with the block
emitter on — and **not one line of any installer knows that**. This says what changes.

Scope was agreed with the maintainer: items 1-5 below, plus the AGENTS.md corrections. A Linux
native package (.deb/.rpm) was considered and explicitly left out — it is new surface, not a v2
correction.

## 0. What v2 is, stated once

Two settings, and they are the whole difference:

```json
{ "ml_backend": "deterministic", "blocks": true }
```

`deterministic` runs the facets that need no model — credential detection, the PII scan, and the
workstream dimensions `/analyze` derives from transcript coordinates — plus dynamics and the
session prior. `blocks` turns on the v2 emitter, which is the unit the whole thing exists to
produce. GLiNER2 is never loaded and its ~1.9 GB weights are never fetched.

⚠️ **The code default stays `"auto"`.** This is deliberate and it is not a hedge. `ml_backend`'s
zero value means auto, so every machine an installer never writes to — a binary upgraded in place,
`go run`, CI, the eval harness — keeps the full ML facet set, `TestBuiltInPipelineStillDemandsAModel`
keeps passing unchanged, and Atlas's Context column keeps rendering `function_guess` / `subcategory`
/ `activity_type` for that population. Phase 5 of `2026-08-25-signal-block-pipeline-design.md` (the
default flip) stays unstarted and gated on Atlas. **The installer opts machines in; the compiler
does not.**

⚠️ **`ml_backend` has NO remote override.** It is local and startup-only (`settings.go:38`), and
nothing in `agentcfg/` touches it. Atlas cannot move a machine between modes, cannot roll one back,
and cannot fix a machine written wrong. The installer is the only lever that exists. Everything
below follows from that: the write has to be correct, idempotent, and it has to actually take
effect in the same run.

## 1. The config write — one seam, not three

**`keld-agent install` (`internal/agentcli/agentcli.go:110`, `runInstall`) is the single choke
point.** All four installer paths call it:

| path | call |
|---|---|
| `scripts/install.sh` | `keld-agent install [--code X]` |
| `scripts/install.ps1` | `& $agent install [--code $Code]` |
| `installers/macos/onboard.command` | `"$AGENT" install --code "$CODE"` |
| `installers/windows/keld-agent.iss` | `[Run] keld-agent.exe install` |

So the write goes in Go, once, and all four inherit it. A JSON merge implemented three times over
in POSIX sh, PowerShell and Inno Pascal would be three chances to corrupt a user's config file.

**New: `settings.WriteInstallDefaults(mode string, blocks bool) error`** in
`internal/agent/settings`. It must:

- **Merge, never overwrite.** Decode the existing file into `map[string]json.RawMessage`, set the
  two keys, re-encode. An operator's `pii_regions`, `include_entity_text` or `features` must
  survive an installer run. An absent or unparseable file starts from empty rather than failing —
  same posture as `Load`, which keeps zero-value defaults on invalid JSON.
- **Write atomically at 0600**, temp file + rename in `KeldHome()`, matching the rest of `~/.keld`.
- **Be idempotent.** Re-running produces byte-identical output.

**Placement: the FIRST thing `runInstall` does**, before the login branch and therefore before the
`default:` headless branch too — a machine that only registers the service still needs the right
config.

⚠️ **Why placement is load-bearing, and why it happens to be safe.** `ml_backend` is read at daemon
STARTUP and never re-read. Writing it to a file next to an already-running daemon changes nothing
until that daemon restarts. `runInstall` ends with `installService()`, and all three implementations
restart:

- darwin — `launchctl bootout` then `bootstrap` (`service_darwin.go:38-39`)
- linux — `systemctl --user restart` (`service_linux.go:35`)
- windows — `schtasks /End` then `/Run` (`service_windows.go:28-29`)

so config-then-install picks it up in the same run. **This is not automatic and must not be
refactored away.** The macOS path is the proof: `installers/macos/scripts/postinstall` runs
`launchctl kickstart -k` on the agent BEFORE it opens `onboard.command`. A daemon is therefore
already running, on `auto`, by the time the config is written — and without the restart at the end
of `runInstall` that machine would run v1 behaviour until the user next logged out.

**`--backend <auto|deterministic|off>` on `keld-agent install`**, default `deterministic`. Two
reasons it exists rather than the value being hardcoded: a support path for putting a machine back
on `auto` without hand-editing JSON, and an escape hatch for the consequence below.

⚠️ **`make install-linux` routes through this too** (`Makefile:67` → `keld-agent install`), so a dev
machine converges on `deterministic` like everyone else. That is the right default — a dev machine
should run what users run — but it means the local ML pipeline stops exercising GLiNER2 unless the
developer passes `--backend auto`. `make install-linux` prints which mode it wrote.

⚠️ **A re-install FLIPS an existing `auto` machine.** Decided deliberately: the write is
unconditional, so every re-install converges. The cost is named here rather than discovered later —
an existing fleet's Atlas Context column empties machine-by-machine as people upgrade, at whatever
pace they upgrade, with no server-side control over the pace, because `ml_backend` has no remote
override. If that pace needs controlling, the control has to be a staged rollout of the installer
itself.

## 2. `blocks` needs a settings key before an installer can reach it

`blocks.Enabled()` (`blocks/emitter.go:425`) reads `KELD_BLOCKS` and nothing else. **An env-only
toggle is structurally unreachable from an installer**: `LaunchAgentPlist` and `SystemdUnit`
(`service/service.go`) carry no environment block at all, and the Windows scheduled task is a bare
`/TR "<exe>" run`. There is nowhere for an installer to put `KELD_BLOCKS=1` that the daemon would
see.

So `blocks.Enabled()` becomes **env > `agent-config.json` > off**, which is the exact shape
`features/toggle.go` already uses for `KELD_FEATURES` (`env > agent-config.json > off`, with the
config value passed in by the caller rather than read by the toggle package — keep that structure,
it is what stops `blocks` importing `settings`).

- `Settings.Blocks bool \`json:"blocks"\`` — local, like `Features`.
- `KELD_BLOCKS` still wins, so an operator can disable a machine without editing the file.
- Default stays off. The compiled-in default is not touched, for the same reason `ml_backend`'s is
  not.

**No remote override, for now.** `Remote.Features`/`Remote.FeaturesPublish` have one; `blocks` does
not, and adding one is a separate decision with its own Atlas-side work. Worth noting the asymmetry
that creates: Atlas can turn feature rows off fleet-wide and cannot turn blocks off.

## 3. The sidecar is the analysis service, not "the ML sidecar"

Every installer describes the sidecar as GLiNER2, and under v2 that description is false — the
model is never loaded. Worse, the *reasoning* attached to it is now backwards. `install.sh` aborts
the entire install on a sidecar failure with:

> Keld Signal requires on-device ML and has no deterministic fallback. Aborting.

Under v2 there is nothing but the deterministic path, and the sidecar is *more* essential than that
sentence claims: without it there is no `/analyze`, no `/ingest`, no `/blocks` — a v2 install with
no sidecar produces **nothing at all** except credential detection. So:

- **The abort stays.** The conclusion was right; only the reason was wrong.
- **The reason changes** to what is actually true: the sidecar is the client-side analysis service,
  it is what turns transcripts into blocks and workstreams, and Keld collects essentially nothing
  without it.
- **Drop "(GLiNER2)" from the progress lines.** `install.sh:232`, `onboard.command`'s
  `fetch_sidecar`, and the `.iss` comment header.
- **`onboard.command`'s non-fatal posture becomes inconsistent with `install.sh`'s abort** and
  should be reconciled — same product, same missing component, two different verdicts. Recommend
  `install.sh`'s: fail loudly. (`onboard.command` cannot abort a pkg install that already
  completed, so "loudly" there means a clearly-marked failure and a re-run instruction, which it
  already has — the divergence is in the *wording*, not the mechanism.)

## 4. Say that no model download happens

`Makefile:73-74` currently tells the user:

> keld-agent installed WITH sidecar (deterministic works instantly; the first ML enrichment
> provisions the model ~1.9GB into ~/.keld/models, then the sidecar takes over).

Under a `deterministic` install that second clause never happens. The installers should say the
true and genuinely good thing instead: **no multi-gigabyte model download, ever, on a default
install.** This is the most user-visible improvement in the whole change and no installer currently
mentions it.

`keld signal doctor` / `status` need **no change**: `localagent.GLiNER2State` already returns
`Needed: false` under `deterministic`, and `ProblemLine`/`StatusLine` already stay silent for an
absent-and-unneeded model. That discipline was built for exactly this moment and it holds.

## 5. Windows has no onboarding at all

`installers/windows/keld-agent.iss:31-32`:

```
[Run]
Filename: "{app}\keld-agent.exe"; Parameters: "install"; Flags: runhidden nowait postinstall
```

**The window is hidden, so onboarding cannot happen — and it is broken either way
`stdoutIsTTY()` resolves.** If it reports false, `runInstall` takes its `default:` branch and
prints "Finish setup by running: keld login && keld signal setup" into a window nobody sees. If it
reports true (a hidden console is still a console; this is unverified without a Windows host and
the spec does not depend on which), it launches an interactive browser device-flow login that no
one can see or complete. `nowait` means Inno does not wait for it and cannot report either outcome.

Either way the user gets a running daemon that idles forever on `awaitConfig`, having never logged
in. Nothing is collected and nothing says so.

⚠️ **AGENTS.md claims this is already solved** — it describes a `[Code]` post-install wizard page
driving `keld --json` with a WinAPI timer and async NDJSON polling. `git log` on the file shows two
commits, neither of which added it. **The page has never existed.** The docs describe an intention.

**Recommended fix: parity with macOS, not the docs' design.** macOS opens a visible Terminal window
running `onboard.command`, which prompts for the setup code and calls `keld-agent install --code`.
Windows should do the same thing with a visible console:

```
[Run]
Filename: "{app}\keld-agent.exe"; Parameters: "install"; \
  Description: "Set up Keld"; Flags: postinstall shellexec
```

plus a small `onboard.cmd` staged beside the binaries (the `onboard.command` equivalent) that
prompts for the code, runs `keld-agent install --code %CODE%`, falls back to interactive on
failure, and reports success from **observed state** — an `ingest_token` in `hook.json` — exactly
as its macOS and shell siblings already do. Dropping `runhidden` is what makes `stdoutIsTTY()` true
and gets `runInstall` onto its interactive branch.

Why this over the NDJSON wizard page AGENTS.md describes: it is one small script against several
hundred lines of Inno Pascal driving an async temp-file poll; it reuses the Go seam that already
works on two platforms; and onboarding then has ONE shape across all three OSes instead of two. The
wizard page is a nicer UX and remains the aspiration — it should be written down as such rather
than as something that exists.

⚠️ **Only human-verifiable on Windows.** No CI check can confirm a console window appeared and a
human pasted a code. The verification plan is: CI compiles the `.iss` (already does), a unit test
pins `runInstall`'s branch selection, and a human runs the built `keld-setup.exe` once.

## 6. AGENTS.md corrections

Three, and the first is the one that matters:

1. **The Windows onboarding wizard page does not exist.** Replace the description with what is
   actually there. A doc that describes unbuilt code as built is worse than no doc — it is what
   stopped this gap being found for two releases.
2. **The sidecar's role in the installers** — it is the analysis service; GLiNER2 is a capability
   it loads lazily and, under a v2 install, never.
3. **A new "What a v2 install lands on" note** near the Model backends bullet: the two keys, that
   the code default stays `auto`, that `ml_backend` has no remote override so the installer is the
   only lever, and that a re-install flips an existing machine.

## Not in scope

- A Linux native package. Linux stays `curl | sh`.
- Flipping the compiled-in default (Phase 5, gated on Atlas).
- A remote override for `blocks`.
- `KELD_TICK`, `KELD_CAPTURE`, `KELD_TEXTEMBED`, `KELD_FEATURES`, `KELD_FEATURES_PUBLISH` — all
  stay off and stay unreachable from installers.
- macOS notarization. The pkg still ships without the sidecar and that is still right: the sidecar
  is larger and more necessary under v2, which strengthens rather than weakens the argument for
  fetching it out of band.

## Risks, named

- **The flip is irreversible from the server.** No remote override means a bad write needs a new
  installer run on every affected machine. Mitigated only by `--backend auto` existing as a manual
  path.
- **Atlas's Context column empties as the fleet upgrades**, uncontrollably paced. Known, accepted,
  restated here so nobody reads it as a regression.
- **Blocks accumulate in a table nothing reads.** Atlas stores them (201) but has no consumer yet.
  `emitter.go:390-398` says the repo's rule is that such rows are opt-in and announced; turning
  them on by default for v2 installs is a deliberate departure from that rule, made because blocks
  ARE v2 and the alternative is shipping v2 that produces nothing.
- **Windows remains partially reaped.** `procgroup_windows.go` cannot deliver SIGTERM from a
  console-less service, so `lifespan` teardown never runs there. Unchanged by this work; a v2
  Windows machine leaks sidecar children on restart exactly as a v1 one did.
