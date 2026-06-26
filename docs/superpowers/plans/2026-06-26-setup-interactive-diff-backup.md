# Interactive / backed-up / conflict-aware `signal setup` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `keld signal setup` show a per-tool unified diff + summary, prompt on conflicts (skip-this-tool / abort) instead of crashing with a misattributed error, and back up every modified config into a central dir recorded in the manifest.

**Architecture:** Adapters surface a conflict as data on `Plan` (no mid-loop exceptions). `_run_setup` computes each tool's plan, renders a colored unified diff + summary for clean tools, prompts per conflict, takes one final confirm, then applies — backing each modified file up to `~/.keld/backups/<tool>/` first.

**Tech Stack:** Python 3.12, Typer, rich, stdlib `difflib`/`shutil`. No new dependencies.

## Global Constraints

- keld-cli repo. Activate the dev venv before running anything: `. .venv/bin/activate`; run tests with `python -m pytest`. No `uv`.
- No new third-party dependencies (stdlib `difflib`/`shutil`; `rich` already present).
- Diff is shown **before** the per-tool summary. Conflicts → prompt **skip this tool / abort everything**. One final confirm for all clean tools.
- Backups go to `~/.keld/backups/<tool_name>/<filename>`, **one-time** (never clobber an existing pristine backup), path recorded on the manifest; surfaced in output.
- `--dry-run`: show diffs/conflicts, no prompts, no writes. `--yes`: skip final confirm; conflicts auto-skip + are reported.
- Each task: failing test → run (RED) → implement → run (GREEN) → commit. Use the code verbatim.

---

### Task 1: `Plan.conflict` field

**Files:**
- Modify: `src/keld/tools/base.py`
- Test: `tests/tools/test_base.py`

**Interfaces:**
- Produces: `Plan` gains `conflict: str | None = None` (after the existing fields).

- [ ] **Step 1: Add the failing test**

Append to `tests/tools/test_base.py`:
```python
def test_plan_conflict_defaults_none_and_settable():
    from pathlib import Path
    from keld.tools.base import Plan
    p = Plan(name="x", config_path=Path("/tmp/c"), after_text="", managed={},
             summary=[], changed=False)
    assert p.conflict is None
    p2 = Plan(name="x", config_path=Path("/tmp/c"), after_text="", managed={},
              summary=[], changed=False, conflict="boom")
    assert p2.conflict == "boom"
```

- [ ] **Step 2: Run test to verify it fails**

Run: `python -m pytest tests/tools/test_base.py::test_plan_conflict_defaults_none_and_settable -v`
Expected: FAIL — `TypeError: ... unexpected keyword argument 'conflict'`.

- [ ] **Step 3: Add the field**

In `src/keld/tools/base.py`, in the `Plan` dataclass, add as the last field:
```python
    conflict: str | None = None
```

- [ ] **Step 4: Run test to verify it passes**

Run: `python -m pytest tests/tools/test_base.py -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/keld/tools/base.py tests/tools/test_base.py
git commit -m "feat: Plan.conflict field"
```

---

### Task 2: Codex adapter returns a conflict Plan instead of raising

**Files:**
- Modify: `src/keld/tools/codex.py`
- Test: `tests/tools/test_codex.py`

**Interfaces:**
- Consumes: `Plan.conflict` (Task 1), `config.merge.validate_toml`, `errors.KeldError`.
- Produces: `CodexAdapter.apply` returns `Plan(conflict=<reason>, changed=False, after_text=current or "", managed={})` when the merged TOML would be invalid (duplicate table); otherwise a normal plan with `conflict=None`.

- [ ] **Step 1: Update the conflict test (RED)**

