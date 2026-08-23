#!/usr/bin/env python3
"""Warn (and, past a deliberately high bar, fail) when a sidecar/requirements.txt
pin has sat unrevisited for a long time.

Why this exists: fastapi==0.115.* and uvicorn[standard]==0.32.* were vendored in
commit e51d686 already over a year stale, then sat untouched through 26 (fastapi)
and 20 (uvicorn) further minor releases with nothing in CI ever looking at them —
a trailing-wildcard pin (`0.115.*`) silently freezes forever once upstream moves
past that series, since nothing ever asks pip to consider `0.116`. There was no
signal anywhere that this had happened; it was found by hand.

What this checks, and what it deliberately does NOT check: this measures the AGE
of the exact version currently pinned (its own PyPI upload date), not how many
versions behind "latest" it is and not whether a newer version merely exists.
Version-count and existence are both noisy for a 0.x-versioned, frequently
released package (fastapi ships a new minor roughly monthly) — a check keyed on
either would fire on nearly every run and get disabled, which is exactly the
"cries wolf" failure mode that produced silent drift here in the first place.
Age of the pin itself only grows when the pin itself is left untouched, so it
stays quiet immediately after a bump and only escalates the longer a pin is
genuinely neglected.

Only plain exact pins (`name==X.Y.Z`, optionally with extras: `name[extra]==`)
are checked — wildcard pins (`spacy==3.8.*`) and URL/VCS pins (`en_core_web_sm @
https://...`) are skipped because there is no single resolved version to date
without actually invoking the resolver, which this intentionally avoids (no pip
install, no network dependency beyond one PyPI JSON GET per pin, no torch).
gliner2/torch/transformers/numpy are the ML stack — deliberately excluded even
though gliner2 is exact-pinnable in principle, because bumping any of them needs
enrich/eval revalidation + the opt-in load tests, not just a version check; see
AGENTS.md.

Exit codes: 0 nothing past FAIL threshold (WARNs may still be printed) or a
network hiccup (never fails the build for that — a check that can't reach PyPI
is not evidence a pin is stale); 1 at least one pin is past the FAIL threshold
AND a newer release genuinely exists for it.
"""
import json
import os
import re
import sys
import urllib.error
import urllib.request
from datetime import datetime, timezone

WARN_DAYS = int(os.environ.get("STALENESS_WARN_DAYS", "180"))
FAIL_DAYS = int(os.environ.get("STALENESS_FAIL_DAYS", "365"))
TIMEOUT_S = float(os.environ.get("STALENESS_HTTP_TIMEOUT", "10"))

# name==X.Y.Z  or  name[extra1,extra2]==X.Y.Z  — no wildcard, no URL/VCS pin.
_PIN_RE = re.compile(
    r"^([A-Za-z0-9][A-Za-z0-9._-]*)(?:\[[^\]]+\])?==([0-9][A-Za-z0-9.\-]*)\s*$"
)


def parse_pins(path: str) -> list[tuple[str, str]]:
    pins = []
    with open(path) as f:
        for raw in f:
            line = raw.split("#", 1)[0].strip()
            if not line:
                continue
            m = _PIN_RE.match(line)
            if m:
                pins.append((m.group(1), m.group(2)))
    return pins


def _fetch_json(url: str):
    req = urllib.request.Request(url, headers={"User-Agent": "keld-dep-staleness-check"})
    with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
        return json.load(resp)


def _upload_time(releases: dict, version: str):
    files = releases.get(version)
    if not files:
        return None
    # Prefer a non-yanked file; fall back to whatever is there.
    for f in files:
        if not f.get("yanked"):
            return f.get("upload_time_iso_8601") or f.get("upload_time")
    return files[0].get("upload_time_iso_8601") or files[0].get("upload_time")


def _parse_ts(ts: str) -> datetime:
    ts = ts.replace("Z", "+00:00")
    dt = datetime.fromisoformat(ts)
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt


def check_pin(name: str, pinned: str):
    """Returns (status, message). status is one of OK, WARN, FAIL, SKIP."""
    try:
        data = _fetch_json(f"https://pypi.org/pypi/{name}/json")
    except (urllib.error.URLError, TimeoutError, OSError, json.JSONDecodeError) as e:
        return "SKIP", f"{name}: could not reach PyPI ({e}); not treated as stale"

    latest = data.get("info", {}).get("version")
    releases = data.get("releases", {})
    pinned_ts = _upload_time(releases, pinned)
    if pinned_ts is None:
        return "SKIP", f"{name}=={pinned}: no PyPI release metadata for the pinned version"

    age_days = (datetime.now(timezone.utc) - _parse_ts(pinned_ts)).days

    if latest == pinned:
        return "OK", f"{name}=={pinned} is the current latest release"

    if age_days > FAIL_DAYS:
        return (
            "FAIL",
            f"{name}=={pinned} was released {age_days}d ago (> {FAIL_DAYS}d) and "
            f"latest is {latest} — this pin needs a deliberate look, not another silent skip",
        )
    if age_days > WARN_DAYS:
        return (
            "WARN",
            f"{name}=={pinned} was released {age_days}d ago (> {WARN_DAYS}d); "
            f"latest is {latest}",
        )
    return "OK", f"{name}=={pinned} is {age_days}d old (latest {latest}); within tolerance"


def main() -> int:
    req_path = sys.argv[1] if len(sys.argv) > 1 else "sidecar/requirements.txt"
    pins = parse_pins(req_path)
    if not pins:
        print(f"check_dependency_staleness: no exact `name==version` pins found in {req_path}")
        return 0

    worst = 0
    for name, pinned in pins:
        status, msg = check_pin(name, pinned)
        print(f"[{status}] {msg}")
        if status == "FAIL":
            worst = 1

    if worst:
        print(
            f"\nFAIL: one or more pins in {req_path} exceed {FAIL_DAYS} days old with a "
            "newer release available. Bump it (or, if there is a real reason to hold, say so "
            "in requirements.txt/AGENTS.md so this isn't unexplained drift again)."
        )
    return worst


if __name__ == "__main__":
    sys.exit(main())
