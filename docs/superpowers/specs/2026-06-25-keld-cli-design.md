# keld CLI — Design

**Date:** 2026-06-25
**Status:** Approved (brainstorming)
**Repo:** `keld-cli` (`git@github.com:ncx-ai/keld-cli.git`)

## 1. Purpose & Scope

`keld` is a provider-agnostic Python CLI that sets up a Keld user's local
environment for telemetry. It authenticates a user against the Keld Atlas
backend, then merges OTLP telemetry configuration and hook settings into each
detected coding tool's config file, and installs the `keld-context` hook
script — **idempotently** (safe to re-run) and **reversibly** (uninstall
removes exactly what Keld added).

The CLI is the local-environment counterpart to the server-side telemetry
onboarding that Atlas performs today via `telemetry_snippets.py` and the hook
served at `/v1/tool-context/hook.py`. Instead of users hand-copying snippets
from the web UI, the CLI does the merge, the diff, and the install for them.

### Provider-agnostic principle

Keld never bakes in assumptions about a single coding tool or telemetry
source. Every shared piece is designed for N tools. v1 ships adapters for
**Claude Code**, **Codex**, and **Gemini CLI**; adding another tool is a new
adapter + a registry entry, mirroring the `Provider` protocol pattern already
used in keld-atlas (`services/api/app/providers/`).

### v1 Subcommands

Auth commands are **top-level** and shared across Keld product groups (so a
future group needing auth reuses the same `keld login`). Telemetry onboarding
lives under the **`keld signal`** group (Keld Signal is the telemetry product),
since the CLI will grow to cover more than telemetry setup over time.

| Command | Purpose |
| --- | --- |
| `keld login` | Authenticate via browser device flow; store credentials. |
| `keld logout` | Delete stored credentials. |
| `keld whoami` | Show the logged-in principal, org, and OTLP endpoint. |
| `keld signal setup` | The core flow: detect tools, diff, merge config, install hook. |
| `keld signal status` | Inspect local state per tool (configured? hook current?). |
| `keld signal doctor` | Diagnose problems (config validity, hook version, reachability). |
| `keld signal uninstall` | Reverse setup: strip Keld-managed entries, remove hook. |

Implementation note: `login`/`logout`/`whoami` register on the root Typer app;
`setup`/`status`/`doctor`/`uninstall` register on a `signal` sub-Typer added via
`app.add_typer(signal_app, name="signal")`. The command *functions* are unchanged
by the grouping.

Out of scope for v1: managing provider Admin keys, viewing usage/spend, any
read of telemetry data. The CLI only manages local onboarding.

## 2. Stack & Packaging

