from __future__ import annotations

import typer

from .commands import login as login_cmd
from .commands import setup as setup_cmd
from .commands import status as status_cmd
from .commands import uninstall as uninstall_cmd
from .console import console_err
from .errors import KeldError

app = typer.Typer(no_args_is_help=True, add_completion=False, help="Keld CLI")

app.command()(login_cmd.login)
app.command()(login_cmd.logout)
app.command()(login_cmd.whoami)
app.command()(setup_cmd.setup)
app.command()(status_cmd.status)
app.command("doctor")(status_cmd.doctor)
app.command()(uninstall_cmd.uninstall)


def main() -> None:
    try:
        app()
    except KeldError as exc:
        console_err.print(f"[bold red]Error:[/] {exc}")
        raise SystemExit(1)
