#!/usr/bin/env python3
"""Refresh internal/agent/pricing/prices.json from LiteLLM's public table.

Run at RELEASE TIME, never at runtime: the daemon must never fetch a price list
on a user's machine, and an estimate whose source changed under a running
process is worse than one that is simply a week old (the page states the
snapshot's date).

    python3 scripts/refresh-prices.py            # fetch upstream
    python3 scripts/refresh-prices.py --from FILE # use a local snapshot

Upstream shape: {model_id: {"input_cost_per_token": …, "output_cost_per_token": …,
"cache_creation_input_token_cost": …, "cache_read_input_token_cost": …, …}}.
Ours is explicit and carries only the ids a Keld-supported tool can emit — the
upstream table is ~4,700 rows of models nobody here runs, and an unmatched model
is a stated "no estimate", never a guess.
"""
import argparse
import datetime
import json
import os
import re
import sys
import urllib.request

UPSTREAM = ("https://raw.githubusercontent.com/BerriAI/litellm/main/"
            "model_prices_and_context_window.json")

# Ids as the TOOLS report them. Anything else is dropped rather than kept "just
# in case": a table nobody can match against is 300 KB of dead weight.
KEEP = re.compile(r"^(claude-|gpt-|o1|o3|o4|gemini-|codex-|grok-)")
SKIP = re.compile(r"(ft:|/|:free|-latest-|azure|vertex_ai|bedrock)")

# Rates for models a Keld-supported tool already emits but LiteLLM does not list
# yet. Applied ONLY where upstream has no row for the id, so the published rate
# takes over the day it lands. Each entry names where its numbers came from;
# nothing goes here from memory.
PENDING_UPSTREAM = {
    # Anthropic's published Claude Opus 5.5 rates: $4 / $20 per MTok, cache
    # reads $0.20. Cache write is DERIVED — 1.25x input, the ratio every Opus
    # row upstream carries ($6.25 on $5). Added 2026-09-29: this model carried
    # 805M tokens in 30 days at $0 on one machine.
    "claude-opus-5-5": {"in": 4e-06, "out": 2e-05, "cache_write": 5e-06, "cache_read": 2e-07},
    "claude-opus-5-5@default": {"in": 4e-06, "out": 2e-05, "cache_write": 5e-06, "cache_read": 2e-07},
}

DST = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
                   "internal", "agent", "pricing", "prices.json")


def num(v):
    return v if isinstance(v, (int, float)) else None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--from", dest="src", help="a local upstream JSON instead of fetching")
    ap.add_argument("--out", default=DST)
    a = ap.parse_args()

    if a.src:
        raw = json.load(open(a.src))
        source = f"local snapshot {a.src}"
    else:
        with urllib.request.urlopen(UPSTREAM, timeout=60) as r:
            raw = json.loads(r.read())
        source = UPSTREAM

    out = {}
    for mid, row in raw.items():
        if not isinstance(row, dict) or SKIP.search(mid) or not KEEP.match(mid):
            continue
        i, o = num(row.get("input_cost_per_token")), num(row.get("output_cost_per_token"))
        if i is None or o is None:
            continue
        e = {"in": i, "out": o}
        cw = num(row.get("cache_creation_input_token_cost"))
        cr = num(row.get("cache_read_input_token_cost"))
        if cw is not None:
            e["cache_write"] = cw
        if cr is not None:
            e["cache_read"] = cr
        out[mid] = e

    for mid, e in PENDING_UPSTREAM.items():
        out.setdefault(mid, e)

    if len(out) < 100:
        sys.exit(f"refusing to write a table of {len(out)} models — upstream shape changed?")

    prev = {}
    if os.path.exists(a.out):
        prev = json.load(open(a.out)).get("models", {})
    added = sorted(set(out) - set(prev))
    removed = sorted(set(prev) - set(out))
    changed = sorted(k for k in set(out) & set(prev) if out[k] != prev[k])

    doc = {
        "generated": datetime.date.today().isoformat(),
        "source": source,
        "note": "USD per token. An id absent here has NO estimate; the page says so rather than guessing.",
        "models": dict(sorted(out.items())),
    }
    with open(a.out, "w") as f:
        json.dump(doc, f, indent=1, sort_keys=False)
        f.write("\n")
    print(f"{len(out)} models -> {a.out}")
    print(f"  added {len(added)}, removed {len(removed)}, repriced {len(changed)}")
    for k in changed[:20]:
        print(f"    {k}: {prev[k]} -> {out[k]}")
    if removed:
        print("  ⚠️ removed ids lose their estimate on the next release:", ", ".join(removed[:10]))


if __name__ == "__main__":
    main()
