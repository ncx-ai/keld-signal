#!/usr/bin/env python3
"""Prove a refactor did not change the numbers.

`snapshot` records a fingerprint of an events frame; `verify` rebuilds and compares. Row counts
are not enough — a reordering or a single changed ref is exactly the class of defect that has
slipped past unit tests here, so the fingerprint hashes the sorted full content, plus a
per-(kind, level) content hash of that group's own sorted rows. Grouping is keyed on
`(kind, level)`, not `level` alone — nothing enforces the three kinds (ref/say/tok) never share a
level name. The content hash (not just rows/total/distinct) is what localises a rename of a
single `ref` value to another value already absent from that level: rows, total, and distinct
count can all stay identical while the actual content differs, and only a hash of the rows
themselves catches that.

Numeric columns are canonicalised to a fixed-point string before hashing, so an int64 column and
a float64 column holding the same values hash identically. A bare `astype(str)` renders `5` and
`5.0` differently and would report a false DIFFERS for a dtype-only change with no numeric
difference at all — exactly the kind of accidental diff a refactor across a package boundary can
introduce. Fixed-point (`.6f`), never significant-figures (`.6g`): `t` is a ~1.7e9 epoch float,
and `%g`-style formatting would truncate its integer part.

Exit codes: 0 identical, 1 differs, 2 the check itself could not run (missing/unreadable input) —
kept distinct from 1 so a caller can tell "the check failed to run" from "the numbers changed".
"""
import argparse, hashlib, json, sys
import pandas as pd


class HarnessError(Exception):
    """The check itself could not run — distinct from a real DIFFERS."""


def _canon(df):
    """Stringify every column for hashing; numeric dtypes go through a fixed-point format so
    int64 vs float64 columns holding the same values render identically."""
    cols = list(df.columns)
    out = {}
    for c in cols:
        if pd.api.types.is_numeric_dtype(df[c]):
            out[c] = df[c].astype(float).map(lambda v: f"{v:.6f}")
        else:
            out[c] = df[c].astype(str)
    return pd.DataFrame(out, columns=cols)


def _hash_rows(df):
    cols = list(df.columns)
    flat = _canon(df).sort_values(cols).to_csv(index=False)
    return hashlib.sha256(flat.encode()).hexdigest()


def fingerprint(outdir):
    path = f"{outdir}/events.parquet"
    try:
        ev = pd.read_parquet(path)
    except Exception as e:
        raise HarnessError(f"cannot read {path}: {e}")
    per_group = {}
    for (kind, lv), g in ev.groupby(["kind", "level"], observed=True):
        per_group[f"{kind}/{lv}"] = {
            "rows": int(len(g)),
            "total": float(g.n.sum()),
            "distinct": int(g.ref.nunique()),
            "sha256": _hash_rows(g),
        }
    return {"rows": int(len(ev)),
            "sha256": _hash_rows(ev),
            "levels": per_group}


def _load_baseline(path):
    try:
        with open(path) as f:
            data = json.load(f)
    except Exception as e:
        raise HarnessError(f"cannot read baseline {path}: {e}")
    for key in ("rows", "sha256", "levels"):
        if key not in data:
            raise HarnessError(f"baseline {path} is malformed: missing '{key}'")
    return data


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("snapshot"); s.add_argument("outdir"); s.add_argument("-o", default="/tmp/identity.json")
    v = sub.add_parser("verify"); v.add_argument("baseline"); v.add_argument("outdir")
    a = ap.parse_args()
    try:
        if a.cmd == "snapshot":
            fp = fingerprint(a.outdir)
            json.dump(fp, open(a.o, "w"), indent=1)
            print(f"snapshot {fp['rows']} rows sha={fp['sha256'][:12]} -> {a.o}")
            return 0
        base = _load_baseline(a.baseline)
        now = fingerprint(a.outdir)
    except HarnessError as e:
        print(f"check_identity: {e}", file=sys.stderr)
        return 2
    if base["sha256"] == now["sha256"]:
        print(f"IDENTICAL  {now['rows']} rows  sha={now['sha256'][:12]}")
        return 0
    print(f"DIFFERS  rows {base['rows']} -> {now['rows']}  "
          f"sha {base['sha256'][:12]} -> {now['sha256'][:12]}")
    for lv in sorted(set(base["levels"]) | set(now["levels"])):
        b, n = base["levels"].get(lv), now["levels"].get(lv)
        if b != n:
            print(f"  {lv:24} {b} -> {n}")
    return 1


if __name__ == "__main__":
    sys.exit(main())
