import pytest
from typer.testing import CliRunner
from keld.cli import app


def test_help_lists_subcommands():
    result = CliRunner().invoke(app, ["--help"])
    assert result.exit_code == 0
    for cmd in ["login", "logout", "whoami", "setup", "status", "doctor", "uninstall"]:
        assert cmd in result.output


def test_main_handles_keld_error(monkeypatch, capsys):
    import keld.cli as cli
    from keld.errors import KeldError

    def boom():
        raise KeldError("boom message")

    monkeypatch.setattr(cli, "app", boom)
    with pytest.raises(SystemExit) as exc:
        cli.main()
    assert exc.value.code == 1
    assert "boom message" in capsys.readouterr().err
