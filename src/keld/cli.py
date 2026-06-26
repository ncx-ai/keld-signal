from __future__ import annotations

import typer

from .commands import login as login_cmd
from .commands import setup as setup_cmd
from .commands import status as status_cmd
from .commands import uninstall as uninstall_cmd
from .console import console_err
from .errors import KeldError

app = typer.Typer(no_args_is_help=True, add_completion=False, help="Keld CLI")

# Top-level auth commands — shared across all product groups.
app.command()(login_cmd.login)
app.command()(login_cmd.logout)
app.command()(login_cmd.whoami)

# `keld signal <cmd>` — Keld Signal telemetry onboarding for local tools.
signal_app = typer.Typer(
    no_args_is_help=True,
    help="Set up Keld Signal telemetry for your local AI coding tools.",
)
signal_app.command()(setup_cmd.setup)
signal_app.command()(status_cmd.status)
signal_app.command("doctor")(status_cmd.doctor)
signal_app.command()(uninstall_cmd.uninstall)
app.add_typer(signal_app, name="signal")


def main() -> None:
    try:
        app()
    except KeldError as exc:
        console_err.print(f"[bold red]Error:[/] {exc}")
        raise SystemExit(1)
