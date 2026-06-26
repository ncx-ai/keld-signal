from __future__ import annotations

from dataclasses import dataclass

import httpx

from ..errors import KeldError


@dataclass
class DeviceStart:
    device_code: str
    user_code: str
    verification_url: str
    interval: int
    expires_in: int


@dataclass
class Onboarding:
    endpoint: str
    ingest_token: str
    actor: str


class AtlasClient:
    def __init__(self, base_url: str, token: str | None = None, transport=None) -> None:
        self.base_url = base_url.rstrip("/")
        self.token = token
        self._transport = transport
        self._http = httpx.Client(base_url=self.base_url, timeout=30.0, transport=transport)

    def _post(self, path: str, **kwargs) -> httpx.Response:
        try:
            return self._http.post(path, **kwargs)
        except httpx.HTTPError as exc:
            raise KeldError(f"network error contacting Atlas: {exc}") from exc

    def _check(self, resp: httpx.Response) -> None:
        if resp.status_code >= 400:
            raise KeldError(f"Atlas returned {resp.status_code}: {resp.text[:200]}")

    def device_start(self) -> DeviceStart:
        resp = self._post("/v1/cli/device/start")
        self._check(resp)
        return DeviceStart(**resp.json())

    def device_poll(self, device_code: str) -> dict | None:
        resp = self._post("/v1/cli/device/poll", json={"device_code": device_code})
        if resp.status_code == 202:
            return None
        self._check(resp)
        return resp.json()

    def onboarding(self) -> Onboarding:
        if not self.token:
            raise KeldError("onboarding requires authentication")
        try:
            resp = self._http.get("/v1/cli/onboarding",
                                  headers={"Authorization": f"Bearer {self.token}"})
        except httpx.HTTPError as exc:
            raise KeldError(f"network error contacting Atlas: {exc}") from exc
        self._check(resp)
        return Onboarding(**resp.json())

    def fetch_hook(self, endpoint: str, ingest_token: str) -> bytes:
        url = f"{endpoint.rstrip('/')}/v1/tool-context/hook.py"
        try:
            with httpx.Client(timeout=30.0, transport=self._transport) as client:
                resp = client.get(url, params={"token": ingest_token})
        except httpx.HTTPError as exc:
            raise KeldError(f"network error downloading hook: {exc}") from exc
        self._check(resp)
        return resp.content
