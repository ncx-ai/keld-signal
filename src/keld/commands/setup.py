from __future__ import annotations

import typer

from ..api.client import AtlasClient, Onboarding
from ..auth.device_flow import require_auth
from ..config.manifest import Manifest, ToolManifest
from ..config.writer import write_atomic
from ..console import console
from ..hook import install_hook
from ..tools.base import SetupParams
from ..tools.registry import select_adapters


def _run_setup(adapters, params: SetupParams, client: AtlasClient, ob: Onboarding,
               *, dry_run: bool, yes: bool, confirm=typer.confirm) -> Manifest:
    plans = []
    for adapter in adapters:
        path = adapter.config_path()
        current = path.read_text() if path.exists() else None
        plan = adapter.apply(current, params)
        plans.append(plan)
        console.print(f"\n[bold]{adapter.display_name}[/] → {plan.config_path}")
        for line in plan.summary:
            console.print(f"  + {line}")
    console.print(f"\n[bold]Hook[/] → will install keld-context.py")

    if dry_run:
        console.print("\n[dim]--dry-run: no changes written.[/]")
        return Manifest.load()
    if not yes and not confirm("Proceed?"):
        console.print("Aborted.")
        return Manifest.load()

    manifest = Manifest(endpoint=ob.endpoint, actor=ob.actor)
    record = install_hook(client, ob)
    manifest.hook = record

    for adapter, plan in zip(adapters, plans):
        write_atomic(plan.config_path, plan.after_text, backup=True)
        manifest.tools[adapter.name] = ToolManifest(
            name=adapter.name, config_path=str(plan.config_path), managed=plan.managed)
        console.print(f"  [green]✓[/] {adapter.display_name}")

    manifest.save()
    console.print("\nSetup complete. Restart any running sessions to pick up the new config.")
    return manifest


def setup(
    tool: str = typer.Option("", "--tool", help="Comma-separated tools to target."),
    dry_run: bool = typer.Option(False, "--dry-run", help="Show changes without writing."),
    yes: bool = typer.Option(False, "--yes", "-y", help="Skip confirmation."),
    no_login: bool = typer.Option(False, "--no-login", help="Fail instead of opening a browser."),
) -> None:
    """Configure detected tools for Keld telemetry."""
    auth = require_auth(no_login=no_login)
    client = AtlasClient(auth.api_url, token=auth.access_token)
    ob = client.onboarding()
    names = [t.strip() for t in tool.split(",") if t.strip()] or None
    adapters = select_adapters(names)
    if not adapters:
        console.print("No supported tools detected. Use --tool to target one explicitly.")
        raise typer.Exit(code=0)
    params = SetupParams(endpoint=ob.endpoint, ingest_token=ob.ingest_token, actor=ob.actor)
    _run_setup(adapters, params, client, ob, dry_run=dry_run, yes=yes)
