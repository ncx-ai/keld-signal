from __future__ import annotations

from typing import NoReturn

import typer
from rich.console import Console

console = Console()
console_err = Console(stderr=True)


def fail(message: str) -> NoReturn:
    console_err.print(f"[bold red]Error:[/] {message}")
    raise typer.Exit(code=1)
