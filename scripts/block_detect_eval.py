#!/usr/bin/env python3
"""Which reference levels detect a BLOCK BOUNDARY?

Task 1 of `.superpowers/sdd/2026-08-25-block-detection-measurement/`: ground truth over every
allocation level, with the level(s) under test excluded so a detector is never scored on its own
flips (that would score a tautology at 1.0 and mean nothing).

Reuses `sizer_eval`'s scoring machinery BY IMPORT, never by copy — `CachingStore`, `DB`, `SEED`,
`SPAN_MINUTES`, `Transition`, `active_bins`, `score`, `MIN_ATTRIBUTED`, `MIN_TRANSITIONS` are the
SAME definitions the 2026-08-24 sizer study was scored against, so a later comparison between the
two runs is comparing results, not two forks of the same arithmetic. `MIN_EVIDENCE` and
`window.attribution` are the SHIPPED ones from `app.analysis.window`, imported rather than
restated, so ground truth stays deterministic and derived from the store — never hand-labelled.
"""
import argparse
import collections
import os
import random
import statistics
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))

from app.analysis.dynamics import EwmaSizer, FixedSizer                             # noqa: E402
from app.analysis.store import BIN_SECONDS, open_store                              # noqa: E402
from app.analysis.window import MIN_EVIDENCE, attribution                           # noqa: E402
from app.analysis.workstreams import ALLOCATION                                     # noqa: E402
from sizer_eval import (CachingStore, DB, MIN_ATTRIBUTED, MIN_TRANSITIONS,          # noqa: E402
                        SEED, SPAN_MINUTES, Transition, active_bins, score)

# Every published allocation level — the ground truth's vocabulary.
ALLOC_LEVELS = tuple((level, floor) for _n, level, floor in ALLOCATION)


def transitions(store, session, exclude=(), levels=ALLOC_LEVELS):
    """Every flip of a dominant allocation value, both sides attributed, for one session.

    `exclude` drops the level(s) under test: a detector scored on its own level's flips
    reports a tautology. A tuple rather than a scalar so a pair needs no special case.
    """
    bins = active_bins(store, session)
    rolls = [(t, store.rollup_window(session, t, t + BIN_SECONDS)) for t in bins]
    n_at, out = 0, []
    for level, floor in levels:
        if level in exclude:
            continue
        prev = None
        for t, rl in rolls:
            a = attribution(rl, level, floor, MIN_EVIDENCE)
            if a.reason != "attributed":
                continue
            n_at += 1
            if prev is not None and a.value != prev[1]:
                out.append(Transition(session, level, float(t), prev[1], a.value,
                                      round((t - prev[0]) / 60.0, 1)))
            prev = (t, a.value)
    return n_at, sorted(out, key=lambda x: x.instant)


def shuffled(trans, bins, rng):
    """Rule 2: every transition relocated to a random non-empty bin of the SAME session.

    Count and density are preserved; only the relationship to the work is destroyed. This is
    the control that collapsed the EWMA sizer from 86.4% to 24.1% while every fixed sizer
    barely moved — which is what makes a positive result here believable rather than assumed.
    """
    if not bins:
        return []
    choices = [float(x) for x in bins]
    return sorted([t._replace(instant=rng.choice(choices)) for t in trans],
                  key=lambda x: x.instant)


def EMPTY_AGG():
    return {"hit": 0, "fp": 0, "miss": 0, "fires": 0, "windows": 0, "dists": []}


def merge(into, r):
    for k in ("hit", "fp", "miss", "fires", "windows"):
        into[k] += r[k]
    into["dists"] += r["dists"]
    return into


def rates(agg):
    hit, fp, miss = agg["hit"], agg["fp"], agg["miss"]
    prec = hit / (hit + fp) if (hit + fp) else 0.0
    rec = hit / (hit + miss) if (hit + miss) else 0.0
    fire = agg["fires"] / agg["windows"] if agg["windows"] else 0.0
    med = statistics.median(agg["dists"]) / 60.0 if agg["dists"] else None
    return {"precision": round(100 * prec, 1), "recall": round(100 * rec, 1),
            "fire_rate": round(100 * fire, 1),
            "median_dist_min": None if med is None else round(med, 1),
            "hit": hit, "fp": fp, "miss": miss, "windows": agg["windows"]}


# Every candidate's detection levels are a TUPLE, including singles, so `exclude=` and the sizer
# assignment have exactly one shape. `workspace` is excluded as a candidate: zero transitions
# across 51 sessions, already measured (see `dynamics.py`'s `DETECT_LEVEL` note).
CANDIDATES = (("branch", ("branch",)), ("language", ("lang",)),
              ("output_type", ("artifact",)), ("component", ("component",)),
              ("skill", ("skill",)), ("action", ("action",)),
              ("branch+language", ("branch", "lang")))


