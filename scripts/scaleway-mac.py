#!/usr/bin/env python3
"""Provision / tear down a Scaleway Apple silicon Mac for testing the keld-signal installer.

Driven by the Makefile: `make scaleway-up` / `make scaleway-down` / `make scaleway-status`.
Thin wrapper over the `scw` CLI (https://github.com/scaleway/scaleway-cli) — no third-party
Python deps (stdlib only), so it runs under any python3.

WHY THIS EXISTS: there is no local macOS on a Linux dev box, and VirtualBox can only run
*Intel* macOS while we ship an *arm64* build — so the installer has to be tested on a real
(Apple-hardware) cloud Mac. This automates the fiddly parts: stock is intermittent and new
accounts have zero quota, so `up` polls every type cheapest-first and grabs the first one
that is both in stock AND permitted by your quota.

PREREQUISITES (see the Makefile header for the authoritative list):
  * `scw` on PATH and authenticated — run `scw init` once.
  * An SSH public key registered in your Scaleway project BEFORE `up`, or SSH won't work
    on the new Mac (VNC still does regardless).
  * Apple silicon quota > 0 for at least one type. New orgs start at 0; M1-M is usually the
    only pre-cleared type. Request more in the console (Quotas) for M2/M4 types.

BILLING: Apple silicon has a 24-HOUR MINIMUM. `down` before 24h have elapsed still bills the
full 24h. Rates ~€0.11/hr (M1-M) to ~€0.29/hr (M4-M).

DO NOT enable FileVault on the Mac — it blocks all remote (VNC/SSH) access until someone
types the disk password at a physical keyboard, which you do not have.

Env knobs (set by the Makefile, override there or inline):
  SCALEWAY_ZONE          Scaleway zone (default fr-par-1 — where the Mac types live)
  SCALEWAY_UP_TIMEOUT    seconds to keep polling for stock before giving up (default 1800)
  SCALEWAY_POLL_INTERVAL seconds between stock checks (default 60)
  YES=1                  `down` only: skip the interactive delete confirmation
"""
from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import time

ZONE = os.environ.get("SCALEWAY_ZONE", "fr-par-1")
UP_TIMEOUT = int(os.environ.get("SCALEWAY_UP_TIMEOUT", "1800"))
POLL_INTERVAL = int(os.environ.get("SCALEWAY_POLL_INTERVAL", "60"))

# Cheapest-first. Price is EUR/hr; M4-SP is not on the public pricing page so its rate is
# approximate. `up` tries these in order and takes the first available + quota-permitted one.
TYPES = [("M1-M", 0.11), ("M2-M", 0.17), ("M4-S", 0.22), ("M4-SP", 0.24), ("M4-M", 0.29)]


def scw_bin() -> str:
    for cand in (os.environ.get("SCW_BIN"), shutil.which("scw"),
                 os.path.expanduser("~/.local/bin/scw")):
        if cand and os.path.exists(cand):
            return cand
    sys.exit("scw CLI not found. Install it and run `scw init`. See the Makefile header.")


SCW = scw_bin()


def scw(args: list[str]) -> tuple[int, str, str, object]:
    """Run scw with JSON output; return (rc, stdout, stderr, parsed-or-None)."""
    r = subprocess.run([SCW, *args, "-o", "json"], capture_output=True, text=True)
    parsed = None
    if r.stdout.strip():
        try:
            parsed = json.loads(r.stdout)
        except json.JSONDecodeError:
            parsed = None
    return r.returncode, r.stdout, r.stderr, parsed


def list_servers() -> list[dict]:
    rc, out, err, data = scw(["apple-silicon", "server", "list", f"zone={ZONE}"])
    if rc != 0:
        sys.exit(f"`scw apple-silicon server list` failed (are you `scw init`'d?):\n{err or out}")
    items = data if isinstance(data, list) else []
    return [s for s in items if isinstance(s, dict)]


def print_info(s: dict) -> None:
    ip = s.get("ip") or s.get("ip_address")
    print(f"  id:      {s.get('id')}")
    print(f"  type:    {s.get('type')}")
    print(f"  status:  {s.get('status')}")
    print(f"  IP:      {ip}")
    if s.get("vnc_url"):
        print(f"  VNC:     {s.get('vnc_url')}   (host+port for your VNC client, e.g. Remmina)")
    if s.get("deletable_at"):
        print(f"  24h commitment ends: {s.get('deletable_at')}")
    os_ = s.get("os") or {}
    if os_:
        print(f"  OS:      {os_.get('label') or os_.get('name')}")
    print()
    print("  → VNC username/password are on the server's Overview page in the Scaleway console")
    print("    (the API does not return the password). The exact SSH command is there too.")
    print(f"  → full raw detail: scw apple-silicon server get {s.get('id')} zone={ZONE}")


