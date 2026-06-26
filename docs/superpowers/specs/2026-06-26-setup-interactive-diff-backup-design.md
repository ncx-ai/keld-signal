# Interactive, backed-up, conflict-aware `keld signal setup` — Design

**Date:** 2026-06-26
**Status:** Approved (brainstorming)
**Repo:** keld-cli

## 1. Problem

Running `keld signal setup` against a machine that already had an `[otel]`
table in `~/.codex/config.toml` produced:

```
Claude Code → /home/dg/.claude/settings.json
  + set 6 OTEL env vars
  + add SessionStart + CwdChanged hooks
Error: resulting TOML is invalid: Cannot declare ('otel',) twice (at line 36, column 6)
```

Three problems:

1. **Misattributed, cryptic error.** The Codex adapter's `apply()` calls
   `validate_toml`, which raises mid-loop *after* the Claude summary printed but
   *before* Codex's. The `KeldError` carries no tool or file name, so it reads as
   if the Claude (JSON) step failed — but it's the Codex (TOML) config.
2. **One tool's conflict aborts everything.** Claude was fine, but the whole
   command died; nothing was configured.
3. **No interactivity, no real diff, no surfaced backup.** The user can't see
   exactly what will change, can't decide what to do about a conflict, and (since
   nothing was written) no backup was made.

## 2. Goals

Make `setup` interactive and trustworthy:

- Per-tool sections with clear attribution (tool + file path).
- A human-readable **unified diff** of each proposed change, followed by a short
  summary.
- **Per-conflict prompt** (skip this tool / abort everything) when a tool can't
  be safely modified.
- A single confirmation, then apply with **central backups** of every modified
  original.

No new third-party dependencies (stdlib `difflib`; `rich` already present).

## 3. Behavior

### 3.1 Compute phase (no writes, no exceptions for conflicts)

For each selected adapter, read the current config text (or `None` if absent)
and compute its `Plan`. Adapters **do not raise** for a conflict; instead a
`Plan` carries an optional `conflict` reason (see §4).

### 3.2 Presentation

For each tool, print a labeled section: `<display_name> · <config_path>`.

- **Clean change** (`plan.conflict is None` and `plan.changed`): render a colored
  **unified diff** of `before` → `after` (the actual file contents; a not-yet-
  existing file diffs against empty), then a one-line **summary** beneath it
  (e.g. `env: +6 OTEL_* keys · hooks: +SessionStart, +CwdChanged`). Diff first,
  summary after.
- **No-op** (`changed is False`): print `already configured — no changes`.
- **Conflict** (`plan.conflict` set): print the reason and how to resolve it
  (see §3.3).

The hook install (`keld-context.py`) is listed once as part of the plan.

### 3.3 Per-conflict prompt

When a tool reports a conflict, after printing
`<tool> · <file>: <reason>` and a resolution hint, prompt:

```
Codex already has its own [otel] settings in ~/.codex/config.toml that Keld
won't overwrite. Edit/remove that section and re-run, or skip Codex for now.
Skip Codex and continue, or abort everything? [s/a]
```

- **skip** → drop this tool from the apply set; continue with the rest.
- **abort** → write nothing, exit non-zero with a clear message.

Non-interactive resolution (see §3.5) auto-skips conflicts.

### 3.4 Confirm + apply

After all tools are presented and conflicts resolved, if there is at least one
clean change, prompt once: `Apply these N change(s)? [y/N]`. On yes:

1. Install the hook (`install_hook`).
2. For each approved tool, in order:
   - If the config file exists, **back it up** to
     `~/.keld/backups/<tool_name>/<filename>` — but only if no backup already
     exists there (one-time; preserves the pristine pre-Keld copy across
     re-runs). Print `backed up <path> → <backup path>`.
   - `write_atomic(path, after_text, backup=False)` (central backup replaces the
     old sibling `.keld.bak`).
   - Record `ToolManifest(name, config_path, managed, backup_path)`.
3. Save the manifest. Print a completion line.

If every selected tool ended up conflicted/skipped (nothing to apply), print a
clear summary and exit `0` (nothing was broken) — distinct from `abort` which is
a user-chosen non-zero exit.

### 3.5 Flags

- `--dry-run`: run the compute + presentation phase (diffs, summaries,
  conflicts) and stop. No prompts, no writes.
- `--yes` / `-y`: skip the final confirmation. Conflicts cannot be prompted, so
  they are **auto-skipped and reported** (CI-friendly: clean tools apply,
  conflicts are listed).