def score_level(cs, sessions, levels_under_test, gt_levels, rng, exclude=None):
    """One candidate over every qualifying session: real and shuffled, plus coverage.

    `exclude` defaults to `levels_under_test` itself — the tautology guard: a detector must
    never be scored against ground truth built from its own level's flips. NARROW ground-truth
    mode passes `exclude=()` instead, because narrow ground truth IS workspace+branch and
    excluding `branch` there would leave almost nothing to score against; that mode exists only
    to replicate a published figure where no exclusion was applied.
    """
    exclude = levels_under_test if exclude is None else exclude
    real, shuf = EMPTY_AGG(), EMPTY_AGG()
    n_sample, n_cov, n_trans, skip = 0, 0, 0, collections.Counter()
    floors = dict(ALLOC_LEVELS)
    probe = levels_under_test[0]
    for s in sessions:
        n_at, trans = transitions(cs, s, exclude=exclude, levels=gt_levels)
        bins = active_bins(cs, s)
        if n_at < MIN_ATTRIBUTED:
            skip["too_few_attributed"] += 1; cs.reset(); continue
        if len(trans) < MIN_TRANSITIONS:
            skip["no_transition"] += 1; cs.reset(); continue
        n_sample += 1
        n_trans += len(trans)
        # Rule 5: coverage is REPORTED, never scored. A level with fine precision on 12% of
        # sessions does not solve the non-engineering problem. Always MEASURED against the
        # store: `component`/`action` are INVENTORY levels absent from `ALLOC_LEVELS`, so a
        # bypass keyed on `probe not in floors` would report those two candidates a silent,
        # unconditional 100% instead of a real figure. 0.50 is the floor every allocation
        # dimension uses, and `attribution` works on any level the store holds.
        if has_level(cs, s, probe, floors.get(probe, 0.50)):
            n_cov += 1
        sz = EwmaSizer(name="ewma:" + "+".join(levels_under_test))
        sz.level = probe            # instance attribute shadows the class attribute
        a = anchors_for(cs, s)
        merge(real, score(sz, cs, s, a, trans))
        merge(shuf, score(sz, cs, s, a, shuffled(trans, bins, rng)))
        cs.reset()
    rr, sr = rates(real), rates(shuf)
    return {"detection_levels": list(levels_under_test),
            "gt_excluded": list(exclude),
            "sessions_scored": n_sample, "gt_transitions": n_trans,
            "coverage_pct": round(100 * n_cov / n_sample, 1) if n_sample else 0.0,
            "real": rr, "shuffled": sr,
            "precision_drop": round(rr["precision"] - sr["precision"], 1),
            "skipped": dict(skip)}


def has_level(store, session, level, floor):
    for t in active_bins(store, session):
        if attribution(store.rollup_window(session, t, t + BIN_SECONDS), level, floor,
                       MIN_EVIDENCE).reason == "attributed":
            return True
    return False


def anchors_for(store, session):
    return [float(t) + BIN_SECONDS for t in active_bins(store, session)]


def run(gt_mode):
    st = open_store(DB)
    cs = CachingStore(st)
    sessions = [s for (s,) in st._conn().execute(
        "SELECT DISTINCT session FROM bin ORDER BY session")]
    gt_levels = (tuple((lv, fl) for lv, fl in ALLOC_LEVELS
                       if lv in ("workspace", "branch")) if gt_mode == "narrow"
                 else ALLOC_LEVELS)
    rng = random.Random(SEED)
    out = {"gt_mode": gt_mode, "sessions_seen": len(sessions), "levels": {}}
    for label, lv in CANDIDATES:
        # In NARROW mode the ground truth IS workspace/branch, so excluding `branch` would
        # leave almost nothing to score against. Narrow exists only to replicate the
        # 2026-08-24 numbers, where no exclusion was applied.
        r = score_level(cs, sessions, lv, gt_levels, rng,
                        exclude=() if gt_mode == "narrow" else None)
        out["levels"][label] = r
        print(f"  {label:<16} n={r['sessions_scored']:<3} "
              f"prec={r['real']['precision']:>5} rec={r['real']['recall']:>5} "
              f"fire={r['real']['fire_rate']:>5} drop={r['precision_drop']:>5} "
              f"cov={r['coverage_pct']:>5}%", flush=True)
    fx = EMPTY_AGG()
    for s in sessions:
        n_at, trans = transitions(cs, s, exclude=(), levels=gt_levels)
        if n_at >= MIN_ATTRIBUTED and len(trans) >= MIN_TRANSITIONS:
            merge(fx, score(FixedSizer(15), cs, s, anchors_for(cs, s), trans))
        cs.reset()
    out["fixed_15"] = rates(fx)
    print(f"  {'FixedSizer(15)':<16} prec={out['fixed_15']['precision']:>5} "
          f"rec={out['fixed_15']['recall']:>5}", flush=True)
    return out


if __name__ == "__main__":
    # Prints only — results derive from real developer transcripts and must never land inside
    # the working tree. Task 5 pipes stdout (e.g. `tee`) to a path under
    # ~/keld/refseries-context/blocks/ and writes its own JSON there.
    ap = argparse.ArgumentParser()
    ap.add_argument("--gt", choices=("wide", "narrow", "both"), default="wide")
    args = ap.parse_args()
    modes = ("wide", "narrow") if args.gt == "both" else (args.gt,)
    for mode in modes:
        print(f"--- gt={mode} ---", flush=True)
        run(mode)
