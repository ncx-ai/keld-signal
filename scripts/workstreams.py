#!/usr/bin/env python3
"""Emit the workstream payload the daemon WOULD publish, for every window of a real corpus.

Built to answer the questions that decide the production build before any of it is written: which
workstreams earn a place, what their buckets look like, and how large the honest unattributed row
is. Nothing here infers — every value is a deterministic reference level, and a window with no
dominant value is reported as unattributed rather than given a plausible one.

    workstreams.py --outdir /tmp/rs-v2 --out ~/keld/refseries-context/workstreams.ndjson
"""
import argparse, json, os, sys, collections
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import pandas as pd

SPAN, STRIDE = pd.Timedelta("60min"), pd.Timedelta("50min")

# ALLOCATION workstreams: spend divides among them, so one value must own the window. The floor is
# what makes "unattributed" honest — below it there is no dominant value and we say so rather than
# picking the largest of several near-equals. 0.5 is deliberate: a bucket holding under half the
# evidence is not what the hour was about.
ALLOCATION = [
    ("project",     "workspace", 0.50),
    ("branch",      "branch",    0.50),
    ("model",       "model",     0.50),
    ("output_type", "artifact",  0.50),
    ("language",    "lang",      0.50),
    ("workflow",    "skill",     0.50),
    ("tooling",     "toolchain", 0.50),
]

# INVENTORY dimensions: multi-valued by nature — "what was used", not "what owns this". No
# dominance requirement, because asking which single tool owns an hour is the wrong question.
INVENTORY = [("harness_tools", "tool"), ("programs", "exe"),
             ("external_systems", "service"), ("integrations", "mcp_tool")]

# Loopback is not an external system. It is 85% of the raw service level and would otherwise be
# the top "system this org depends on".
LOOPBACK = {"127.0.0.1", "localhost", "0.0.0.0", "::1", "enrich-sidecar"}


def windows(ev):
    for sess, g in ev.groupby("session", observed=True):
        t0, t1 = g.ts.min(), g.ts.max()
        t = t0.floor("h")
        while t < t1:
            w = g[(g.ts >= t) & (g.ts < t + SPAN)]
            if len(w):
                yield sess, t, w
            t += STRIDE


def dominant(w, level, floor):
    """The value owning this window, or None. Returns (value, share, total)."""
    g = w[(w.kind == "ref") & (w.level == level)]
    if g.empty:
        return None, 0.0, 0
    tot = g.groupby("ref", observed=True).n.sum().sort_values(ascending=False)
    share = tot.iloc[0] / tot.sum()
    return (str(tot.index[0]), float(share), int(tot.sum())) if share >= floor else (None, float(share), int(tot.sum()))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--outdir", default="/tmp/rs-v2")
    ap.add_argument("--out", required=True)
    a = ap.parse_args()
    ev = pd.read_parquet(os.path.join(a.outdir, "events.parquet"))
    ev["ts"] = pd.to_datetime(ev.ts)

    rows, cover = [], collections.defaultdict(lambda: [0, 0])
    buckets = collections.defaultdict(collections.Counter)
    for sess, t, w in windows(ev):
        ws = {}
        for name, level, floor in ALLOCATION:
            v, share, tot = dominant(w, level, floor)
            cover[name][1] += 1
            if v is not None:
                cover[name][0] += 1
                buckets[name][v] += 1
            ws[name] = None if v is None else {
                "value": v, "share": round(share, 3), "evidence": tot,
                "provenance": "known:tool_inputs"}
        inv = {}
        for name, level in INVENTORY:
            g = w[(w.kind == "ref") & (w.level == level)]
            vals = g.groupby("ref", observed=True).n.sum().sort_values(ascending=False)
            if name == "external_systems":
                vals = vals[~vals.index.isin(LOOPBACK)]
            inv[name] = [{"value": str(k), "n": int(v)} for k, v in vals.head(12).items()]
        rows.append({"session": sess, "window_start": t.isoformat(),
                     "window_end": (t + SPAN).isoformat(),
                     "evidence": int(w[w.kind == "ref"].n.sum()),
                     "workstreams": ws, "inventory": inv})

    with open(a.out, "w") as fh:
        for r in rows:
            fh.write(json.dumps(r) + "\n")

    print(f"{len(rows)} windows -> {a.out}\n")
    print(f"{'workstream':14} {'attributed':>11} {'unattributed':>13}   top buckets")
    for name, _, _ in ALLOCATION:
        ok, tot = cover[name]
        top = ", ".join(f"{k} {v}" for k, v in buckets[name].most_common(4))
        print(f"{name:14} {ok/tot:11.0%} {1-ok/tot:13.0%}   {top}")
    print(f"\n{'inventory':18} windows with any   distinct values")
    for name, level in INVENTORY:
        n = sum(1 for r in rows if r["inventory"][name])
        d = len({x["value"] for r in rows for x in r["inventory"][name]})
        print(f"  {name:18} {n/len(rows):13.0%}   {d}")


if __name__ == "__main__":
    main()
