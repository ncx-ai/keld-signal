from __future__ import annotations

from pathlib import Path

import typer

from ..config.manifest import Manifest
from ..config.writer import delete_if_empty, write_atomic
from ..console import console
from ..paths import hook_path
from ..tools.registry import get_adapter


def _run_uninstall(manifest: Manifest, names: list[str] | None,
                   *, yes: bool, confirm=typer.confirm) -> None:
    targets = [n for n in manifest.tools if (names is None or n in names)]
    if not targets:
        console.print("Nothing to uninstall.")
        return
    if not yes and not confirm(f"Remove Keld config from {', '.join(targets)}?"):
        console.print("Aborted.")
        return

    for name in targets:
        tm = manifest.tools[name]
        adapter = get_adapter(name)
        path = Path(tm.config_path)
        current = path.read_text() if path.exists() else None
        plan = adapter.remove(current, tm.managed)
        if tm.managed.get("created") and delete_if_empty(path, plan.after_text):
            pass
        else:
            write_atomic(path, plan.after_text, backup=False)
        bak = path.with_name(path.name + ".keld.bak")
        if bak.exists():
            bak.unlink()
        del manifest.tools[name]
        console.print(f"  [green]✓[/] {adapter.display_name}")

    if not manifest.tools:
        if hook_path().exists():
            hook_path().unlink()
        if manifest.hook:
            manifest.hook = None
        manifest.endpoint = None
        manifest.actor = None
    manifest.save()
    console.print("Done.")


def uninstall(
    tool: str = typer.Option("", "--tool", help="Comma-separated tools to target."),
    yes: bool = typer.Option(False, "--yes", "-y", help="Skip confirmation."),
) -> None:
    """Remove Keld telemetry config and hook."""
    names = [t.strip() for t in tool.split(",") if t.strip()] or None
    _run_uninstall(Manifest.load(), names, yes=yes)
