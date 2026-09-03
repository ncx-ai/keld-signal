#!/usr/bin/env python3
"""Run the attribution arms over the benchmark manifest's WITH-INPUT blocks.

    KELD_BENCH_VECS=~/.cache/keld-bench-vecs.pkl \\
    PYTHONPATH=sidecar python scripts/run_block_benchmark.py [--gold context|norepo|both]

Reads `scripts/testdata/block_benchmark.jsonl` via `block_benchmark.load_benchmark`, so the
population is the 64 blocks that have user text — the only ones a text-based scorer can be
judged on. The 25 without input are excluded here by construction, not counted as misses.

Everything is the SHIPPED decision (`attribution.score_block`'s arithmetic: USER-turn text,
per-message max, `cut = max(null, top - MARGIN)`, metadata boost) with the project
definitions as currently declared in `scripts/testdata/keld_projects.json`. Declare repos
there and the repo boost applies; leave them empty — the customer condition — and it does not.
The run prints which condition it measured, so a number is never read without it.

Arms:
  raw          the shipped decision
  centred      per-document offsets (mean similarity over this population's messages)
               subtracted from every score. Leave-one-block-out was measured identical to
               in-sample on this corpus (offsets are means over ~200 vectors), so in-sample
               is used here for speed and the fact is stated rather than hidden.
  centred+cap2 centred, at most two projects per block — the guard against the multi-project
               spray seen when no anchor exists.

Embedding-only: the Gemma verifier is not loaded. It adjudicates the borderline band in
production, so these are the decisions BEFORE verdicts — `test_attribution_quality.py`'s
`cut_only` arm.

The majority baseline (always the most common label) is printed first, because on a corpus
this skewed it is the bar every arm has to clear before it has earned anything.
"""
from __future__ import annotations

import argparse
import glob
import json
import os
import pickle
import re
import statistics
import sys
from collections import Counter
from datetime import datetime

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(ROOT, "sidecar"))
sys.path.insert(0, os.path.join(ROOT, "scripts"))

from block_benchmark import load_benchmark  # noqa: E402

VEC_CACHE = os.path.expanduser(os.environ.get("KELD_BENCH_VECS", "~/.cache/keld-bench-vecs.pkl"))
SEED_CACHE = os.environ.get("KELD_BENCH_SEED_VECS", "")   # an older cache to seed from


def cos(a, b):
    return sum(x * y for x, y in zip(a, b))


def prf(tp, fp, fn):
    p = tp / (tp + fp) if tp + fp else 1.0
    r = tp / (tp + fn) if tp + fn else 1.0
    return p, r, (2 * p * r / (p + r) if p + r else 0.0)


