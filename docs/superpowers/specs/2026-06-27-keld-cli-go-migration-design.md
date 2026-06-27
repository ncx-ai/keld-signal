# Keld CLI → Go migration

**Date:** 2026-06-27
**Status:** Approved (design)

## Problem

The Keld CLI ships as a Python package (`pipx install keld` / `uvx keld` / `pip install`).
Non-technical users on the Integrations page have to first install Python, pip, and a
working isolated-environment tool before they can run it — environments differ per
machine and this is the main onboarding friction.

A compiled, dependency-free binary removes that friction: the user downloads one file
and runs it, with no runtime to install.

## Goal

Migrate the entire `keld-cli` to **Go**, producing self-contained per-platform binaries
(macOS arm64/amd64, Linux arm64/amd64, Windows amd64) that require **nothing** on the
user's machine — at install time *and* at runtime.

Non-goal for this work: changes to `keld-atlas` (the backend). The Atlas
`/v1/tool-context/hook.py` endpoint may remain for backward compatibility with
already-installed Python hooks; new installs stop using it.

## Why Go

For a small pure-I/O CLI, Go and Rust both yield single static binaries that need no
runtime — indistinguishable to the end user. Go wins on *shipping* the binary: it
cross-compiles to every OS/arch from a single machine via `GOOS`/`GOARCH` (no per-OS CI
runners, no cross-toolchain setup), with fast dev velocity and a mature CLI ecosystem.
Rust's only edge — a marginally smaller binary — is invisible to users. The codebase is
~1,160 LOC, so the rewrite is small (≈1–2 days of porting).

## Critical finding: the hook is also Python

`keld signal setup` does two things: it edits tool config files, **and** it installs a
telemetry hook. Today the hook is `keld-context.py` (171 lines, stdlib-only Python,
served from `{endpoint}/v1/tool-context/hook.py` with the endpoint+token templated in),
and each tool is wired to run it via `python3 {hook_path}; true`.

So migrating only the CLI would leave a hidden Python runtime requirement: every Claude
Code / Codex / Gemini session would still shell out to `python3`. To truly remove the
Python dependency, the hook must also be ported to Go.

**Decision:** the hook becomes a hidden subcommand of the same binary — `keld __hook`.
Tools are wired to call `keld __hook --source <tool>` instead of `python3 …`. The single
binary is both the CLI and the hook runtime.

## What the hook does (behavior to preserve)

From `keld-atlas/services/api/app/telemetry_hook.py`:

1. Resolve `endpoint`, `token`, `source`. Today endpoint/token are templated into the
   served script (with `KELD_CTX_ENDPOINT` / `KELD_CTX_TOKEN` env overrides);
   `source` comes from `KELD_CTX_SOURCE`. If endpoint or token is missing → exit 0.
2. Read hook JSON from stdin (tolerate malformed → `{}`).
3. `cwd` = `hook_input["cwd"]` or process cwd.
4. Build payload: `session_id` (first of `session_id`/`conversation_id`/`thread_id`),
   `source`, `ts` (UTC ISO-8601), `repo`, `attributes`. No `session_id` → exit 0.
5. `repo`: `git -C cwd remote get-url origin` normalized (strip `.git`, `git@host:org/repo`
   → `host/org/repo`, strip scheme + creds); else basename of
   `git rev-parse --show-toplevel`; else `null`. Git calls time out at 2s.
6. `attributes`: scalar keys (str/int/float/bool → string) from the `[keld]` table of
   `cwd/.keld.toml`; missing/unparseable → `{}`.