In `tests/tools/test_codex.py`, replace the existing `test_apply_conflict_raises` with:
```python
def test_apply_conflict_returns_conflict_plan_not_raises():
    plan = CodexAdapter().apply('[otel]\nenvironment = "dev"\n', P)
    assert plan.conflict is not None
    assert "otel" in plan.conflict.lower()
    assert plan.changed is False
```
(Keep the other codex tests as-is. `P` is the existing module-level `SetupParams`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `python -m pytest tests/tools/test_codex.py -v`
Expected: FAIL — the old code raises `KeldError`, so `apply(...)` throws instead of returning a plan.

- [ ] **Step 3: Implement conflict-as-data**

In `src/keld/tools/codex.py`, ensure the imports include `KeldError`:
```python
from ..errors import KeldError
```
Replace the body of `apply` with:
```python
    def apply(self, current_text: str | None, params: SetupParams) -> Plan:
        body = t.codex_block_body(params, t.hook_command(str(hook_path())))
        after = upsert_keld_block(current_text, body)
        try:
            validate_toml(after)
        except KeldError:
            reason = ("your ~/.codex/config.toml already defines settings that "
                      "conflict with Keld's (a duplicate [otel] table); "
                      "Keld won't modify it.")
            return Plan(
                name=self.name, config_path=self.config_path(),
                after_text=current_text or "", managed={}, summary=[],
                changed=False, conflict=reason,
            )
        return Plan(
            name=self.name, config_path=self.config_path(), after_text=after,
            managed={"block": True, "created": current_text is None},
            summary=["add [otel] + SessionStart/PreToolUse hooks block"],
            changed=after != (current_text or ""),
        )
```

- [ ] **Step 4: Run test to verify it passes**

Run: `python -m pytest tests/tools/test_codex.py -v`
Expected: PASS (clean-config tests still pass; the conflict test now asserts the plan).

- [ ] **Step 5: Commit**

```bash
git add src/keld/tools/codex.py tests/tools/test_codex.py
git commit -m "feat: Codex adapter surfaces conflict as data, not an exception"
```

---

### Task 3: central backup dir + `backup_config`

**Files:**
- Modify: `src/keld/paths.py`, `src/keld/config/writer.py`
- Test: `tests/config/test_writer.py`

**Interfaces:**
- Produces: `paths.backups_dir() -> Path` = `keld_home()/"backups"`. `writer.backup_config(path: Path, tool_name: str) -> Path | None` — if `path` exists and `backups_dir()/tool_name/path.name` does not yet exist, copy it there (mkdir parents) and return the destination; otherwise return `None`.

- [ ] **Step 1: Add the failing test**

Append to `tests/config/test_writer.py`:
```python
def test_backup_config_central_one_time(keld_home, tmp_path):
    from keld.config.writer import backup_config
    from keld.paths import backups_dir

    cfg = tmp_path / ".claude" / "settings.json"
    cfg.parent.mkdir(parents=True)
    cfg.write_text('{"a": 1}\n')

    dest = backup_config(cfg, "claude_code")
    assert dest == backups_dir() / "claude_code" / "settings.json"
    assert dest.read_text() == '{"a": 1}\n'

    # one-time: a second call does not clobber and returns None
    cfg.write_text('{"a": 2}\n')
    assert backup_config(cfg, "claude_code") is None
    assert dest.read_text() == '{"a": 1}\n'


def test_backup_config_missing_source_returns_none(keld_home, tmp_path):
    from keld.config.writer import backup_config
    assert backup_config(tmp_path / "nope.json", "codex") is None
```

- [ ] **Step 2: Run test to verify it fails**

Run: `python -m pytest tests/config/test_writer.py -v`
Expected: FAIL — `ImportError: cannot import name 'backup_config'` / `backups_dir`.

- [ ] **Step 3: Implement**

In `src/keld/paths.py`, add after `hook_path()`:
```python
def backups_dir() -> Path:
    return keld_home() / "backups"
```

In `src/keld/config/writer.py`, add (keep existing `write_atomic`/`delete_if_empty`):
```python
import shutil

from ..paths import backups_dir


def backup_config(path: Path, tool_name: str) -> Path | None:
    """Copy `path` into ~/.keld/backups/<tool_name>/ before Keld modifies it.

    One-time: if a backup already exists there it is preserved (keeps the
    pristine pre-Keld copy across re-runs). Returns the backup path, or None
    if the source doesn't exist or a backup already exists.
    """
    if not path.exists():
        return None
    dest = backups_dir() / tool_name / path.name
    if dest.exists():
        return None
    dest.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(path, dest)
    return dest
```
(Add `from pathlib import Path` only if not already imported — it is used by the existing `write_atomic` signature.)

- [ ] **Step 4: Run test to verify it passes**

Run: `python -m pytest tests/config/test_writer.py -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/keld/paths.py src/keld/config/writer.py tests/config/test_writer.py
git commit -m "feat: central one-time config backups (backup_config)"
```

---

### Task 4: `ToolManifest.backup_path`

**Files:**
- Modify: `src/keld/config/manifest.py`
- Test: `tests/config/test_manifest.py`

**Interfaces:**
- Produces: `ToolManifest` gains `backup_path: str | None = None`; round-trips through `to_dict`/`from_dict`; a manifest dict lacking the key still loads (default None).

- [ ] **Step 1: Add the failing test**

Append to `tests/config/test_manifest.py`:
```python
def test_tool_manifest_backup_path_round_trip(keld_home):
    from keld.config.manifest import Manifest, ToolManifest
    m = Manifest(endpoint="https://e", actor="a@b.co")
    m.tools["claude_code"] = ToolManifest(
        name="claude_code", config_path="/h/.claude/settings.json",
        managed={"env_keys": []}, backup_path="/h/.keld/backups/claude_code/settings.json")
    m.save()
    again = Manifest.load()
    assert again.tools["claude_code"].backup_path == "/h/.keld/backups/claude_code/settings.json"


def test_tool_manifest_loads_without_backup_path(keld_home):
    # Manifests written before this field must still load.
    from keld.config.manifest import ToolManifest
    tm = ToolManifest(**{"name": "codex", "config_path": "/c", "managed": {}})
    assert tm.backup_path is None
```

- [ ] **Step 2: Run test to verify it fails**

Run: `python -m pytest tests/config/test_manifest.py -v`
Expected: FAIL — `TypeError: ... unexpected keyword argument 'backup_path'`.

- [ ] **Step 3: Add the field**

In `src/keld/config/manifest.py`, in the `ToolManifest` dataclass, add after `managed`:
```python
    backup_path: str | None = None
```
(No change to `to_dict`/`from_dict` is needed: `to_dict` uses `vars(v)` so it includes the new field, and `from_dict` builds `ToolManifest(**v)` so older dicts without the key fall back to the default.)

- [ ] **Step 4: Run test to verify it passes**

Run: `python -m pytest tests/config/test_manifest.py -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/keld/config/manifest.py tests/config/test_manifest.py
git commit -m "feat: ToolManifest.backup_path"
```

---

### Task 5: unified-diff renderer (`diffview.py`)

**Files:**
- Create: `src/keld/diffview.py`
- Test: `tests/test_diffview.py`

**Interfaces:**
- Consumes: `keld.console.console`.
- Produces: `diffview.diff_lines(before: str | None, after: str, label: str) -> list[str]` (unified diff via `difflib`); `diffview.render(before: str | None, after: str, label) -> None` (prints the diff colored, `markup=False` so bracketed config text isn't parsed as rich markup).

- [ ] **Step 1: Add the failing test**

```python
# tests/test_diffview.py
from keld import diffview


def test_diff_lines_shows_added_line():
    lines = diffview.diff_lines('{\n}\n', '{\n  "x": 1\n}\n', "settings.json")
    body = "".join(lines)
    assert '+  "x": 1' in body
    assert any(l.startswith("@@") for l in lines)


def test_diff_lines_new_file_diffs_against_empty():
    lines = diffview.diff_lines(None, "hello\n", "f")
    body = "".join(lines)
    assert "+hello" in body


def test_render_smoke(capsys):
    diffview.render(None, "hello\n", "f")  # must not raise, prints something
    assert "hello" in capsys.readouterr().out
```

- [ ] **Step 2: Run test to verify it fails**

Run: `python -m pytest tests/test_diffview.py -v`
Expected: FAIL — `ModuleNotFoundError: No module named 'keld.diffview'`.

- [ ] **Step 3: Implement**

```python
# src/keld/diffview.py
from __future__ import annotations

import difflib

from .console import console


def diff_lines(before: str | None, after: str, label: str) -> list[str]:
    return list(difflib.unified_diff(
        (before or "").splitlines(keepends=True),
        after.splitlines(keepends=True),
        fromfile=f"a/{label}", tofile=f"b/{label}",
    ))


def render(before: str | None, after: str, label) -> None:
    for raw in diff_lines(before, after, str(label)):
        line = raw.rstrip("\n")
        if line.startswith("+") and not line.startswith("+++"):
            style = "green"
        elif line.startswith("-") and not line.startswith("---"):
            style = "red"
        elif line.startswith("@@"):
            style = "cyan"
        else:
            style = "dim"
        # markup=False: config lines contain brackets ([otel], JSON) that rich
        # would otherwise try to parse as markup.
        console.print(line, style=style, markup=False)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `python -m pytest tests/test_diffview.py -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add src/keld/diffview.py tests/test_diffview.py
git commit -m "feat: unified-diff renderer"
```

---

### Task 6: rewrite `_run_setup` (interactive, diff, conflict-prompt, backups)

**Files:**
- Modify: `src/keld/commands/setup.py`
- Test: `tests/commands/test_setup.py`

**Interfaces:**
- Consumes: `Plan.conflict` (T1), `diffview.render` (T5), `writer.backup_config` (T3), `ToolManifest.backup_path` (T4), `errors.KeldError`, existing `install_hook`, `write_atomic`, `Manifest`, `AtlasClient`, `Onboarding`, `SetupParams`, `select_adapters`.
- Produces: `_run_setup(adapters, params, client, ob, *, dry_run, yes, confirm=typer.confirm, resolve_conflict=None) -> Manifest`. `resolve_conflict(adapter, plan) -> bool` (True = skip this tool, False = abort); defaults to `_default_resolve_conflict` (a `typer.confirm` prompt). `setup` CLI command unchanged in signature; it calls `_run_setup` as today.

- [ ] **Step 1: Replace the setup tests (RED)**

Replace the body of `tests/commands/test_setup.py` with:
```python
import json

import httpx
import pytest

from keld.api.client import AtlasClient, Onboarding
from keld.commands.setup import _run_setup
from keld.config.manifest import Manifest
from keld.paths import backups_dir
from keld.tools.base import SetupParams
from keld.tools.claude import ClaudeAdapter
from keld.tools.codex import CodexAdapter


def _client():
    return AtlasClient("https://atlas.keld.co",
                       transport=httpx.MockTransport(lambda r: httpx.Response(200, content=b"# hook\n")))


PARAMS = SetupParams(endpoint="https://ingest.keld.co", ingest_token="tok", actor="dg@keld.co")
OB = Onboarding(endpoint="https://ingest.keld.co", ingest_token="tok", actor="dg@keld.co")


def test_clean_tool_applies_and_backs_up(keld_home, monkeypatch, tmp_path):
    cfg = tmp_path / ".claude" / "settings.json"
    cfg.parent.mkdir(parents=True)
    cfg.write_text(json.dumps({"model": "opus"}) + "\n")   # pre-existing user config
    monkeypatch.setattr(ClaudeAdapter, "config_path", lambda self: cfg)

    manifest = _run_setup([ClaudeAdapter()], PARAMS, _client(), OB,
                          dry_run=False, yes=True)
    obj = json.loads(cfg.read_text())
    assert obj["env"]["OTEL_EXPORTER_OTLP_ENDPOINT"] == "https://ingest.keld.co"
    assert obj["model"] == "opus"
    # central backup of the original was made and recorded
    bak = backups_dir() / "claude_code" / "settings.json"
    assert json.loads(bak.read_text()) == {"model": "opus"}
    assert manifest.tools["claude_code"].backup_path == str(bak)


def test_conflict_skip_applies_others(keld_home, monkeypatch, tmp_path):
    claude_cfg = tmp_path / ".claude" / "settings.json"
    codex_cfg = tmp_path / ".codex" / "config.toml"
    codex_cfg.parent.mkdir(parents=True)
    codex_cfg.write_text('[otel]\nenvironment = "dev"\n')   # conflicts
    monkeypatch.setattr(ClaudeAdapter, "config_path", lambda self: claude_cfg)
    monkeypatch.setattr(CodexAdapter, "config_path", lambda self: codex_cfg)

    manifest = _run_setup([ClaudeAdapter(), CodexAdapter()], PARAMS, _client(), OB,
                          dry_run=False, yes=False,
                          confirm=lambda msg: True,
                          resolve_conflict=lambda adapter, plan: True)  # skip codex
    assert "claude_code" in manifest.tools
    assert "codex" not in manifest.tools           # conflicted tool skipped
    assert claude_cfg.exists()
    assert codex_cfg.read_text() == '[otel]\nenvironment = "dev"\n'  # untouched


def test_conflict_abort_writes_nothing(keld_home, monkeypatch, tmp_path):
    claude_cfg = tmp_path / ".claude" / "settings.json"
    codex_cfg = tmp_path / ".codex" / "config.toml"
    codex_cfg.parent.mkdir(parents=True)
    codex_cfg.write_text('[otel]\nx = 1\n')
    monkeypatch.setattr(ClaudeAdapter, "config_path", lambda self: claude_cfg)
    monkeypatch.setattr(CodexAdapter, "config_path", lambda self: codex_cfg)

    import typer
    with pytest.raises(typer.Exit):
        _run_setup([ClaudeAdapter(), CodexAdapter()], PARAMS, _client(), OB,
                   dry_run=False, yes=False,
                   confirm=lambda msg: True,
                   resolve_conflict=lambda adapter, plan: False)  # abort
    assert not claude_cfg.exists()                 # nothing written
    assert Manifest.load().tools == {}


def test_dry_run_writes_nothing(keld_home, monkeypatch, tmp_path):
    cfg = tmp_path / ".claude" / "settings.json"
    monkeypatch.setattr(ClaudeAdapter, "config_path", lambda self: cfg)
    _run_setup([ClaudeAdapter()], PARAMS, _client(), OB, dry_run=True, yes=True)
    assert not cfg.exists()
    from keld.paths import manifest_path
    assert not manifest_path().exists()


def test_yes_auto_skips_conflict(keld_home, monkeypatch, tmp_path):
    claude_cfg = tmp_path / ".claude" / "settings.json"
    codex_cfg = tmp_path / ".codex" / "config.toml"
    codex_cfg.parent.mkdir(parents=True)
    codex_cfg.write_text('[otel]\nx = 1\n')
    monkeypatch.setattr(ClaudeAdapter, "config_path", lambda self: claude_cfg)
    monkeypatch.setattr(CodexAdapter, "config_path", lambda self: codex_cfg)

    # --yes: no prompts; conflict auto-skipped, clean tool applied
    manifest = _run_setup([ClaudeAdapter(), CodexAdapter()], PARAMS, _client(), OB,
                          dry_run=False, yes=True)
    assert "claude_code" in manifest.tools and "codex" not in manifest.tools
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `python -m pytest tests/commands/test_setup.py -v`
Expected: FAIL — `_run_setup` has no `resolve_conflict` param / old behavior (raises on codex, no backups).

- [ ] **Step 3: Rewrite `_run_setup`**

In `src/keld/commands/setup.py`, update imports and replace `_run_setup` (leave `setup` as-is). New imports block:
```python
from __future__ import annotations

import typer

from .. import diffview
from ..api.client import AtlasClient, Onboarding
from ..auth.device_flow import require_auth
from ..config.manifest import Manifest, ToolManifest
from ..config.writer import backup_config, write_atomic
from ..console import console
from ..errors import KeldError
from ..hook import install_hook
from ..paths import api_base_override, set_api_base_override
from ..tools.base import Plan, SetupParams
from ..tools.registry import select_adapters
```
Replace `_run_setup` (the `setup` function below it stays unchanged) with:
```python
def _default_resolve_conflict(adapter, plan) -> bool:
    """Prompt for a conflicted tool. Returns True to skip it, False to abort."""
    return typer.confirm(
        f"Skip {adapter.display_name} and continue? (answering no aborts the whole run)",
        default=True,
    )


def _run_setup(adapters, params: SetupParams, client: AtlasClient, ob: Onboarding,
               *, dry_run: bool, yes: bool, confirm=typer.confirm,
               resolve_conflict=None) -> Manifest:
    if resolve_conflict is None:
        resolve_conflict = _default_resolve_conflict

    approved = []  # list[tuple[adapter, Plan]]
    for adapter in adapters:
        path = adapter.config_path()
        before = path.read_text() if path.exists() else None
        try:
            plan = adapter.apply(before, params)
        except KeldError as exc:
            plan = Plan(name=adapter.name, config_path=path, after_text=before or "",
                        managed={}, summary=[], changed=False, conflict=str(exc))

        console.print(f"\n[bold]{adapter.display_name}[/] · {plan.config_path}")

        if plan.conflict:
            console.print(f"  [yellow]conflict:[/] {plan.conflict}")
            console.print(f"  [dim]resolve it and re-run, or skip {adapter.display_name} for now.[/]")
            if dry_run:
                console.print("  [dim](dry-run: would be skipped)[/]")
                continue
            if yes:
                console.print(f"  [yellow]skipped[/] (--yes)")
                continue
            if resolve_conflict(adapter, plan):
                console.print("  [yellow]skipped[/]")
                continue
            console.print("Aborted.")
            raise typer.Exit(code=1)

        if not plan.changed:
            console.print("  already configured — no changes")
            continue

        diffview.render(before, plan.after_text, plan.config_path)
        for line in plan.summary:
            console.print(f"  [dim]{line}[/]")
        approved.append((adapter, plan))

    console.print("\n[bold]Hook[/] · keld-context.py → ~/.keld")

    if dry_run:
        console.print("\n[dim]--dry-run: no changes written.[/]")
        return Manifest.load()
    if not approved:
        console.print("\nNothing to apply.")
        return Manifest.load()
    if not yes and not confirm(f"Apply {len(approved)} change(s)?"):
        console.print("Aborted.")
        return Manifest.load()

    manifest = Manifest(endpoint=ob.endpoint, actor=ob.actor)
    manifest.hook = install_hook(client, ob)
    for adapter, plan in approved:
        backup = backup_config(plan.config_path, adapter.name)
        if backup:
            console.print(f"  [dim]backed up {plan.config_path} → {backup}[/]")
        write_atomic(plan.config_path, plan.after_text, backup=False)
        manifest.tools[adapter.name] = ToolManifest(
            name=adapter.name, config_path=str(plan.config_path),
            managed=plan.managed, backup_path=str(backup) if backup else None)
        console.print(f"  [green]✓[/] {adapter.display_name}")

    manifest.save()
    console.print("\nSetup complete. Restart any running sessions to pick up the new config.")
    return manifest
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `python -m pytest tests/commands/test_setup.py -v`
Expected: PASS (5 tests).

- [ ] **Step 5: Run the full suite (no regressions)**

Run: `python -m pytest -q`
Expected: PASS — whole suite green.

- [ ] **Step 6: Commit**

```bash
git add src/keld/commands/setup.py tests/commands/test_setup.py
git commit -m "feat: interactive setup — diffs, per-conflict prompt, central backups"
```

---

### Task 7: CLI end-to-end (conflict skip) + README + full green

**Files:**
- Modify: `tests/commands/test_signal_cli.py`, `README.md`
- Test: the whole suite.

**Interfaces:**
- Consumes: everything above. The CLI `setup` wrapper is unchanged; `--api-url`, `--tool`, `--dry-run`, `--yes`, `--no-login` all still work.

- [ ] **Step 1: Add a CLI conflict-skip test (RED)**

Append to `tests/commands/test_signal_cli.py`:
```python
def test_signal_setup_conflict_skip_via_cli(keld_home, monkeypatch, tmp_path):
    from keld.tools.codex import CodexAdapter
    claude_cfg = tmp_path / ".claude" / "settings.json"
    codex_cfg = tmp_path / ".codex" / "config.toml"
    codex_cfg.parent.mkdir(parents=True)
    codex_cfg.write_text('[otel]\nx = 1\n')   # conflicts
    _patch_wrappers(monkeypatch, claude_cfg)
    monkeypatch.setattr(CodexAdapter, "config_path", lambda self: codex_cfg)

    # CLI runner can't answer interactive prompts; --yes auto-skips the conflict.
    result = runner.invoke(
        app, ["signal", "setup", "--tool", "claude_code,codex", "--yes"])
    assert result.exit_code == 0, result.output
    assert "claude_code" in Manifest.load().tools
    assert "codex" not in Manifest.load().tools
    assert codex_cfg.read_text() == '[otel]\nx = 1\n'   # untouched
```

- [ ] **Step 2: Run test to verify it fails / then passes**

Run: `python -m pytest tests/commands/test_signal_cli.py -v`
Expected: After Task 6 this should already PASS (the behavior exists); if it fails, fix the wiring before continuing. (The test is added here to lock the end-to-end path.)

- [ ] **Step 3: Update the README**

In `README.md`, under the `keld signal setup` flags paragraph, add:
```markdown
`setup` is interactive: it shows a unified diff of the exact changes to each
config file, then asks before writing. If a tool's config already has settings
Keld can't safely merge (e.g. Codex with its own `[otel]` section), setup
explains the conflict and lets you skip that tool or abort — the other tools
still get configured. Every file Keld modifies is first copied to
`~/.keld/backups/<tool>/`. Use `--dry-run` to preview without writing and
`--yes` to skip prompts (conflicts are auto-skipped in that mode).
```

- [ ] **Step 4: Run the full suite + smoke test**

Run: `python -m pytest -q`
Expected: PASS (whole suite).
Then: `keld signal setup --help` shows the flags; no traceback.

- [ ] **Step 5: Commit**

```bash
git add tests/commands/test_signal_cli.py README.md
git commit -m "test+docs: CLI conflict-skip e2e; document interactive setup"
```

---

## Self-Review Notes (for the implementer)

- **Spec coverage:** §3.1 compute-without-raising (T2 + T6 try/except); §3.2 diff-then-summary (T5 + T6); §3.3 per-conflict prompt skip/abort (T6 `resolve_conflict`); §3.4 one confirm + central backup + manifest path (T3, T4, T6); §3.5 dry-run/--yes (T6 + T7); §3.6 conflict only for TOML duplicate (T2; JSON shows in diff); §4 component table (T1–T6); §5 non-conflict compute errors attributed via the `except KeldError` wrapper (T6); §6 tests (each task).
- **Type consistency:** `Plan.conflict` (T1) used in T2/T6; `backup_config(path, tool_name) -> Path|None` (T3) used in T6; `ToolManifest.backup_path` (T4) set in T6; `diffview.render(before, after, label)` (T5) called in T6; `_run_setup(..., resolve_conflict=None)` with `resolve_conflict(adapter, plan) -> bool` consistent between T6 impl and its tests.
- **No placeholders:** every step has complete code.
