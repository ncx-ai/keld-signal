#!/usr/bin/env python3
"""Send one test prompt to the running keld-agent daemon's loopback ingress.

Reads ~/.keld/agent.json (port + secret) and POSTs an inline prompt to /enrich,
so the daemon enriches it and publishes to the configured endpoint. Pass a custom
prompt as the first arg; the default contains a fake secret + email to exercise
sensitivity detection.
"""
import json
import os
import sys
import urllib.request

DEFAULT = "Write a Python function; my API key is sk-live-ABC123 and email a@b.com"


def main() -> int:
    cfg_path = os.path.expanduser("~/.keld/agent.json")
    try:
        cfg = json.load(open(cfg_path))
    except FileNotFoundError:
        print(f"{cfg_path} not found — is keld-agent running? (make install-linux, then start it)", file=sys.stderr)
        return 1
    text = sys.argv[1] if len(sys.argv) > 1 else DEFAULT
    body = json.dumps({
        "source": {"id": "manual_test", "origin": "make", "version": "1"},
        "correlation": {"scheme": "manual", "id": os.urandom(6).hex(), "session_id": "dev"},
        "inline": {"text": text},
    }).encode()
    req = urllib.request.Request(
        f"http://127.0.0.1:{cfg['port']}/enrich",
        data=body,
        headers={"content-type": "application/json", "x-keld-agent-secret": cfg["secret"]},
    )
    with urllib.request.urlopen(req, timeout=5) as r:
        print(f"daemon accepted prompt (HTTP {r.status}); watch your sink / Findings page.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