7. Dedup: sha256 of `[repo, attributes]` (sorted-keys JSON) stored at
   `~/.keld/state/<session>.sig` (session id sanitized: `/` and `\` → `_`). Unchanged
   since last run → exit 0. On any state-file error, prefer reporting.
8. POST payload to `endpoint` with `content-type: application/json` and
   `x-keld-ingest-token: <token>`, 3s timeout. **Always exit 0** — never block the tool.
   On failure, surface to stderr: full traceback-equivalent in dev, concise one-liner in
   prod. Dev = `KELD_CTX_DEBUG` set to non-empty/non-`"0"`, or endpoint host in
   {localhost, 127.0.0.1, 0.0.0.0, ::1}. Prod one-liner: `HTTP <code>` for HTTP errors,
   the reason for URL errors, else the error type name — never leak payload/token.

## How `keld __hook` gets endpoint + token

The Go binary can't be "templated" the way the served script is. Instead:

- `keld signal setup` writes `~/.keld/hook.json` (mode 0600): `{ "endpoint": …,
  "ingest_token": … }`.
- Tools are wired to run `keld __hook --source claude_code|codex|gemini`.
- `keld __hook` resolves config in this precedence: `KELD_CTX_ENDPOINT` / `KELD_CTX_TOKEN`
  env (dev override) → `~/.keld/hook.json`. Missing either → exit 0 (matches today).
- `KELD_CTX_DEBUG` still honored for dev error verbosity.

This keeps the token in one 0600 file rather than duplicated across each tool's config.

## Architecture (Go)

Mirrors the current Python package layout.

```
keld-cli/
  go.mod                          # module github.com/ncx-ai/keld-cli
  cmd/keld/main.go                # entrypoint → cli.Execute()
  internal/
    cli/        root + `signal` group wiring; KeldError → stderr + exit(1)
    commands/   login, setup, status, doctor, uninstall, hook
    api/        AtlasClient (net/http)
    auth/       store (auth.json, 0600) + device flow
    config/     manifest, writer (atomic temp+rename, backups), merge (json/toml helpers)
    tools/      Adapter interface + claude, codex, gemini
    telemetry/  otel env, gemini telemetry block, codex block body, hook-command strings
    paths/      ~/.keld paths + api-base override
    console/    styled stdout/stderr helpers
    diffview/   unified-diff rendering
  .goreleaser.yaml
  .github/workflows/release.yml
```

Python → Go mapping:

- `ToolAdapter` Protocol → Go `interface`; `ClaudeAdapter`/`CodexAdapter`/`GeminiAdapter`
  → structs implementing it; `ALL_ADAPTERS` registry preserved.
- Dataclasses (`SetupParams`, `Plan`, `ToolStatus`, `Manifest`, `ToolManifest`,
  `HookRecord`, `AuthData`, `DeviceStart`, `Onboarding`) → structs.
- `KeldError` → a sentinel error type; `cli` prints `Error: <msg>` and exits 1.
- Typer commands/flags → cobra commands/flags, identical names and help:
  - top-level: `login`, `logout`, `whoami` (with `--no-login`, `--api-url`).
  - `signal` group: `setup`, `status`, `doctor`, `uninstall`.
  - `setup` flags: `--tool`, `--dry-run`, `--diff`, `--yes/-y`, `--no-login`, `--api-url`.
  - `uninstall` flags: `--tool`, `--yes/-y`.
  - hidden: `__hook --source <tool>`.

## Hook lifecycle changes (vs. Python)

- **Install:** no script download. `setup` writes `~/.keld/hook.json` and wires each tool
  to `keld __hook --source <tool>`. The hook-command substring used for detect/remove
  becomes a stable marker (e.g. `keld __hook`) instead of `keld-context.py`.
- **Manifest:** `HookRecord`'s `sha256`/downloaded-path semantics no longer apply; it
  records that the binary-hook is wired (e.g. version = binary/CLI version) rather than a
  downloaded-file hash. `install_hook` + `AtlasClient.fetch_hook` are removed.
- **Uninstall:** removes each tool's Keld config, then `~/.keld/hook.json` and
  `~/.keld/state/` (no script file to delete). Behavior otherwise matches today
  (delete-if-empty created files, drop `.keld.bak`, clear manifest endpoint/actor/hook).
- **status/doctor:** "hook configured" = `hook.json` present and the binary resolvable;
  doctor flags drift as today.

## Library choices

| Need | Library | Notes |
|---|---|---|
| CLI framework | `spf13/cobra` | maps to `keld` / `keld signal` groups, hidden `__hook` |
| Styled output | `fatih/color` | bold/red/green/dim/cyan + rule lines (replaces `rich`) |
| JSON edits | `iancoleman/orderedmap` | preserves key order — see gotcha below |
| TOML validate/read | `pelletier/go-toml/v2` | replaces `tomllib` (codex validate, `.keld.toml`) |
| Unified diff | `pmezard/go-difflib` | matches Python `difflib.unified_diff` output |
| Open browser | `pkg/browser` | replaces `webbrowser.open` |
| HTTP, files, hashing | stdlib | `net/http`, atomic write, `os.Chmod 0600`, `crypto/sha256` |

**JSON ordering gotcha (key correctness item):** Python's `json.loads`→`json.dumps`
preserves the user's existing key order and appends new keys; Go's `map[string]any`
marshals keys **alphabetically**, which would reorder a user's `settings.json` on every
run and create noisy diffs. `orderedmap` preserves order, keeping output faithful to the
Python CLI. JSON output format: 2-space indent + trailing newline (matches `dump_json`).

## Distribution pipeline

- **GoReleaser** + GitHub Actions, triggered on `v*` tags. One Linux runner
  cross-compiles all targets (darwin arm64/amd64, linux arm64/amd64, windows amd64),
  builds archives + `checksums.txt`, and publishes a GitHub Release.
- **Install UX:** `curl -fsSL keld.co/install.sh | sh` (and a `.ps1` for Windows) that
  detect OS/arch and place the right binary on PATH. The Integrations page links here.
- **Package managers (nice-to-have):** GoReleaser-generated Homebrew tap + Scoop manifest.
- **Signing (later hardening):** macOS notarization + Windows code-signing as optional
  GoReleaser steps (require Apple/Windows certs). Pipeline ships unsigned first; signing
  removes Gatekeeper/SmartScreen warnings and is tracked separately. This is
  language-independent and not solved by the Go migration itself.

## Testing & parity

Port the ~1,190-line pytest suite to Go `testing`:

- HTTP: `httptest.Server` replaces httpx transport mocks for device flow / onboarding.
- Isolation: `KELD_HOME` set to a temp dir per test (mirrors `conftest.py`).
- **Golden-file parity tests:** for representative inputs (existing Claude `settings.json`,
  Codex `config.toml` with and without a conflicting `[otel]`, Gemini `settings.json`),
  assert the Go output is byte-identical to the current Python CLI's output — proving the
  migration doesn't change users' files.
- **Conflict flows:** Codex `[otel]` skip/replace/abort, replace-safety check, dry-run,
  `--yes` auto-skip, `--diff`.
- **New `keld __hook` suite:** repo derivation (origin URL normalization variants;
  toplevel fallback; no-git → null), `.keld.toml` `[keld]` scalar extraction, session
  dedup via sig file, malformed stdin, missing endpoint/token → exit 0, POST failure
  never blocks (exit 0) with dev-vs-prod stderr.

## Risks & mitigations

- **Behavioral drift in config edits** → golden-file parity tests gate the port.
- **JSON key reordering** → `orderedmap`; covered by golden tests.
- **TOML block insert/strip** is string/line-based today (marker comments, table
  stripping); port the string logic verbatim and keep `go-toml/v2` only for validation,
  matching the Python `tomllib`-validate + safety-recheck approach.
- **Already-installed Python hooks** keep working (backend endpoint stays); `uninstall`
  for an old install still cleans tool config via the recorded managed markers.

## Out of scope

- `keld-atlas` backend changes (the served `hook.py` endpoint).
- macOS/Windows code-signing certificate procurement (tracked as follow-up hardening).
- Removing PyPI publishing — can be retired once the binary install path is live.
