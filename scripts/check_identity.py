#!/usr/bin/env python3
"""Prove a refactor did not change the numbers.

`snapshot` records a fingerprint of an events frame; `verify` rebuilds and compares. Row counts
are not enough — a reordering or a single changed ref is exactly the class of defect that has
slipped past unit tests here, so the fingerprint is a hash of the sorted full content plus
per-level vocabulary and totals, which localises any difference to a level.
"""
import argparse, hashlib, json, sys
import pandas as pd


def fingerprint(outdir):
    ev = pd.read_parquet(f"{outdir}/events.parquet")
    cols = list(ev.columns)
    flat = ev.astype(str).sort_values(cols).to_csv(index=False)
    per_level = {}
    for lv, g in ev.groupby("level", observed=True):
        per_level[str(lv)] = {"rows": int(len(g)), "total": float(g.n.sum()),
                              "distinct": int(g.ref.nunique())}
    return {"rows": int(len(ev)),
            "sha256": hashlib.sha256(flat.encode()).hexdigest(),
            "levels": per_level}


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("snapshot"); s.add_argument("outdir"); s.add_argument("-o", default="/tmp/identity.json")
    v = sub.add_parser("verify"); v.add_argument("baseline"); v.add_argument("outdir")
    a = ap.parse_args()
    if a.cmd == "snapshot":
        fp = fingerprint(a.outdir)
        json.dump(fp, open(a.o, "w"), indent=1)
        print(f"snapshot {fp['rows']} rows sha={fp['sha256'][:12]} -> {a.o}")
        return 0
    base = json.load(open(a.baseline))
    now = fingerprint(a.outdir)
    if base["sha256"] == now["sha256"]:
        print(f"IDENTICAL  {now['rows']} rows  sha={now['sha256'][:12]}")
        return 0
    print(f"DIFFERS  rows {base['rows']} -> {now['rows']}  "
          f"sha {base['sha256'][:12]} -> {now['sha256'][:12]}")
    for lv in sorted(set(base["levels"]) | set(now["levels"])):
        b, n = base["levels"].get(lv), now["levels"].get(lv)
        if b != n:
            print(f"  {lv:18} {b} -> {n}")
    return 1


if __name__ == "__main__":
    sys.exit(main())