- Default (interactive): per-conflict prompt + final confirm.

### 3.6 What is a "conflict"

A conflict is a situation Keld will not write through safely:

- **TOML duplicate table** (today's only case): the user's `config.toml` already
  declares a table Keld manages (e.g. `[otel]`), so inserting Keld's block would
  produce invalid TOML. The Codex adapter detects this and returns a conflict
  `Plan`.

JSON tools (Claude, Gemini) merge non-destructively — Keld sets its own keys and
leaves the rest. If Keld overwrites an existing value (e.g. a custom
`OTEL_EXPORTER_OTLP_ENDPOINT`), that change is **shown in the diff** (red→green)
and covered by the final confirm; it is not a separate prompt.

## 4. Component changes

| File | Change |
| --- | --- |
| `tools/base.py` | `Plan` gains `conflict: str | None = None`. |
| `tools/codex.py` | `apply()` builds + validates as today, but on the duplicate-table `KeldError` from `validate_toml`, returns `Plan(..., conflict="<reason>", changed=False)` instead of propagating. No other adapter sets `conflict`. |
| `paths.py` | `backups_dir() -> Path` = `keld_home()/"backups"`. |
| `config/writer.py` | `backup_config(path: Path, tool_name: str) -> Path | None` — if `path` exists and no backup exists yet at `backups_dir()/tool_name/path.name`, copy it there and return the backup path; else return None. `write_atomic` keeps its `backup` param for compatibility but setup calls it with `backup=False` and backs up via `backup_config`. |
| `config/manifest.py` | `ToolManifest` gains `backup_path: str | None = None` (serializes via existing `vars()`/`from_dict`). |
| `keld/diffview.py` (new) | `render(before: str | None, after: str, path) -> None` — print a colored unified diff via `difflib.unified_diff` + `rich`; helper `summarize(plan) -> str` may live with the adapters instead (see below). |
| `commands/setup.py` | Rewrite `_run_setup` to the §3 flow: compute plans, present (diff+summary / conflict), prompt per conflict, single confirm, apply with central backup + manifest. |

The one-line per-tool **summary** is derived from `plan.summary` (already a
`list[str]` on `Plan`); `diffview` only renders the diff. The unified diff is
computed in the command/diffview layer from `before`/`after` (the command has
both: it read `before`, the plan has `after_text`).

## 5. Error handling

- Adapter compute errors that are *not* conflicts (e.g. a config file that fails
  to parse as JSON/TOML at read time) still surface as `KeldError`, but the
  command catches per-tool and attributes them: `<tool> · <file>: <message>` and
  treats them like a conflict (prompt skip/abort) rather than crashing the run.
- Never partially write a file (atomic write is unchanged).
- `abort` exits non-zero; "all skipped, nothing to do" exits zero.

## 6. Testing

- **Codex conflict-as-data:** `apply()` on a config with a pre-existing top-level
  `[otel]` returns a `Plan` with `conflict` set and `changed False`, and does
  **not** raise. A clean config still yields a normal plan.
- **Backup:** `backup_config` copies an existing file into
  `~/.keld/backups/<tool>/`, returns the path, and is a no-op (returns None) when
  a backup already exists; returns None when the source doesn't exist. The path
  is recorded on the `ToolManifest` and round-trips through the manifest.
- **`_run_setup` flow** (driven with injected `confirm`/conflict-prompt fakes):
  - clean tools apply after one confirm; backups created; manifest written.
  - a conflicted tool + `skip` → other tools still apply; conflicted tool absent
    from manifest.
  - a conflicted tool + `abort` → nothing written, non-zero.
  - `--dry-run` → diffs/conflicts shown, nothing written, no prompt.
  - `--yes` with a conflict → clean tools apply, conflict auto-skipped + reported.
- **Diff renderer:** produces a unified diff containing the added lines for a
  representative before/after; new-file case diffs against empty.
- **CLI end-to-end** (`test_signal_cli.py`): a `signal setup` run where Codex
  conflicts and the user skips → Claude configured, Codex reported skipped,
  backup present for Claude.

## 7. Out of scope

- Prompting on soft JSON overwrites (shown in diff instead — §3.6).
- Migrating existing sibling `.keld.bak` files to the central dir (fresh installs
  only; `uninstall` already removes via the manifest, not the backup).
- Changing `uninstall`'s restore logic (it strips Keld's entries via the
  manifest/markers; central backups are a manual safety net, surfaced for the
  user).