- **Language:** Python 3.12 (matches keld-atlas).
- **CLI framework:** [Typer](https://typer.tiangolo.com/) (Click-based) — idiomatic
  subcommands, type-hint driven, automatic help. *Alternatives considered:
  raw Click (more boilerplate), argparse (poor ergonomics).*
- **HTTP:** httpx.
- **Output:** rich (Typer-integrated) for diffs, tables, spinners.
- **TOML:** stdlib `tomllib` for validation; the Codex managed block
  (`# >>> keld` / `# <<< keld`) is built and stripped as a delimited text
  region, so no third-party TOML writer is needed.
- **JSON:** stdlib `json` with stable key ordering and 2-space indent.
- **Packaging:** hatchling build backend, published to **PyPI**. The single
  PyPI artifact supports pip, pipx, and uvx simultaneously. **Documented
  default install: `pipx install keld`** — isolated and reliable on every
  platform, and it sidesteps the `externally-managed-environment` (PEP 668)
  failure that bare `pip install` hits on modern distros (Manjaro, Debian,
  macOS/Homebrew). `pip install keld` (inside a venv) and `uvx keld` /
  `uv tool install keld` are documented alternatives. All runtime
  dependencies are pure-Python wheels (no compiled parts), so isolation is the
  only consideration. Console entry point: `keld = keld.cli:main`.

## 3. Provider-Agnostic Tool Adapters

The central abstraction. Each supported coding tool implements a `ToolAdapter`,
mirroring Atlas's `Provider` protocol so the pattern is familiar to anyone who
has worked in keld-atlas.

```python
class ToolAdapter(Protocol):
    name: str                          # "claude_code" | "codex" | "gemini"
    display_name: str                  # "Claude Code", "Codex", "Gemini CLI"

    def detect(self) -> bool: ...          # is the tool installed / config dir present?
    def config_path(self) -> Path: ...     # ~/.claude/settings.json, etc.
    def apply(self, params: SetupParams) -> ChangeSet: ...   # compute telemetry+hook merge
    def remove(self, manifest: ToolManifest) -> ChangeSet: ...  # strip Keld-managed entries
    def status(self, manifest: ToolManifest | None) -> ToolStatus: ...
```

- **Registry:** `{claude_code, codex, gemini}`. Adding a tool = new adapter +
  register.
- **`SetupParams`** carries the values fetched from Atlas: `endpoint`,
  `ingest_token`, `actor` (the user's email principal).
- **`ChangeSet`** is a computed, displayable description of edits (keys added,
  hook entries, block ranges) — produced first so it can be rendered as a diff
  *before* anything is written. `apply()` returning a ChangeSet and the actual
  write are separate steps.

### Per-format marker strategy

| Format | Tools | How Keld-managed entries are marked |
| --- | --- | --- |
| JSON | Claude Code, Gemini CLI | Telemetry env keys are well-known `OTEL_*` / `CLAUDE_CODE_ENABLE_TELEMETRY` names; hook entries identified by their `command` referencing `keld-context.py`. A `_keld` sentinel object records the managed key list for unambiguous removal. |
| TOML | Codex | Keld block wrapped in `# >>> keld` / `# <<< keld` comment delimiters, built and stripped as a delimited text region (stdlib `tomllib` validates the result); hook array entries live inside that block. |

JSON and TOML adapters share deep-merge helpers in `config/merge.py` but differ
only in the marker mechanism above.

## 4. Authentication

### Mechanism: browser device flow

`keld login`:

1. Initiates a **device/OAuth flow** against Atlas: requests a device code,
   opens the user's browser to the verification URL, and polls until the user
   authorizes (or it times out).
2. On success, receives a CLI access token (long-lived, revocable) and the
   user's identity (email/principal + org).
3. Stores credentials at `~/.keld/auth.json`, file mode `0600`.

`whoami` prints principal + org + OTLP endpoint. `logout` deletes
`~/.keld/auth.json`.

### Lazy / automatic auth

Auth is **lazy**: any command that requires authentication (e.g. `setup`)
checks for valid stored credentials and, if absent or expired, transparently
runs the device-flow login inline, then continues the original command. There
is no dead-end "please run `keld login`" error in interactive use.

- `--no-login` flag forces a clean non-interactive failure instead of opening a
  browser (for CI / scripted contexts).
- A non-interactive environment (no TTY / no browser) with missing creds and no
  `--no-login` fails with a clear, actionable message.

### Backend dependency (out of this repo)

The device-code issue + poll/verify endpoints, the CLI-token issuance, and the
authenticated "fetch my org's endpoint + ingest token + principal" endpoint do
**not** exist in keld-atlas yet. This spec defines the **contract** the CLI
expects; the Atlas-side implementation is tracked as a separate companion
spec/plan in the keld-atlas repo. The CLI's `api/client.py` is written against
that contract and is independently testable with a mocked backend.

**Expected API contract (CLI's view):**

- `POST /v1/cli/device/start` → `{ device_code, user_code, verification_url, interval, expires_in }`
- `POST /v1/cli/device/poll` `{ device_code }` → `202` pending | `200 { access_token, principal, org }`
- `GET  /v1/cli/onboarding` (auth: bearer access_token) → `{ endpoint, ingest_token, actor }`
- Hook download (existing): `GET <endpoint>/v1/tool-context/hook.py?token=<ingest_token>`

## 5. `keld signal setup` Flow

1. **Ensure auth** (lazy): valid creds, else run device flow inline (unless
   `--no-login`).
2. **Fetch onboarding values** from Atlas: `endpoint`, `ingest_token`, `actor`.
3. **Detect** installed tools. `--tool claude_code,codex` overrides detection to
   an explicit set.
4. For each selected tool, compute a `ChangeSet` and render a **diff** of the
   intended edits, plus the hook install.
5. **Confirm** (skip with `--yes`; stop after diff with `--dry-run`).
6. **Apply**, per file, atomically:
   - Back up the file on first write (`<file>.keld.bak`).
   - Write to a temp file + atomic rename (never edit in place).
   - Merge telemetry `env` + `hooks` under managed markers.
   - Download the prebaked `keld-context` hook from
     `<endpoint>/v1/tool-context/hook.py?token=<ingest_token>` →
     `~/.keld/keld-context.py`.
   - Record edits in `~/.keld/manifest.json`.
7. Re-running `setup` updates in place (idempotent: apply-twice == apply-once).

### Manifest (`~/.keld/manifest.json`)

Records, per tool: config file path, the exact keys/hook-entry ids written, and
the block range (for TOML). Records the hook script path + version hash.
The manifest is the source of truth that makes `uninstall` precise and
`status` accurate.

## 6. `status` / `doctor` and `uninstall`

- **`status`** — reads the manifest + live config and reports per tool:
  configured? hook installed and current (local hash vs latest)? Plus endpoint
  reachability.
- **`doctor`** — deeper diagnostics: config file parse validity, drift between
  manifest and actual file contents, hook version staleness, auth validity,
  endpoint reachability with latency. Emits actionable fixes.
- **`uninstall`** — uses the manifest + markers to remove exactly the
  Keld-managed entries from each config, deletes `~/.keld/keld-context.py`, and
  clears the manifest. `--tool` scopes it to specific tools.

## 7. Repository Structure

```
keld-cli/
  pyproject.toml            # hatchling; entry point: keld = keld.cli:main
  README.md
  src/keld/
    cli.py                  # Typer app; registers subcommands
    commands/
      login.py              # login, logout, whoami
      setup.py              # setup
      status.py             # status, doctor
      uninstall.py          # uninstall
    auth/
      device_flow.py        # browser device-code flow
      store.py              # ~/.keld/auth.json read/write (mode 0600)
    api/
      client.py             # httpx client to Atlas (against §4 contract)
    tools/
      base.py               # ToolAdapter protocol + ChangeSet/ToolStatus types
      claude.py             # Claude Code  (~/.claude/settings.json, JSON)
      codex.py              # Codex        (~/.codex/config.toml, TOML)
      gemini.py             # Gemini CLI   (~/.gemini/settings.json, JSON)
      registry.py           # adapter registry
    config/
      manifest.py           # ~/.keld/manifest.json read/write
      merge.py              # JSON deep-merge + TOML managed-block helpers
    hook.py                 # download/install keld-context hook
    console.py              # rich output helpers (diffs, tables, prompts)
  tests/
```

Files stay small and single-purpose: each adapter owns one tool's format;
merge mechanics live in `config/merge.py`; the manifest is the single
read/write seam for installed state.

## 8. Error Handling

- **Never corrupt a user file.** Write temp + atomic rename. Back up before the
  first write. If a config file fails to parse, refuse to edit it and report
  clearly rather than overwriting.
- **All-or-nothing per file.** A failed merge for one tool does not leave that
  file half-edited; other tools proceed independently and the summary reports
  per-tool outcomes.
- **Network/auth failures are explicit and actionable** — never silent, never a
  stack trace as the primary message.
- The installed hook itself remains non-blocking to the coding tools (its
  existing behavior: log to stderr, exit 0); the CLI does not change that.

## 9. Testing Strategy

- **pytest**, with an isolated temp `HOME` fixture so no real user config is
  touched.
- **Golden fixtures** per adapter: before/after config files for clean-install,
  install-onto-existing-user-config, and re-install cases.
- **Idempotency test:** apply twice == apply once (byte-identical).
- **Round-trip test:** apply → remove == original file (byte-identical to a
  pre-existing user config).
- **Mocked Atlas backend** (httpx transport) for the device flow and onboarding
  fetch — the CLI is testable without a live backend.
- **Marker/manifest tests:** uninstall removes only Keld-managed entries when
  the user has their own unrelated `env`/`hooks` present.

## 10. Open Backend Work (companion, in keld-atlas)

Tracked separately, required before `keld login` works end-to-end:

1. CLI device-flow endpoints (`/v1/cli/device/start`, `/v1/cli/device/poll`).
2. CLI access-token issuance + verification (revocable).
3. Authenticated onboarding endpoint returning `{ endpoint, ingest_token, actor }`.
4. A verification page in the Atlas web UI for the device `user_code`.