def wait_ready(server_id: str, timeout: int = 1200) -> dict:
    """Poll until the Mac finishes provisioning (~minutes). Returns the latest server dict."""
    deadline = time.time() + timeout
    last = None
    s: dict = {}
    while time.time() < deadline:
        *_, data = scw(["apple-silicon", "server", "get", server_id, f"zone={ZONE}"])
        s = data if isinstance(data, dict) else {}
        status = s.get("status")
        if status != last:
            print(f"  status: {status}")
            last = status
        if status == "ready":
            return s
        if status in ("error", "locked"):
            sys.exit(f"server entered '{status}' — check the console.")
        time.sleep(15)
    print("  (not ready yet after wait; re-check later with `make scaleway-status`)")
    return s


def up() -> None:
    # Idempotent: if a Mac already exists we reuse it rather than create (and bill) a second.
    existing = [s for s in list_servers() if s.get("status") != "deleting"]
    if existing:
        print("A Mac already exists in this project — reusing it (not creating another):\n")
        for s in existing:
            print_info(s)
        return

    print(f"No Mac yet. Polling {ZONE} cheapest-first (timeout {UP_TIMEOUT}s, "
          f"every {POLL_INTERVAL}s). Ctrl-C to stop.\n")
    blacklist: set[str] = set()  # types that returned quota 0/0 or a non-stock failure
    deadline = time.time() + UP_TIMEOUT
    attempt = 0
    while time.time() < deadline:
        attempt += 1
        *_, types = scw(["apple-silicon", "server-type", "list", f"zone={ZONE}"])
        type_list = types if isinstance(types, list) else []
        stock = {t["name"]: t.get("stock") for t in type_list if isinstance(t, dict)}
        line = ", ".join(f"{n}={stock.get(n)}" for n, _ in TYPES)
        print(f"[{attempt}] {line}" + (f"  (skipping {sorted(blacklist)})" if blacklist else ""))

        for name, price in TYPES:
            if name in blacklist or not stock.get(name) or stock.get(name) == "no_stock":
                continue
            print(f">>> {name} in stock (€{price}/hr, ~€{price * 24:.2f}/24h) — creating…")
            rc, o, e, _ = scw(["apple-silicon", "server", "create", f"type={name}", f"zone={ZONE}"])
            blob = (o + e).lower()
            if "quota" in blob and "0/0" in blob.replace(" ", ""):
                print(f"    {name}: quota 0/0 (account not cleared for this type) — skipping")
                blacklist.add(name)
                continue
            if "out of stock" in blob:
                print(f"    {name}: lost the stock race — will retry")
                continue
            if rc != 0:
                print(f"    {name}: create failed — {e or o}")
                blacklist.add(name)
                continue
            s = json.loads(o)
            print(f">>> created {name} (id {s.get('id')}). Waiting for it to boot…")
            s = wait_ready(s["id"])
            print("\n✅ Mac is up:\n")
            print_info(s)
            return

        if len(blacklist) == len(TYPES):
            sys.exit("\nEvery type is quota-blocked (0/0) or failed. Request an Apple-silicon "
                     "quota increase in the console (Quotas) for a high-stock type, then re-run "
                     "`make scaleway-up`.")
        time.sleep(POLL_INTERVAL)

    sys.exit(f"\nTimed out after {UP_TIMEOUT}s — nothing in stock that you have quota for. "
             "Re-run, raise SCALEWAY_UP_TIMEOUT, or request a quota increase.")


def down() -> None:
    servers = [s for s in list_servers() if s.get("status") != "deleting"]
    if not servers:
        print("No Mac to delete.")
        return
    print("About to DELETE:")
    for s in servers:
        print(f"  {s.get('id')}  {s.get('type')}  {s.get('status')}  ip={s.get('ip')}")
    print("\n⚠️  Apple silicon bills a 24-HOUR MINIMUM — deleting before 24h still charges the full day.")
    if os.environ.get("YES") != "1":
        if not sys.stdin.isatty():
            sys.exit("Non-interactive shell; refusing to delete without YES=1.")
        if input("Type 'yes' to delete: ").strip().lower() != "yes":
            print("Aborted.")
            return
    for s in servers:
        rc, o, e, _ = scw(["apple-silicon", "server", "delete", s["id"], f"zone={ZONE}"])
        print(f"  delete {s['id']}: {'ok' if rc == 0 else (e or o).strip()}")


def status() -> None:
    servers = list_servers()
    if not servers:
        print(f"No Apple silicon Macs in this project (zone {ZONE}).")
        return
    for s in servers:
        print_info(s)


COMMANDS = {"up": up, "down": down, "status": status}


def main() -> None:
    cmd = sys.argv[1] if len(sys.argv) > 1 else ""
    fn = COMMANDS.get(cmd)
    if not fn:
        sys.exit(f"usage: scaleway-mac.py {{{'|'.join(COMMANDS)}}}")
    fn()


if __name__ == "__main__":
    main()
