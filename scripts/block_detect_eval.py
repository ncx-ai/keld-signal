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
import os
import statistics
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))

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
