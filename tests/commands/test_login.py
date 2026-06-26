from typer.testing import CliRunner

import keld.commands.login as login_cmd
from keld.auth.store import AuthData, save_auth
from keld.cli import app

runner = CliRunner()


def test_whoami_not_logged_in(keld_home):
    result = runner.invoke(app, ["whoami"])
    assert result.exit_code == 1
    assert "not logged in" in result.output.lower()


def test_whoami_logged_in(keld_home):
    save_auth(AuthData(access_token="t", principal="dg@keld.co", org="Keld",
                       api_url="https://atlas.keld.co"))
    result = runner.invoke(app, ["whoami"])
    assert result.exit_code == 0
    assert "dg@keld.co" in result.output and "Keld" in result.output


def test_logout(keld_home):
    save_auth(AuthData(access_token="t", principal="p", org="o", api_url="u"))
    result = runner.invoke(app, ["logout"])
    assert result.exit_code == 0
    assert "logged out" in result.output.lower()


def test_login_invokes_require_auth(keld_home, monkeypatch):
    called = {}
    monkeypatch.setattr(login_cmd, "require_auth",
                        lambda **kw: called.update(kw) or AuthData("t", "p", "o", "u"))
    result = runner.invoke(app, ["login"])
    assert result.exit_code == 0
    assert called == {"no_login": False}
