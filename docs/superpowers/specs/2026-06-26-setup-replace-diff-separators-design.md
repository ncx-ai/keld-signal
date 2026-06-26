# `signal setup`: replace option, `--diff` flag, per-tool dividers — Design

**Date:** 2026-06-26
**Status:** Approved (brainstorming)
**Repo:** keld-cli
**Builds on:** `2026-06-26-setup-interactive-diff-backup-design.md` (interactive setup).

## 1. Motivation

Three refinements to the just-shipped interactive `signal setup`:

1. **Replace option.** A conflicted tool currently offers only *skip* / *abort*.
   Add **replace** so the user can resolve a conflict by overwriting just the
   conflicting section with Keld's, keeping the rest of their config.
2. **`--diff` flag.** The full unified diff shows by default and is verbose.
   Make it opt-in (`--diff`); show the concise summary by default. A destructive
   **replace** is the exception — it always shows its diff.
3. **Per-tool dividers.** Tool blocks (Claude, Codex, …) run together visually.
   Give each its own clearly-delimited section.

## 2. `--diff` flag

- `setup` gains `--diff` (bool, default `False`). `_run_setup` gains
  `show_diff: bool = False`.
- **Clean/additive change:** render the unified diff only when `show_diff`;
  always print the per-tool summary lines.
- **Replace change:** always render the diff (regardless of `show_diff`) — it
  removes user config, so the change must be visible.
- **Conflict:** the conflict reason is always printed.

## 3. Per-tool section dividers

Each tool's block opens with a titled horizontal rule (rich `console.rule`)
instead of a plain bold line, e.g.:

```
──────────────────────── Claude Code · ~/.claude/settings.json ────────────────────────
  env: +6 OTEL_* keys
  hooks: +SessionStart, +CwdChanged
──────────────────────────── Codex · ~/.codex/config.toml ─────────────────────────────
  conflict: …
```

`_run_setup` replaces the current `console.print(f"\n[bold]{name}[/] · {path}")`
header with `console.rule(f"[bold]{adapter.display_name}[/] · {plan.config_path}")`.
The "Hook" line stays a normal line (it's not a per-tool config block).

## 4. Three-way conflict resolution

- `resolve_conflict(adapter, plan) -> str` returns `"skip" | "replace" | "abort"`
  (previously a bool). `_default_resolve_conflict` prompts interactively:
  `[s]kip this tool, [r]eplace the conflicting section, or [a]bort everything? [s/r/a]`
  (implemented with `typer.prompt`, validated to `s`/`r`/`a`, default `s`).
- `_run_setup` conflict handling:
  - `dry_run` → print "(dry-run: would be skipped)", continue (no prompt).
  - `yes` → print "skipped (--yes)", continue (no prompt — **never** auto-replace;
    replace is destructive and requires explicit interactive consent).
  - else → `choice = resolve_conflict(adapter, plan)`:
    - `"skip"` → print "skipped", continue.
    - `"abort"` → print "Aborted.", `raise typer.Exit(code=1)`.
    - `"replace"` → recompute `plan = adapter.apply(before, params, replace=True)`.
      If that *still* reports a conflict (can't safely replace), print it and skip.
      Otherwise **always** render its diff, print its summary, append to
      `approved`. (It then flows through the normal apply path: backup → write →
      manifest.)

## 5. Replace computation (surgical)

- New helper `merge.strip_toml_table(text: str, table: str) -> str`: removes the
  top-level `[table]` header and its body, plus any `[table.sub]` subtable
  sections, from raw TOML text — preserving every other table/key. Implemented
  by walking lines and tracking the current top-level table (the first
  dotted segment of the most recent `[header]`/`[[header]]`); lines are dropped
  while that segment equals `table`. No-op if the table is absent.
- `ToolAdapter.apply` protocol signature gains `replace: bool = False`.
  Claude/Gemini accept and ignore it (their JSON merge is non-destructive and
  never produces a conflict, so `replace=True` is never reached for them; it
  returns the same plan).
- `CodexAdapter.apply(current_text, params, *, replace=False)`:
  - Build `body`; `after = upsert_keld_block(current_text, body)`; `validate_toml(after)`.
  - On the `KeldError` from `validate_toml`:
    - if `replace`: `stripped = strip_toml_table(current_text or "", "otel")`;
      `after = upsert_keld_block(stripped, body)`; `validate_toml(after)` again.
      If it still raises → return a conflict `Plan` (can't safely replace,
      include the error). Otherwise return a normal `Plan` (conflict=None,
      `changed=True`, summary noting it **replaces your existing [otel]**,
      `managed={"block": True, "created": current_text is None}`).
    - else (not replace): return the conflict `Plan` (unchanged from today).
  - Clean (no validate error) path unchanged; ignores `replace`.

The backup step (`backup_config`) already copies the pre-Keld file before
writing, so a replaced `[otel]` is always recoverable from
`~/.keld/backups/codex/config.toml`.

## 6. Flag interactions

- `--diff` + `--yes`: additive changes show the diff iff `--diff`; conflicts
  auto-skip (no replace). Replace can't occur (no prompt).
- `--diff` + `--dry-run`: show diffs per `--diff` + conflicts; nothing written;
  no prompt (replace can't occur).
- Replace is interactive-only (reached only through the prompt).

## 7. Testing

- `strip_toml_table`: removes `[otel]` and `[otel.exporter]` subtable while
  preserving other tables/keys; no-op when the table is absent.
- `CodexAdapter.apply(replace=True)` on a config with a pre-existing `[otel]`:
  returns a non-conflict plan; `after_text` contains Keld's block and not the
  user's old otel value; an unrelated user table is preserved. `replace=True`
  with no conflict behaves like a normal apply.
- `_run_setup`:
  - `resolve_conflict` returning `"replace"` → the tool is applied with the
    replace plan, the user's other config is preserved, a backup is made, and
    the manifest records it.
  - `"skip"`/`"abort"` behave as before.
- `show_diff`: additive change prints no diff hunk (`@@`) when `show_diff=False`,
  prints one when `True`; a replace prints its diff hunk even when
  `show_diff=False`.
- `--yes` still auto-skips conflicts (no replace).
- Dividers: a smoke assertion that each tool's display name appears in output
  (the rule renders without error).

## 8. Out of scope

- A non-interactive `--replace` flag (replace stays interactive-only).
- Replace for JSON tools (Claude/Gemini don't hard-conflict; their merge already
  overwrites Keld's own keys, visible in the diff).
