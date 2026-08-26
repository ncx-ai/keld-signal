#!/usr/bin/env python3
"""Emit the workstream payload the daemon WOULD publish, for every window of a real corpus.

Built to answer the questions that decide the production build before any of it is written: which
workstreams earn a place, what their buckets look like, and how large the honest unattributed row
is. Nothing here infers — every value is a deterministic reference level, and a window with no
dominant value is reported as unattributed rather than given a plausible one.

A thin CLI over `app.analysis.window` + `app.analysis.workstreams`: the rollup, dominance and
payload assembly all live in the package now (Counter-based, pandas-free) so the study and the
daemon share the same measured behaviour. Only the session/time windowing over the parquet events
frame stays here — that frame is study-only.

    workstreams.py --outdir /tmp/rs-v2 --out ~/keld/refseries-context/workstreams.ndjson
"""
import argparse, json, os, sys, collections
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))
import pandas as pd

from app.analysis.window import rollup  # noqa: E402
from app.analysis.workstreams import ALLOCATION, INVENTORY, payload  # noqa: E402

SPAN, STRIDE = pd.Timedelta("60min"), pd.Timedelta("50min")

# Column order matches the row shape `window.rollup` expects: (t, session, repo, branch, side,
# kind, level, ref, n) — the same tuple `levels.events_for_turns` produces per row.
ROW_COLS = ["t", "session", "repo", "branch", "side", "kind", "level", "ref", "n"]


def windows(ev):
    for sess, g in ev.groupby("session", observed=True):
        t0, t1 = g.ts.min(), g.ts.max()
        t = t0.floor("h")
        while t < t1:
            w = g[(g.ts >= t) & (g.ts < t + SPAN)]
            if len(w):
                yield sess, t, w
            t += STRIDE


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
        rl = rollup(list(w[ROW_COLS].itertuples(index=False, name=None)))
        doc = payload(rl)
        for name, _, _ in ALLOCATION:
            v = doc["workstreams"][name]
            # `status`, not a null check: sidecar SCHEMA 16 answers every dimension with an
            # object, sub-floor ones included, so coverage is the attributed share and nothing
            # else. Same bar, different field.
            cover[name][1] += 1
            if v is not None and v.get("status") == "attributed":
                cover[name][0] += 1
                buckets[name][v["value"]] += 1
        rows.append({"session": sess, "window_start": t.isoformat(),
                     "window_end": (t + SPAN).isoformat(),
                     "evidence": int(w[w.kind == "ref"].n.sum()),
                     "schema": doc["schema"],
                     "workstreams": doc["workstreams"], "inventory": doc["inventory"]})

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
    for name, _level, _cap in INVENTORY:
        n = sum(1 for r in rows if r["inventory"][name])
        d = len({x["value"] for r in rows for x in r["inventory"][name]})
        print(f"  {name:18} {n/len(rows):13.0%}   {d}")


if __name__ == "__main__":
    main()
