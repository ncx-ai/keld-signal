from __future__ import annotations

from pathlib import Path

import typer

from ..auth.store import load_auth
from ..config.manifest import Manifest
from ..console import console
from ..tools.base import ToolStatus
from ..tools.registry import ALL_ADAPTERS


def _read(path: Path) -> str | None:
    return path.read_text() if path.exists() else None


def _collect_status(manifest: Manifest) -> list[tuple[str, ToolStatus]]:
    rows = []
    for adapter in ALL_ADAPTERS:
        tm = manifest.tools.get(adapter.name)
        managed = tm.managed if tm else None
        st = adapter.status(_read(adapter.config_path()), managed)
        rows.append((adapter.display_name, st))
    return rows


def status() -> None:
    """Show local Keld configuration state."""
    auth = load_auth()
    if auth is None:
        console.print("[yellow]Not logged in[/] (run `keld login`)")
    else:
        console.print(f"Logged in: [bold]{auth.principal}[/] · org {auth.org} · {auth.api_url}")

    manifest = Manifest.load()
    for display_name, st in _collect_status(manifest):
        state = "[green]configured[/]" if st.configured else (
            "not configured" if st.installed else "[dim]not installed[/]")
        console.print(f"  {display_name:14} {state}")
    if manifest.hook:
        console.print(f"  hook            v{manifest.hook.version}")


def doctor() -> None:
    """Diagnose Keld configuration problems."""
    problems: list[str] = []
    manifest = Manifest.load()

    for name, tm in manifest.tools.items():
        adapter = next((a for a in ALL_ADAPTERS if a.name == name), None)
        if adapter is None:
            continue
        st = adapter.status(_read(Path(tm.config_path)), tm.managed)
        if not st.configured:
            problems.append(f"{adapter.display_name}: manifest records setup but config is not "
                            f"configured (drift). Re-run `keld setup`.")

    if manifest.hook and not Path(manifest.hook.path).exists():
        problems.append("hook script is missing. Re-run `keld setup`.")

    if problems:
        for p in problems:
            console.print(f"  [red]✗[/] {p}")
        raise typer.Exit(code=1)
    console.print("[green]No problems found.[/]")
