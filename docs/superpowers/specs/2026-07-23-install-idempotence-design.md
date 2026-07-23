# Bulletproof install / PATH-safe hooks — design

**Date:** 2026-07-23. **Goal:** a `keld` install is idempotent and PATH-safe —
no stale `keld` binary can shadow the release or hijack the tool hooks, and the
running daemon is always the just-installed build.

## Problem (observed)

`keld` ships via two channels that write to different dirs:
- `scripts/install.sh` (curl|sh) → `~/.local/bin` (or `$KELD_INSTALL_DIR`).
- macOS `.pkg` → `/usr/local/keld/keld`, symlinked at `/usr/local/bin/keld`.

Running both over a machine's lifetime leaves two `keld` binaries on `PATH`.
Whichever sorts earlier on `PATH` wins for **everything invoked by name** — the
user's CLI *and* the tool hooks (`command: keld __hook --source <tool>`). A real
case: an old curl-install left `keld v0.8.0` in `~/.local/bin`, the `.pkg`
installed `v0.11.1` in `/usr/local/bin`, `~/.local/bin` sorted first → the v0.8.0
hook ran and emitted the (since-removed) context POST → `HTTP 405` on every
Gemini prompt. Telemetry was unaffected (it lives in settings.json, not the
binary), which is why only the hook misbehaved.

## Design (four parts)

### 1. Pin the hook to an absolute path (Go)
`keld setup` resolves the running binary's canonical path (`os.Executable` +
`filepath.EvalSymlinks`) and writes each tool's hook command as
`<abs>/keld __hook --source <tool>` instead of bare `keld`. PATH order can then
never hijack the hook.
- `telemetry.SetupParams` gains `BinPath string`; `setup.go` populates it.
- `telemetry.HookCommand(binPath, source)` prepends `binPath` (falls back to bare
  `keld` when `binPath == ""`, preserving current behavior for callers/goldens
  that don't set it).
- `HookCommandSubstr = "keld __hook"` is unchanged and still recognizes the
  pinned command (the path ends in `.../keld __hook`), so hook removal/detection
  keeps working.

### 2. Idempotent install — repoint strays to canonical (shell)
On install, stray `keld`/`keld-agent` binaries in the *other* known PATH dirs are
replaced with symlinks to the just-installed canonical binary (**last install
wins**; nothing the user placed is deleted outright — it becomes a symlink to the
current build). Known dirs: `~/.local/bin`, `/opt/homebrew/bin`, `/usr/local/bin`.
- `.pkg postinstall` (canonical = `/usr/local/keld/keld{,-agent}`): repoint strays
  in the console user's `~/.local/bin` and `/opt/homebrew/bin`.
- `install.sh` (canonical = `$DEST/keld{,-agent}`): repoint strays in writable
  known dirs; for a dir that needs root (e.g. `/usr/local/bin`), print the exact
  `sudo ln -sf …` fix rather than failing.

### 3. Replace the running process (shell)
`.pkg postinstall` restarts the per-user agent so the live daemon is the
just-installed build: `launchctl kickstart -k gui/<uid>/co.keld.agent`
(best-effort; onboarding still handles a first-time load).

### 4. `keld doctor` detects drift (Go)
`doctor` walks `PATH`, collects every `keld`, resolves symlinks, and if more than
one *distinct* binary is reachable it reports which one wins and which are
shadowed, with the repoint/remove fix. This is the safety net for anything the
installers can't auto-reconcile (e.g. a root-owned stray during a curl install).

## Testing
- Go (TDD): `HookCommand` pinned vs bare; `SetupParams.BinPath` threaded through
  claude/codex/gemini; goldens stay bare (BinPath unset — the shape is
  environment-independent); doctor PATH-scan detects >1 distinct keld.
- Shell: extend `scripts/test-install-sh.sh` to assert a stray binary in a second
  dir is repointed to canonical; smoke the postinstall repoint logic in isolation.

## Non-goals
- Quoting binary paths that contain spaces (install dirs don't; YAGNI).
- Unifying the two channels into one canonical location (bigger cross-repo change).
