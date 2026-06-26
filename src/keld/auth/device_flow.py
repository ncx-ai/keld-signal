from __future__ import annotations

import time
import webbrowser

from ..api.client import AtlasClient
from ..console import console
from ..errors import KeldError
from ..paths import api_base
from .store import AuthData, load_auth, save_auth


def login(client: AtlasClient, *, open_browser: bool = True,
          sleep=time.sleep, opener=webbrowser.open) -> AuthData:
    ds = client.device_start()
    console.print(f"To authorize, visit [bold]{ds.verification_url}[/] "
                  f"and enter code [bold]{ds.user_code}[/]")
    if open_browser:
        opener(ds.verification_url)

    waited = 0
    while waited <= ds.expires_in:
        result = client.device_poll(ds.device_code)
        if result is not None:
            auth = AuthData(access_token=result["access_token"],
                            principal=result["principal"], org=result["org"],
                            api_url=client.base_url)
            save_auth(auth)
            console.print(f"Logged in as [bold]{auth.principal}[/] (org: {auth.org})")
            return auth
        sleep(ds.interval)
        waited += max(ds.interval, 1)
    raise KeldError("login timed out; please run `keld login` again")


def require_auth(*, no_login: bool, open_browser: bool = True) -> AuthData:
    existing = load_auth()
    if existing is not None:
        return existing
    if no_login:
        raise KeldError("not logged in (run `keld login`; --no-login was set)")
    return login(AtlasClient(api_base()), open_browser=open_browser)