def pg_ts(s):
    t = s.strip().replace(" ", "T")
    if re.search(r"[+-]\d{2}$", t):
        t += ":00"
    try:
        return datetime.fromisoformat(t)
    except ValueError:
        return None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--gold", default="both", choices=["context", "norepo", "both"])
    ap.add_argument("--projects", default="",
                    help="comma-separated project ids to score against (default: all declared)")
    args = ap.parse_args()

    from app.analysis import attribution as A
    from app.analysis import textembed
    from app.analysis.capture import epoch
    from app.analysis.transcript import iter_turns

    projects = json.load(open(os.path.join(ROOT, "scripts", "testdata", "keld_projects.json")))
    if args.projects:
        keep = set(args.projects.split(","))
        projects = [p for p in projects if p["id"] in keep]
    A.set_projects(projects)
    ids = [p["id"] for p in projects]
    declared_repos = sum(len(p.get("repos") or []) for p in projects)

    rows = load_benchmark(with_input_only=True)
    print(f"population: {len(rows)} with-input blocks · {len(projects)} projects · "
          f"{declared_repos} declared repos "
          f"-> {'REPO BOOST ACTIVE' if declared_repos else 'CUSTOMER CONDITION (no repo boost)'}\n")

    # ---- texts -----------------------------------------------------------------
    for r in rows:
        a, b = pg_ts(r["start"]), pg_ts(r["end"])
        hits = glob.glob(os.path.expanduser(f"~/.claude/projects/*/{r['session']}.jsonl"))
        r["texts"] = []
        if hits and a and b:
            try:
                r["texts"] = [m.text for m in textembed.messages_in(iter_turns(hits[0]), epoch)
                              if m.stream == textembed.USER
                              and a.timestamp() <= m.t < b.timestamp()]
            except Exception:      # noqa: BLE001
                pass

    # ---- vectors, cached ---------------------------------------------------------
    cache = {}
    for path in (SEED_CACHE, VEC_CACHE):
        if path and os.path.exists(path):
            cache.update(pickle.load(open(path, "rb")))
    need = [t for r in rows for t in r["texts"] if t not in cache]
    need += [A.project_doc(p) for p in projects if A.project_doc(p) not in cache]
    if A.NULL_DOC not in cache:
        need.append(A.NULL_DOC)
    if need:
        print(f"encoding {len(need)} texts not in cache…", flush=True)
        child = textembed.Encoder()
        vecs, st = child.encode(need)
        if st != textembed.STATUS_OK or len(vecs) != len(need):
            sys.exit(f"encoder not ready: {st}")
        cache.update(zip(need, vecs))
        os.makedirs(os.path.dirname(VEC_CACHE), exist_ok=True)
        pickle.dump(cache, open(VEC_CACHE, "wb"))

    pv = {p["id"]: A._l2(cache[A.project_doc(p)]) for p in projects}
    nullv = A._l2(cache[A.NULL_DOC])
    for r in rows:
        r["tv"] = [A._l2(cache[t]) for t in r["texts"] if t in cache]

    # ---- offsets over THIS population's messages ------------------------------------
    msgvecs = [v for r in rows for v in r["tv"]]
    docs = dict(pv, NULL=nullv)
    off = {k: statistics.mean(cos(m, dv) for m in msgvecs) for k, dv in docs.items()}
    ZERO = {k: 0.0 for k in docs}
    print(f"offsets from {len(msgvecs)} messages: null {off['NULL']:.3f}, projects "
          f"{min(off[i] for i in ids):.3f}-{max(off[i] for i in ids):.3f}\n")

    def decide(r, offsets, cap=None):
        tv = r["tv"]
        if not tv:
            return set()
        dims = {"project": r["workspace_dim"]} if r.get("workspace_dim") else {}
        nl = max(cos(v, nullv) for v in tv) - offsets["NULL"]
        sc = {p["id"]: round(max(cos(v, pv[p["id"]]) for v in tv) - offsets[p["id"]]
                             + A.metadata_boost(p, dims, r["texts"]), 4) for p in projects}
        top = max(sc.values())
        cut = max(nl, top - A.MARGIN)
        chosen = [pid for pid, s in sorted(sc.items(), key=lambda kv: -kv[1])
                  if s >= cut and top > nl]
        return set(chosen[:cap] if cap else chosen)

    arms = {
        "raw (shipped)": lambda r: decide(r, ZERO),
        "centred": lambda r: decide(r, off),
        "centred + cap 2": lambda r: decide(r, off, cap=2),
    }

    golds = ["context", "norepo"] if args.gold == "both" else [args.gold]
    for prov in golds:
        gold = {}
        for r in rows:
            lab = r["labels"].get(prov)
            if lab is None or lab == ["?"]:
                continue
            gold[r["key"]] = {x for x in lab if x not in ("none", "?")}
        keys = [r["key"] for r in rows if r["key"] in gold]
        byk = {r["key"]: r for r in rows}
        maj_label = Counter(x for k in keys for x in gold[k]).most_common(1)[0][0]

        print("=" * 74)
        print(f"GOLD = {prov}: {len(keys)} of {len(rows)} with-input blocks are labelled and "
              f"decidable")
        print("=" * 74)
        print(f"{'arm':<26} {'P':>7} {'R':>7} {'F1':>7} {'cover':>7} {'trust':>7}")
        print("-" * 66)

        def report(name, preds):
            tp = sum(len(preds[k] & gold[k]) for k in keys)
            fp = sum(len(preds[k] - gold[k]) for k in keys)
            fn = sum(len(gold[k] - preds[k]) for k in keys)
            p, rc, f1 = prf(tp, fp, fn)
            lab = [k for k in keys if preds[k]]
            shown = sum(len(preds[k]) for k in lab)
            right = sum(len(preds[k] & gold[k]) for k in lab)
            print(f"{name:<26} {p:>7.3f} {rc:>7.3f} {f1:>7.3f} "
                  f"{len(lab) / len(keys):>6.0%} {(right / shown if shown else 0):>6.0%}")

        report(f"MAJORITY always-{maj_label.replace('proj_', '')}",
               {k: {maj_label} for k in keys})
        for name, fn_ in arms.items():
            report(name, {k: fn_(byk[k]) for k in keys})
        print()


if __name__ == "__main__":
    main()
