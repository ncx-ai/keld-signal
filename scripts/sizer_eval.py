#!/usr/bin/env python3
"""Does an ADAPTIVE sizer choose `/analyze`'s dynamics slice better than a fixed constant?

Task 3 of docs/superpowers/plans/2026-08-24-dynamics-in-analyze.md. The rules this script is
scored against were written down BEFORE it ran and are NOT restated from memory here — they live
in `~/keld/refseries-context/dynamics/SIZER-PREREGISTRATION.md`. This file implements them.

THIS IS AN EXPERIMENT, NOT A FEATURE. Nothing here is imported by the sidecar. It plugs the rival
sizers into the SHIPPED seam (`app.analysis.dynamics.Sizer.plan` / `series`) precisely so that
"the seam admits an adaptive implementation unreshaped" is demonstrated rather than asserted, and
so a winner could be moved into `dynamics.py` by copying the class.

GROUND TRUTH — deterministic, from the store, never hand-labelled
----------------------------------------------------------------
A transition is where the dominant allocation value CHANGES across the reference series:
`workspace` first, then `branch`, each read through the shipped `window.attribution` at the
shipped `MIN_EVIDENCE`. Only a flip whose BOTH sides are `attributed` counts — a flip out of
`absent` is not a transition, which is the distinction Task 1 shipped `REASONS` to make.

Two decisions the pre-registration leaves to the implementation, stated here because both are
load-bearing:

  * THE FLIP IS BETWEEN SUCCESSIVE ATTRIBUTED BINS, not successive bins. Unattributed bins
    between them do not break the chain; if they did, every transition separated by one thin bin
    would vanish and the ground truth would measure evidence density rather than change. The
    transition INSTANT is the start of the later attributed bin — the earliest moment at which
    the new value is observable at the series' own resolution.
  * The gap between those two bins is recorded, and results are re-reported over the subset with
    a gap <= `CONTIGUOUS_GAP_MIN`, because a flip across an overnight gap is a real change of
    work whose instant is genuinely ambiguous and no detector can be blamed for missing it.

WHAT A "DETECTED CHANGE POINT" IS
---------------------------------
A `Sizer` returns a `Slicing`, not a change-point list: one boundary per window. So the detection
being scored is that ONE instant, taken from `detail["detected_at"]` when the sizer fired and
from `slice_start` otherwise (which is how `FixedSizer` is scored: a constant offset IS its only
claim about where the current state of work began). Scoring uses the RAW detected instant, never
the clamped `slice_start`, so a sizer is judged on what it detected rather than on the budget's
half-span cap; the cap's effect is reported separately as the slice-length distribution.

THE OBSERVATION STREAM the detectors consume
--------------------------------------------
ADWIN/PageHinkley/KSWIN take a float at a time, and the series is CATEGORICAL, so an encoding is
required and it is the same encoding for every rival:

    novelty(bucket) = 1 - (bucket evidence in the RUNNING MODE value) / (bucket total)

0.0 while the window keeps doing what it was doing, ->1.0 for as long as a newly-arrived value
outweighs nothing. It is a share, so it is invariant to how busy the bucket was — the same
normalisation argument `dynamics.compare` makes. Empty buckets yield NO observation (a bucket
with no evidence is not a bucket with no change), and the first observation is 0.0 by definition
since there is nothing yet to be novel against.

Buckets are `DETECT_STEP_S` = 60 s, not the 5-minute bin: `series(..., step=...)` allows it, it
stays strictly inside the span budget, and it is read exactly from `event` rows rather than
interpolated. It is the most data any sizer can have inside the budget — 60 observations instead
of 12. At the 5-minute bin, KSWIN's reference window (20) exceeds the observation count and the
detector could not fire at all, which would test the bin width rather than the detector.

DETECTOR CALIBRATION, and why it is not fitting
-----------------------------------------------
river's defaults are built for long streams and are silent on a 60-observation one: ADWIN's
`clock=32` only tests twice in a whole window, and PageHinkley's `threshold=50` is unreachable by
a cumulative sum of values in [0, 1]. Left at defaults the experiment would have measured river's
parameter defaults, not river. So each detector is set to the most sensitive parameterisation
that is still SILENT on synthetic flat streams of the same length (`calibrate`), and the
calibration uses synthetic data only — it never sees the corpus and cannot see ground truth.

SCORING UNIT is a WINDOW: one anchor `end` per non-empty 5-minute bin of a qualifying session,
looking back `SPAN_MINUTES` — the digest's own budget. Precision is over detections, recall over
(window, transition) pairs, and both are reported alone, never as an F-score.

    ~/.keld/sidecar-venv/bin/python scripts/sizer_eval.py calibrate
    ~/.keld/sidecar-venv/bin/python scripts/sizer_eval.py truth
    ~/.keld/sidecar-venv/bin/python scripts/sizer_eval.py run
    ~/.keld/sidecar-venv/bin/python scripts/sizer_eval.py control
"""
import argparse
import collections
import json
import os
import random
import statistics
import sys
import time
from datetime import datetime, timedelta, timezone

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))
from app.analysis.dynamics import (DEFAULT_SIZER, EwmaSizer, FixedSizer,  # noqa: E402
                                   MATERIAL, READINGS, Sizer, Slicing, _absent_mass,
                                   _status, compare, dynamics, series)
from app.analysis.levels import quantize                                  # noqa: E402
from app.analysis.store import BIN_SECONDS, open_store                    # noqa: E402
from app.analysis.window import MIN_EVIDENCE, attribution                 # noqa: E402
from app.analysis.workstreams import ALLOCATION, INVENTORY                 # noqa: E402

# --- PRE-REGISTERED, fixed before any score was looked at ---------------------------------
OUT = os.path.expanduser("~/keld/refseries-context/dynamics")
DB = os.path.join(OUT, "frozen-corpus.db")
# `workspace` first, then `branch` — the pre-registration's order, and both are measured.
GT_LEVELS = tuple((name, level, floor) for name, level, floor in ALLOCATION
                  if level in ("workspace", "branch"))
TOLERANCE_S = BIN_SECONDS       # a HIT is within 5 min: the bin width, so sub-bin precision is
                                # not claimed. Pre-registered.
SPAN_MINUTES = 60               # the digest's window == the sizer's budget
DETECT_STEP_S = 60              # observation bucket for the detectors (see module docstring)
MIN_ATTRIBUTED = 2              # a session needs >= 2 attributed windows ...
MIN_TRANSITIONS = 1             # ... and >= 1 real transition, or it cannot discriminate
CONTIGUOUS_GAP_MIN = 15         # sensitivity cut: transitions whose two bins are this close
FIXED_SWEEP = (5, 10, 15, 20, 25, 30)   # the SLICE_MINUTES curve, whatever wins
SEED = 20260824
# -----------------------------------------------------------------------------------------


class CachingStore:
    """`rollup_window` memoised. Every sizer sees the identical shipped query; windows overlap
    twelvefold and the detectors re-walk the same buckets, so without this the experiment is
    dominated by re-answering questions whose answer cannot have changed (the store is frozen)."""

    def __init__(self, st):
        self._st, self._c = st, {}

    def rollup_window(self, session, start, end, exclude_slots=()):
        k = (session, start, end, exclude_slots)
        v = self._c.get(k)
        if v is None:
            v = self._c[k] = self._st.rollup_window(session, start, end, exclude_slots)
        return v

    def reset(self):
        self._c.clear()

    def __getattr__(self, n):
        return getattr(self._st, n)


# --- ground truth ----------------------------------------------------------------------------

Transition = collections.namedtuple("Transition", "session level instant before after gap_min")


def active_bins(store, session):
    """The non-empty 5-minute bins of a session, ascending. A bin with no `bin` row has no event
    (bins are derived from events), so its rollup is empty and every level in it is `absent` —
    it can neither be attributed nor be one side of a transition."""
    return [t for (t,) in store._conn().execute(
        "SELECT DISTINCT bin_ts FROM bin WHERE session=? ORDER BY bin_ts", (session,))]


def ground_truth(store, session):
    """`{level: (n_attributed_bins, [Transition, ...])}` for one session."""
    bins = active_bins(store, session)
    rolls = [(t, store.rollup_window(session, t, t + BIN_SECONDS)) for t in bins]
    out = {}
    for _name, level, floor in GT_LEVELS:
        prev, n_at, trans = None, 0, []
        for t, rl in rolls:
            a = attribution(rl, level, floor, MIN_EVIDENCE)
            if a.reason != "attributed":
                continue
            n_at += 1
            if prev is not None and a.value != prev[1]:
                trans.append(Transition(session, level, float(t), prev[1], a.value,
                                        round((t - prev[0]) / 60.0, 1)))
            prev = (t, a.value)
        out[level] = (n_at, trans)
    return out


# --- the rival sizers, all behind the shipped seam --------------------------------------------

class DetectorSizer(Sizer):
    """A sizer whose boundary is the LAST change point a sequential detector signalled inside the
    budget, falling back to the fixed constant when it never fired.

    The fallback is deliberate and is what makes the degeneracy rule measurable: a detector that
    never fires reduces to `FixedSizer` exactly, so its fire rate — not its score — is what
    exposes it. `detail["detected_at"]` is None in that case and the window is scored as no
    detection at all, which is what "a detected change point" means for a sequential detector."""

    level = "branch"        # deliberately the level ground truth is read from: the most generous
                            # signal available, so a null result cannot be blamed on the input
    step = DETECT_STEP_S
    fallback_minutes = 15.0

    def observations(self, store, session, start, end):
        """`[(bucket_start_epoch, novelty)]` — see the module docstring on the encoding."""
        seen, out = collections.Counter(), []
        for t, items in series(store, session, start, end, self.level, step=self.step):
            total = sum(n for _ref, n in items)
            if not total:
                continue
            if seen:
                # running mode, tie-broken alphabetically to match `window.rollup`'s order
                ref = min(seen.items(), key=lambda kv: (-kv[1], kv[0]))[0]
                out.append((t, 1.0 - dict(items).get(ref, 0) / total))
            else:
                out.append((t, 0.0))
            for r, n in items:
                seen[r] += n
        return out

    def fire_indices(self, xs):
        raise NotImplementedError

    def plan(self, store, session, end, span_minutes, floor=None):
        end_dt = end if not isinstance(end, (int, float)) else \
            datetime.fromtimestamp(end, tz=timezone.utc)
        span = float(span_minutes)
        lo = end_dt - timedelta(minutes=span)
        obs = self.observations(store, session, lo, end_dt)
        idx = self.fire_indices([x for _t, x in obs])
        detected = obs[idx[-1]][0] if idx else None
        detail = {"detected_at": detected, "observations": len(obs), "fires": len(idx)}
        if detected is None:
            sl = min(self.fallback_minutes, span / 2.0)
            detail["fallback"] = True
        else:
            sl = (end_dt.timestamp() - detected) / 60.0
            # The budget's own rule, from FixedSizer: a baseline no longer than the slice it
            # judges is a peer, not a baseline. Clamped for the PRODUCTION boundary only —
            # scoring reads `detected_at`, so the clamp cannot flatter or punish the detection.
            sl = max(float(BIN_SECONDS) / 60.0, min(sl, span / 2.0))
            detail["clamped"] = sl != (end_dt.timestamp() - detected) / 60.0
        slice_start = end_dt - timedelta(minutes=sl)
        baseline_start = end_dt - timedelta(minutes=span)
        fl = None if floor is None else (
            floor if not isinstance(floor, (int, float))
            else datetime.fromtimestamp(floor, tz=timezone.utc))
        if fl is not None and fl > baseline_start:
            baseline_start = fl
            detail["baseline_clamped"] = True
        detail["slice_minutes"] = sl
        return Slicing(slice_start, end_dt, baseline_start, self.name, detail)


# `EwmaSizer` is IMPORTED from app.analysis.dynamics, not defined here. It was defined here when
# it was a rival; it won, so it moved into the package and this script now scores the shipped
# class itself. That is deliberate: re-running `run` re-derives the committed table from the code
# that is actually in production, so a port that changed the behaviour would show up as a changed
# number rather than as nothing. Its `plan` differs from `DetectorSizer`'s only in naming the
# slice-length clamp `slice_clamped` (the retention clamp keeps `clamped`, matching FixedSizer);
# scoring reads `detected_at`, which is unchanged.


class RiverSizer(DetectorSizer):
    def __init__(self, factory, name):
        self._factory, self.name = factory, name

    def fire_indices(self, xs):
        d = self._factory()
        out = []
        for i, x in enumerate(xs):
            d.update(x)
            if d.drift_detected:
                out.append(i)
        return out


def river_sizers():
    from river import drift
    return [
        # CALIBRATED (see `calibrate`): the most sensitive setting silent on a flat stream of the
        # same length. river's own defaults never fire inside a 60-observation budget.
        RiverSizer(lambda: drift.ADWIN(delta=0.3, clock=4, grace_period=5,
                                       min_window_length=3), "adwin"),
        RiverSizer(lambda: drift.PageHinkley(min_instances=5, delta=0.005, threshold=1.0),
                   "page_hinkley"),
        RiverSizer(lambda: drift.KSWIN(alpha=0.005, window_size=20, stat_size=7, seed=SEED),
                   "kswin"),
    ]


class NamedFixed(FixedSizer):
    def __init__(self, minutes):
        super().__init__(minutes)
        self.name = f"fixed({minutes:g}m)"


def sizers():
    out = [NamedFixed(m) for m in FIXED_SWEEP]
    out += [EwmaSizer(0.5, 0.05, 0.3, name="ewma(0.5/0.05@0.3)"),
            EwmaSizer(0.3, 0.02, 0.2, name="ewma(0.3/0.02@0.2)")]
    out += river_sizers()
    return out


# --- calibration (synthetic only, never the corpus) -------------------------------------------

def do_calibrate(_a):
    r = random.Random(SEED)
    n = int(SPAN_MINUTES * 60 / DETECT_STEP_S)
    streams = {
        "flat-0": [0.0] * n,
        "flat-noisy": [r.choice([0.0, 0.0, 0.0, 0.1, 0.2]) for _ in range(n)],
        "flat-high": [0.9] * n,
    }
    for at in (20, 30, 45, 50):
        streams[f"step@{at}"] = [0.0] * at + [1.0] * (n - at)
    szs = [s for s in sizers() if isinstance(s, DetectorSizer)]
    print(f"synthetic streams of {n} observations ({DETECT_STEP_S}s buckets over "
          f"{SPAN_MINUTES}m)\n")
    print(f"{'stream':12}" + "".join(f"{s.name:>26}" for s in szs))
    for label, xs in streams.items():
        print(f"{label:12}" + "".join(f"{str(s.fire_indices(xs)):>26}" for s in szs))
    print("\nA calibration is ACCEPTABLE iff every flat-* row is empty (no false fire) and at "
          "least\none step row fires. Lag = fire index - change index; a lag above "
          f"{TOLERANCE_S//60} buckets means the\ndetector structurally cannot HIT at this "
          "tolerance, which is a result, not a bug.")


# --- the run ---------------------------------------------------------------------------------

def do_truth(_a):
    st = open_store(DB)
    cs = CachingStore(st)
    sessions = [s for (s,) in st._conn().execute("SELECT DISTINCT session FROM bin "
                                                 "ORDER BY session")]
    rows, excl = [], collections.Counter()
    for s in sessions:
        gt = ground_truth(cs, s)
        cs.reset()
        n_at = max(v[0] for v in gt.values())
        trans = [t for v in gt.values() for t in v[1]]
        row = {"session": s, "bins": len(active_bins(cs, s)),
               "attributed_workspace": gt["workspace"][0], "attributed_branch": gt["branch"][0],
               "transitions_workspace": len(gt["workspace"][1]),
               "transitions_branch": len(gt["branch"][1]),
               "gaps": sorted(t.gap_min for t in trans)}
        row["qualifies"] = bool(n_at >= MIN_ATTRIBUTED and len(trans) >= MIN_TRANSITIONS)
        if not row["qualifies"]:
            excl["too_few_attributed" if n_at < MIN_ATTRIBUTED else "no_transition"] += 1
        rows.append(row)
    with open(os.path.join(OUT, "sizer-ground-truth.jsonl"), "w") as f:
        for r in rows:
            f.write(json.dumps(r) + "\n")
    q = [r for r in rows if r["qualifies"]]
    print(f"sessions {len(rows)}  qualifying {len(q)}  excluded {dict(excl)}")
    print(f"transitions: workspace {sum(r['transitions_workspace'] for r in rows)}  "
          f"branch {sum(r['transitions_branch'] for r in rows)}")
    return rows


def score(sz, cs, session, anchors, trans, span=SPAN_MINUTES):
    """HIT/FP/MISS + the detection instants for one sizer over one session's windows."""
    r = {"hit": 0, "fp": 0, "miss": 0, "fires": 0, "windows": 0, "dists": [],
         "slice_minutes": [], "hit_c": 0, "fp_c": 0, "miss_c": 0, "fires_disc": 0,
         "windows_disc": 0}
    for end in anchors:
        lo = end - span * 60.0
        here = [t for t in trans if lo <= t.instant < end]
        cont = [t for t in here if t.gap_min <= CONTIGUOUS_GAP_MIN]
        end_dt = datetime.fromtimestamp(end, tz=timezone.utc)
        p = sz.plan(cs, session, end_dt, span, None)
        det = p.detail.get("detected_at", p.slice_start.timestamp())
        r["windows"] += 1
        r["slice_minutes"].append(round((p.slice_end - p.slice_start).total_seconds() / 60.0, 2))
        if here:
            r["windows_disc"] += 1
        if det is None:
            r["miss"] += len(here)
            r["miss_c"] += len(cont)
            continue
        r["fires"] += 1
        if here:
            r["fires_disc"] += 1
        d = min((abs(det - t.instant) for t in here), default=None)
        if d is not None:
            r["dists"].append(d)
        if d is not None and d <= TOLERANCE_S:
            r["hit"] += 1
            r["miss"] += len(here) - 1
        else:
            r["fp"] += 1
            r["miss"] += len(here)
        # The same scoring, with ground truth restricted to CONTIGUOUS transitions. Symmetric
        # with the primary: a detection in a window holding no contiguous transition is a false
        # positive there, exactly as it is above.
        dc = min((abs(det - t.instant) for t in cont), default=None)
        if dc is not None and dc <= TOLERANCE_S:
            r["hit_c"] += 1
            r["miss_c"] += len(cont) - 1
        else:
            r["fp_c"] += 1
            r["miss_c"] += len(cont)
    return r


def do_run(_a):
    t0 = time.time()
    st = open_store(DB)
    cs = CachingStore(st)
    sessions = [s for (s,) in st._conn().execute("SELECT DISTINCT session FROM bin "
                                                 "ORDER BY session")]
    sample, skipped = [], collections.Counter()
    for s in sessions:
        gt = ground_truth(cs, s)
        cs.reset()
        n_at = max(v[0] for v in gt.values())
        trans = sorted([t for v in gt.values() for t in v[1]], key=lambda t: t.instant)
        if n_at < MIN_ATTRIBUTED:
            skipped["too_few_attributed_windows"] += 1
            continue
        if len(trans) < MIN_TRANSITIONS:
            skipped["no_transition"] += 1
            continue
        sample.append((s, trans))
    print(f"SAMPLE FIXED: {len(sample)} qualifying sessions, excluded {dict(skipped)}, "
          f"{sum(len(t) for _s, t in sample)} transitions", flush=True)

    szs = sizers()
    agg = {s.name: collections.Counter() for s in szs}
    dists = {s.name: [] for s in szs}
    slices = {s.name: [] for s in szs}
    per_session = collections.defaultdict(dict)
    for k, (session, trans) in enumerate(sample, 1):
        anchors = [float(t + BIN_SECONDS) for t in active_bins(cs, session)]
        for sz in szs:
            r = score(sz, cs, session, anchors, trans)
            for key in ("hit", "fp", "miss", "fires", "windows", "hit_c", "fp_c", "miss_c",
                        "fires_disc", "windows_disc"):
                agg[sz.name][key] += r[key]
            dists[sz.name] += r["dists"]
            slices[sz.name] += r["slice_minutes"]
            per_session[session][sz.name] = {kk: r[kk] for kk in ("hit", "fp", "miss", "fires",
                                                                  "windows")}
        cs.reset()
        print(f"  [{k}/{len(sample)}] {session} {len(anchors)} windows, {len(trans)} transitions "
              f"({time.time()-t0:.0f}s)", flush=True)

    def rate(a, b):
        return round(a / b, 4) if b else 0.0

    rows = []
    for sz in szs:
        c = agg[sz.name]
        rows.append({
            "sizer": sz.name,
            "windows": c["windows"], "fires": c["fires"], "fire_rate": rate(c["fires"], c["windows"]),
            "hit": c["hit"], "fp": c["fp"], "miss": c["miss"],
            "precision": rate(c["hit"], c["hit"] + c["fp"]),
            "recall": rate(c["hit"], c["hit"] + c["miss"]),
            "median_distance_min": round(statistics.median(dists[sz.name]) / 60.0, 2)
                                   if dists[sz.name] else None,
            "precision_contiguous": rate(c["hit_c"], c["hit_c"] + c["fp_c"]),
            "recall_contiguous": rate(c["hit_c"], c["hit_c"] + c["miss_c"]),
            "windows_with_transition": c["windows_disc"],
            "fire_rate_on_transition_windows": rate(c["fires_disc"], c["windows_disc"]),
            "slice_minutes_median": round(statistics.median(slices[sz.name]), 2),
        })
    out = {"sample_sessions": len(sample), "excluded": dict(skipped),
           "transitions": sum(len(t) for _s, t in sample),
           "span_minutes": SPAN_MINUTES, "tolerance_s": TOLERANCE_S,
           "detect_step_s": DETECT_STEP_S, "seed": SEED,
           "elapsed_s": round(time.time() - t0, 1), "rows": rows,
           "per_session": {s: v for s, v in per_session.items()}}
    with open(os.path.join(OUT, "sizer-eval.json"), "w") as f:
        json.dump(out, f, indent=2)
    render(out)
    return out


def render(out):
    rows = out["rows"]
    print(f"\n## n = {out['sample_sessions']} sessions, {out['transitions']} transitions, "
          f"{rows[0]['windows']} windows;  excluded {out['excluded']}\n")
    print(f"{'sizer':26}{'fire':>7}{'HIT':>6}{'FP':>6}{'MISS':>6}{'prec':>8}{'recall':>8}"
          f"{'med_d':>8}{'slice':>7}")
    for r in rows:
        print(f"{r['sizer']:26}{r['fire_rate']:7.1%}{r['hit']:6d}{r['fp']:6d}{r['miss']:6d}"
              f"{r['precision']:8.1%}{r['recall']:8.1%}"
              f"{(r['median_distance_min'] if r['median_distance_min'] is not None else -1):8.1f}"
              f"{r['slice_minutes_median']:7.1f}")
    base = next(r for r in rows if r["sizer"] == "fixed(15m)")
    print(f"\n## against the shipped baseline {base['sizer']} "
          f"(prec {base['precision']:.1%}, recall {base['recall']:.1%})\n")
    print(f"{'sizer':26}{'d-prec':>9}{'d-recall':>10}  verdict")
    for r in rows:
        if r["sizer"] == base["sizer"]:
            continue
        dp = (r["precision"] - base["precision"]) * 100
        dr = (r["recall"] - base["recall"]) * 100
        if r["fire_rate"] > 0.5 or r["fire_rate"] < 0.02:
            v = f"DISQUALIFIED (rule 3: fires {r['fire_rate']:.1%})"
        elif dp <= 0 or dr <= 0:
            v = "loses rule 1 (must beat BOTH)"
        elif max(dp, dr) < 10:
            v = "loses rule 2 (margin < 10 points)"
        else:
            v = "WINS"
        print(f"{r['sizer']:26}{dp:+9.1f}{dr:+10.1f}  {v}")
    print("\n## contiguous-transition sensitivity (gap <= "
          f"{CONTIGUOUS_GAP_MIN}m)\n")
    print(f"{'sizer':26}{'prec':>9}{'recall':>10}")
    for r in rows:
        print(f"{r['sizer']:26}{r['precision_contiguous']:9.1%}{r['recall_contiguous']:10.1%}")


def do_control(_a):
    """Two validity checks the pre-registration does not ask for and the winner needs anyway.

    1. THE GAP CENSUS. A transition whose two attributed bins are further apart than the span
       budget has its `before` side OUTSIDE anything a sizer may read, so no sizer can detect it
       and any sizer that appears to has hit it by the accident of a constant offset.
    2. THE SHUFFLE CONTROL. Ground truth is relocated to random non-empty bins of the SAME
       session, count preserved, and everything is re-scored. A sizer whose score survives this
       was scoring its own fire pattern against the shape of the corpus, not the work. This is
       the check that separates "detects the transition" from "fires often enough to land near
       one", and no aggregate table can distinguish those two.
    """
    st = open_store(DB)
    cs = CachingStore(st)
    sessions = [s for (s,) in st._conn().execute("SELECT DISTINCT session FROM bin "
                                                 "ORDER BY session")]
    sample, gaps = [], []
    for s in sessions:
        gt = ground_truth(cs, s)
        cs.reset()
        tr = sorted([t for v in gt.values() for t in v[1]], key=lambda t: t.instant)
        if max(v[0] for v in gt.values()) < MIN_ATTRIBUTED or len(tr) < MIN_TRANSITIONS:
            continue
        sample.append((s, tr))
        gaps += [t.gap_min for t in tr]
    print(f"gap between the two attributed bins, over {len(gaps)} transitions:")
    for lo, hi in ((0.0, 5.0), (5.0, CONTIGUOUS_GAP_MIN), (CONTIGUOUS_GAP_MIN, SPAN_MINUTES)):
        print(f"   ({lo:g}, {hi:g}] min: {sum(1 for g in gaps if lo < g <= hi or (lo == 0.0 and g <= 5.0))}")
    print(f"   > {SPAN_MINUTES}m (BEYOND THE BUDGET, undetectable by construction): "
          f"{sum(1 for g in gaps if g > SPAN_MINUTES)}")

    rng = random.Random(SEED)
    szs = sizers()
    print(f"\n{'ground truth':10} {'sizer':26}{'prec':>8}{'recall':>8}{'fire':>8}")
    for label, shuffle in (("real", False), ("shuffled", True)):
        agg = {s.name: collections.Counter() for s in szs}
        for s, tr in sample:
            bins = active_bins(cs, s)
            if shuffle:
                picks = rng.sample(bins, min(len(tr), len(bins)))
                tr = [t._replace(instant=float(p)) for t, p in zip(tr, picks)]
            anchors = [float(b + BIN_SECONDS) for b in bins]
            for sz in szs:
                r = score(sz, cs, s, anchors, tr)
                for k in ("hit", "fp", "miss", "fires", "windows"):
                    agg[sz.name][k] += r[k]
            cs.reset()
        for sz in szs:
            c = agg[sz.name]
            p = c["hit"] / (c["hit"] + c["fp"]) if c["hit"] + c["fp"] else 0.0
            rc = c["hit"] / (c["hit"] + c["miss"]) if c["hit"] + c["miss"] else 0.0
            print(f"{label:10} {sz.name:26}{p:8.1%}{rc:8.1%}{c['fires']/c['windows']:8.1%}")


# --- rule 3 PER SESSION: the one open concern from SIZER-RESULTS.md ---------------------------

class RefractoryEwma(EwmaSizer):
    """The winner with a minimum spacing between accepted rising edges.

    Included to be MEASURED, not because it can work: the fire RATE is the share of windows in
    which the sizer fired at all, and the first rising edge of a window is never the one a
    refractory period suppresses. So this can only move WHICH instant is reported (earlier),
    never whether the window fired. The table proves that rather than asserting it."""

    def __init__(self, buckets, fast=0.3, slow=0.02, threshold=0.2):
        super().__init__(fast, slow, threshold)
        self.buckets = buckets
        self.name = f"ewma+refract({buckets})"

    def fire_indices(self, xs):
        out = []
        for i in super().fire_indices(xs):
            if not out or i - out[-1] >= self.buckets:
                out.append(i)
        return out


class FireCappedEwma(EwmaSizer):
    """The only rate guard a `Sizer` can actually implement: it is confined to the span budget,
    so it cannot know the SESSION's fire rate — only its own window's. A window whose stream
    holds more than `max_fires` rising edges is called too churny to localise and the sizer
    reduces to the fixed fallback."""

    def __init__(self, max_fires, fast=0.3, slow=0.02, threshold=0.2):
        super().__init__(fast, slow, threshold)
        self.max_fires = max_fires
        self.name = f"ewma+cap({max_fires})"

    def fire_indices(self, xs):
        idx = super().fire_indices(xs)
        return [] if len(idx) > self.max_fires else idx


def do_guards(_a):
    """Is a 50%-of-windows ceiling something a sizer can honour PER SESSION, and does any guard
    that enforces it keep the win?

    Two measurements, both over the same corpus and the same scoring as `run`:

    1. THE OPPORTUNITY RATE. The share of a session's windows that actually CONTAIN a transition
       inside the span budget. It is the ceiling on any detector's fire rate that is not a false
       positive — windows overlap twelvefold (60-minute span, one anchor per 5-minute bin), so a
       single transition is visible in up to 12 consecutive windows and a churny session's
       opportunity rate is arithmetically forced above 50%. `excess` = fire rate - opportunity
       rate is therefore the honest reading of "fires more often than the work changes".
    2. THE GUARDS, scored end to end against the unguarded winner.
    """
    st = open_store(DB)
    cs = CachingStore(st)
    sessions = [s for (s,) in st._conn().execute("SELECT DISTINCT session FROM bin "
                                                 "ORDER BY session")]
    sample = []
    for s in sessions:
        gt = ground_truth(cs, s)
        cs.reset()
        tr = sorted([t for v in gt.values() for t in v[1]], key=lambda t: t.instant)
        if max(v[0] for v in gt.values()) < MIN_ATTRIBUTED or len(tr) < MIN_TRANSITIONS:
            continue
        sample.append((s, tr))

    win = EwmaSizer(0.3, 0.02, 0.2, name="ewma(0.3/0.02@0.2)")
    szs = [win, NamedFixed(15)] + [RefractoryEwma(b) for b in (3, 5, 10)] \
        + [FireCappedEwma(k) for k in (1, 2, 3)]
    agg = {s.name: collections.Counter() for s in szs}
    per = {}
    for s, tr in sample:
        anchors = [float(b + BIN_SECONDS) for b in active_bins(cs, s)]
        for sz in szs:
            r = score(sz, cs, s, anchors, tr)
            for k in ("hit", "fp", "miss", "fires", "windows", "windows_disc"):
                agg[sz.name][k] += r[k]
            if sz is win:
                per[s] = r
        cs.reset()

    def rate(a, b):
        return a / b if b else 0.0

    print(f"## 1. fire rate vs OPPORTUNITY rate, per session, {win.name}\n")
    print(f"{'session':10}{'wins':>7}{'oppty':>8}{'fire':>8}{'excess':>9}{'prec':>8}"
          f"{'recall':>8}")
    rows = sorted(per.items(), key=lambda kv: -rate(kv[1]['fires'], kv[1]['windows']))
    over, over_excess = 0, 0
    for s, r in rows:
        f = rate(r["fires"], r["windows"])
        o = rate(r["windows_disc"], r["windows"])
        p = rate(r["hit"], r["hit"] + r["fp"])
        rc = rate(r["hit"], r["hit"] + r["miss"])
        over += f > 0.5
        over_excess += (f - o) > 0.0
        print(f"{s:10}{r['windows']:7d}{o:8.1%}{f:8.1%}{f-o:+9.1%}{p:8.1%}{rc:8.1%}")
    print(f"\nsessions over the 50% fire ceiling: {over}/{len(rows)};  "
          f"sessions firing MORE OFTEN THAN THE WORK CHANGES (excess > 0): "
          f"{over_excess}/{len(rows)}")
    hi = [r for _s, r in rows if rate(r["fires"], r["windows"]) > 0.5]
    lo = [r for _s, r in rows if rate(r["fires"], r["windows"]) <= 0.5]
    for label, grp in (("over-ceiling", hi), ("under-ceiling", lo)):
        h = sum(r["hit"] for r in grp)
        print(f"   {label:14} n={len(grp):2d}  pooled precision "
              f"{rate(h, h + sum(r['fp'] for r in grp)):6.1%}  recall "
              f"{rate(h, h + sum(r['miss'] for r in grp)):6.1%}")

    print(f"\n## 2. guards, scored over the same {len(sample)} sessions\n")
    print(f"{'sizer':22}{'fire':>8}{'HIT':>6}{'FP':>5}{'MISS':>6}{'prec':>8}{'recall':>8}"
          f"{'d-prec':>9}{'d-recall':>10}")
    base = None
    for sz in szs:
        c = agg[sz.name]
        p = rate(c["hit"], c["hit"] + c["fp"])
        rc = rate(c["hit"], c["hit"] + c["miss"])
        if base is None:
            base = (p, rc)
        print(f"{sz.name:22}{rate(c['fires'], c['windows']):8.1%}{c['hit']:6d}{c['fp']:5d}"
              f"{c['miss']:6d}{p:8.1%}{rc:8.1%}{(p-base[0])*100:+9.1f}"
              f"{(rc-base[1])*100:+10.1f}")
    print("\nd- columns are against the unguarded winner (row 1). A guard is worth having only "
          "if it\nbuys a lower fire rate without giving back the margin the winner was chosen "
          "for.")

    # 3. Does any guard bring the OVER-CEILING sessions under the ceiling? That, not the corpus
    #    aggregate, is the thing rule 3 was read per-session to require.
    hi_s = [s for s, r in rows if rate(r["fires"], r["windows"]) > 0.5]
    print(f"\n## 3. per-session fire rate on the {len(hi_s)} over-ceiling sessions\n")
    cand = [win] + [g for g in szs if isinstance(g, FireCappedEwma)]
    print(f"{'session':10}{'oppty':>8}" + "".join(f"{g.name:>16}" for g in cand))
    still = collections.Counter()
    for s in hi_s:
        tr = dict(sample)[s]
        anchors = [float(b + BIN_SECONDS) for b in active_bins(cs, s)]
        cells = []
        for g in cand:
            r = score(g, cs, s, anchors, tr)
            f = rate(r["fires"], r["windows"])
            still[g.name] += f > 0.5
            cells.append(f"{f:>15.1%} ")
        cs.reset()
        print(f"{s:10}{rate(per[s]['windows_disc'], per[s]['windows']):8.1%}" + "".join(cells))
    print("\nstill over the ceiling: " + ", ".join(f"{k} {v}/{len(hi_s)}"
                                                   for k, v in still.items()))


# --- TASK 4: does each dynamic CARRY INFORMATION over the corpus? -----------------------------

# A metric is UNINFORMATIVE if almost every window reports the same value for it. Stated as a
# number before looking at one: `band` is the largest share of a dimension's `compared` readings
# that fall inside any single 0.05-wide bucket, and a metric at or above UNINFORMATIVE_BAND is
# disqualified regardless of how principled its definition is — a reader who could have guessed
# the value without reading it learned nothing. 0.05 is the resolution the payload rounds to a
# reader's eye (`round(x, 3)` is finer than anyone acts on) and 0.90 is the same "distinguishable
# from a constant" idea `MIN_EVIDENCE` applies to a share.
UNINFORMATIVE_BAND = 0.90
# The second disqualifier, and it is independent: a field that is `None` on most windows is a
# field a consumer must branch on more often than it can use. A dimension `compared` on fewer
# than this share of windows cannot carry a routine published metric.
MIN_COMPARED_RATE = 0.10
BAND_W = 0.05
# The MIRROR of the constancy failure, and the one the 0.05-band test cannot see. Read as the
# yes/no a reader actually acts on ("did anything new appear?"), a turnover whose non-zero rate is
# ~1.0 is as constant as one whose value never moves: the answer is always yes. A dimension
# outside [1 - ALWAYS_RATE, ALWAYS_RATE] on that reading is disqualified.
ALWAYS_RATE = 0.90
# `MATERIAL` and the `reading` vocabulary were PROPOSED here and are now SHIPPED
# (`app.analysis.dynamics`), so they are imported rather than kept as a second copy: a divergence
# between the measurement and the implementation is the failure this whole script exists to
# prevent. The threshold is derived, not chosen — see MATERIAL's comment in that module.

# The inventory levels, measured with the IDENTICAL arithmetic as allocation (`dynamics._status`
# and `dynamics._absent_mass`, imported rather than reimplemented) so the comparison that decides
# whether they ship is between populations, not between two spellings of turnover. They have no
# share floor — that is what makes them inventory — so only turnover/decay/status are defined for
# them; there is no dominant value, hence no concentration shift and no `changed`.
INV_DIMENSIONS = tuple(INVENTORY)


def _top_absent(items, other, _total):
    """The values in `items` absent from `other`, ranked by the mass they carry.

    PRODUCTION DROPPED THIS (`dynamics._absent_mass` returns the mass alone now) precisely because
    of what it measured — the top entering value is either `slice.value` itself or a value below
    the 0.50 dominance floor. It lives here so that finding stays re-derivable rather than becoming
    a number in a document with no code behind it. Tie-break is alphabetical, matching
    `window.rollup`'s order, as the dropped implementation did.
    """
    new = [(ref, n) for ref, n in items if ref not in other]
    return sorted(new, key=lambda kv: (-kv[1], kv[0]))


def _q(xs, p):
    if not xs:
        return None
    ys = sorted(xs)
    i = min(len(ys) - 1, max(0, int(round(p * (len(ys) - 1)))))
    return round(ys[i], 3)


def _band(xs, lo, w=BAND_W):
    """The largest share of `xs` inside any single `w`-wide bucket, and that bucket's left edge."""
    if not xs:
        return 0.0, None
    c = collections.Counter(int((x - lo) // w) for x in xs)
    k, n = max(c.items(), key=lambda kv: (kv[1], -kv[0]))
    return round(n / len(xs), 4), round(lo + k * w, 3)


def _summary(xs, lo):
    band, at = _band(xs, lo)
    return {"n": len(xs), "mean": round(statistics.fmean(xs), 3) if xs else None,
            "sd": round(statistics.pstdev(xs), 3) if len(xs) > 1 else 0.0,
            "min": _q(xs, 0.0), "p10": _q(xs, 0.10), "p25": _q(xs, 0.25), "med": _q(xs, 0.50),
            "p75": _q(xs, 0.75), "p90": _q(xs, 0.90), "max": _q(xs, 1.0),
            "zero": round(sum(1 for x in xs if abs(x) < 1e-9) / len(xs), 4) if xs else None,
            "band": band, "band_at": at,
            "distinct_2dp": len({round(x, 2) for x in xs})}


def _pearson(xs, ys):
    if len(xs) < 2:
        return None
    mx, my = statistics.fmean(xs), statistics.fmean(ys)
    num = sum((a - mx) * (b - my) for a, b in zip(xs, ys))
    dx = sum((a - mx) ** 2 for a in xs) ** 0.5
    dy = sum((b - my) ** 2 for b in ys) ** 0.5
    return round(num / (dx * dy), 3) if dx and dy else None


def do_dist(_a):
    """The distribution of every dynamic over EVERY window /analyze could answer.

    POPULATION — deliberately wider than `run`'s. `run` scored only the 25 sessions carrying
    ground truth, because a detector cannot be scored where nothing changes. This asks a
    different question — what does a READER receive — so it walks all 51 sessions and every
    non-empty bin as an anchor (2,702 windows), sized by the SHIPPED `DEFAULT_SIZER` with the
    SHIPPED serving floor, i.e. exactly `main.py`'s call. A field's information content has to be
    measured on the windows it is actually emitted on, including the quiet ones, or the answer is
    conditioned on the very churn the field is supposed to reveal.
    """
    t0 = time.time()
    st = open_store(DB)
    cs = CachingStore(st)
    floor = st.serving_floor()
    sessions = [s for (s,) in st._conn().execute("SELECT DISTINCT session FROM bin "
                                                 "ORDER BY session")]
    names = [n for n, _l, _f in ALLOCATION]
    inv = [n for n, _l, _c in INV_DIMENSIONS]
    level_of = {n: l for n, l, _f in ALLOCATION}
    level_of.update({n: l for n, l, _c in INV_DIMENSIONS})
    acc = {n: {"status": collections.Counter(), "changed": collections.Counter(),
               "reading": collections.Counter(), "turnover": [], "decay": [], "shift": [],
               "em_n": [], "dc_n": [], "pair": [],
               # TURNOVER SPLIT BY WHETHER THE WORK REALLY CHANGED. This is the discriminator
               # for the inventory question: a dimension measuring a CHANGE OF WORK must read
               # higher inside a window that contains a ground-truth transition than inside one
               # that does not. A dimension measuring tool-surface BREADTH reads the same either
               # way, because a wide surface is sampled incompletely in every window.
               "to_trans": [], "to_flat": [],
               # TURNOVER vs THE SIZE OF THE SURFACE, and this one needs no ground truth at all
               # — which matters, because the only ground truth this corpus has is `branch`
               # transitions and a dimension orthogonal to branch would be under-credited by the
               # lift split above. If turnover is BREADTH, it is the chance that a value in the
               # slice simply was not sampled in the baseline, which grows with the number of
               # distinct values on the level; if it is CHANGE, it does not care how wide the
               # surface is. So: Pearson r against the baseline's distinct-value count.
               "surface": [],
               # DO THE `emerged`/`decayed` TOP-LISTS CARRY A FACT THE REST OF THE BLOCK DOES
               # NOT? Two numbers decide it: how often the list is non-empty at all, and — when
               # it is — whether the top entering value clears the 0.50 dominance floor. A value
               # below that floor is one this package's own `window.dominant` refuses to name as
               # what the window was about, so highlighting it under `emerged` would re-introduce
               # exactly what the floor exists to prevent, one field over.
               "em_top": [], "em_is_dominant": [],
               "named": collections.defaultdict(list)}
           for n in names + inv}
    sizer_detail = {"fires": [], "fallback": 0, "slice_minutes": [], "windows": 0}
    per_session = collections.defaultdict(lambda: collections.defaultdict(collections.Counter))
    for k, s in enumerate(sessions, 1):
        anchors = [float(t + BIN_SECONDS) for t in active_bins(cs, s)]
        # Ground truth for the lift split, from the SAME function `run` scored against.
        gt = ground_truth(cs, s)
        trans = sorted(t.instant for v in gt.values() for t in v[1])
        for end in anchors:
            has_t = any(end - SPAN_MINUTES * 60.0 <= i < end for i in trans)
            end_dt = datetime.fromtimestamp(end, tz=timezone.utc)
            # `dynamics_for` inlined by ONE line — the plan is taken out so the same two rollups
            # can also be read at the inventory levels, which `compare` does not iterate. Same
            # sizer, same floor, same `dynamics()` call: this is `main.py`'s path with the two
            # intermediate rollups kept, not a reimplementation of it.
            pl = DEFAULT_SIZER.plan(cs, s, end_dt, SPAN_MINUTES, floor)
            blk = dynamics(cs, s, pl.slice_start, pl.slice_end, pl.baseline_start,
                           sizer=pl.sizer, detail=pl.detail)
            s_lo, s_hi = quantize(pl.slice_start.timestamp()), quantize(pl.slice_end.timestamp())
            b_lo = quantize(pl.baseline_start.timestamp())
            slice_rl = cs.rollup_window(s, s_lo, s_hi)
            baseline_rl = cs.rollup_window(s, b_lo, s_lo)
            # The published block reports only the SURVIVING dimensions. The measurement has to
            # cover the dropped ones too or the drop stops being re-derivable, so the full
            # allocation set goes through the same shipped `compare`, explicitly.
            dims = compare(slice_rl, baseline_rl, dimensions=tuple(ALLOCATION))
            d = blk["sizer_detail"]
            sizer_detail["windows"] += 1
            sizer_detail["fires"].append(d.get("fires", 0))
            sizer_detail["fallback"] += bool(d.get("fallback"))
            sizer_detail["slice_minutes"].append(blk["slice_minutes"])
            for n in names:
                v = dims[n]
                a = acc[n]
                a["status"][v["status"]] += 1
                a["changed"][str(v["changed"])] += 1
                per_session[s][n][v["status"]] += 1
                if v["status"] != "compared":
                    continue
                a["turnover"].append(v["turnover"])
                a["decay"].append(v["decay"])
                a["pair"].append((v["turnover"], v["decay"]))
                (a["to_trans"] if has_t else a["to_flat"]).append(v["turnover"])
                a["surface"].append((float(len(baseline_rl.get(level_of[n]) or [])),
                                     v["turnover"]))
                lv = level_of[n]
                em = _top_absent(slice_rl.get(lv) or [],
                                 dict(baseline_rl.get(lv) or []), None)
                dc = _top_absent(baseline_rl.get(lv) or [],
                                 dict(slice_rl.get(lv) or []), None)
                a["em_n"].append(len(em))
                a["dc_n"].append(len(dc))
                if em:
                    s_tot_a = sum(x for _r, x in slice_rl.get(lv) or [])
                    a["em_top"].append(em[0][1] / s_tot_a if s_tot_a else 0.0)
                    a["em_is_dominant"].append(em[0][0] == v["slice"]["value"])
                a["reading"][str(v["reading"])] += 1
                if v["concentration_shift"] is not None:
                    a["shift"].append(v["concentration_shift"])
                # NAMED IDENTIFIERS, not just a table: the actual (session, window) behind every
                # extreme and every non-zero reading, so a claim in the report can be re-derived
                # by hand. Roughly twenty defects in this study surfaced as plausible wrong
                # numbers and none was caught by reading an aggregate.
                a["named"]["turnover"].append((v["turnover"], s, end))
                if v["concentration_shift"] is not None:
                    a["named"]["shift"].append((v["concentration_shift"], s, end))
                if v["changed"]:
                    a["named"]["changed"].append((1.0, s, end))
            # The inventory dimensions, same arithmetic, no floor.
            for n, level, _cap in INV_DIMENSIONS:
                a = acc[n]
                s_items = slice_rl.get(level) or []
                b_items = baseline_rl.get(level) or []
                s_tot = sum(x for _r, x in s_items)
                b_tot = sum(x for _r, x in b_items)
                status = _status(s_tot, b_tot, MIN_EVIDENCE)
                a["status"][status] += 1
                per_session[s][n][status] += 1
                if status != "compared":
                    continue
                to = _absent_mass(s_items, dict(b_items), s_tot)
                de = _absent_mass(b_items, dict(s_items), b_tot)
                a["turnover"].append(to)
                a["decay"].append(de)
                a["pair"].append((to, de))
                (a["to_trans"] if has_t else a["to_flat"]).append(to)
                a["surface"].append((float(len(b_items)), to))
                a["em_n"].append(len(_top_absent(s_items, dict(b_items), s_tot)))
                a["dc_n"].append(len(_top_absent(b_items, dict(s_items), b_tot)))
                a["named"]["turnover"].append((to, s, end))
        cs.reset()
        print(f"  [{k}/{len(sessions)}] {s} {len(anchors)} windows ({time.time()-t0:.0f}s)",
              flush=True)

    rows = []
    for n in names + inv:
        a = acc[n]
        tot = sum(a["status"].values())
        cmp_n = a["status"]["compared"]
        row = {"dimension": n, "kind": "allocation" if n in names else "inventory",
               "windows": tot, "compared": cmp_n,
               "compared_rate": round(cmp_n / tot, 4) if tot else 0.0,
               "status": dict(a["status"]), "changed": dict(a["changed"]),
               "turnover": _summary(a["turnover"], 0.0),
               "decay": _summary(a["decay"], 0.0),
               "concentration_shift": _summary(a["shift"], -1.0),
               "emerged_n_med": _q([float(x) for x in a["em_n"]], 0.5),
               "decayed_n_med": _q([float(x) for x in a["dc_n"]], 0.5),
               "reading": dict(a["reading"]),
               "emerged_nonempty": round(len(a["em_top"]) / a["status"]["compared"], 4)
                                   if a["status"]["compared"] else None,
               "emerged_top_below_floor": round(
                   sum(1 for x in a["em_top"] if x < 0.5) / len(a["em_top"]), 4)
                   if a["em_top"] else None,
               "emerged_top_is_slice_dominant": round(
                   sum(a["em_is_dominant"]) / len(a["em_is_dominant"]), 4)
                   if a["em_is_dominant"] else None,
               "surface_r": _pearson([x for x, _y in a["surface"]],
                                     [y for _x, y in a["surface"]]),
               "surface_med": _q([x for x, _y in a["surface"]], 0.5),
               "turnover_decay_r": _pearson([x for x, _y in a["pair"]],
                                            [y for _x, y in a["pair"]]),
               "both_zero": round(sum(1 for x, y in a["pair"]
                                      if x < 1e-9 and y < 1e-9) / len(a["pair"]), 4)
                            if a["pair"] else None,
               "one_zero": round(sum(1 for x, y in a["pair"]
                                     if (x < 1e-9) != (y < 1e-9)) / len(a["pair"]), 4)
                           if a["pair"] else None,
               "lift": {"n_trans": len(a["to_trans"]), "n_flat": len(a["to_flat"]),
                        "mean_trans": round(statistics.fmean(a["to_trans"]), 3)
                                      if a["to_trans"] else None,
                        "mean_flat": round(statistics.fmean(a["to_flat"]), 3)
                                     if a["to_flat"] else None,
                        "nonzero_trans": round(sum(1 for x in a["to_trans"] if x > 1e-9)
                                               / len(a["to_trans"]), 4) if a["to_trans"] else None,
                        "nonzero_flat": round(sum(1 for x in a["to_flat"] if x > 1e-9)
                                              / len(a["to_flat"]), 4) if a["to_flat"] else None}}
        for metric in ("turnover", "decay", "concentration_shift"):
            key = "shift" if metric == "concentration_shift" else metric
            xs = a["named"].get(key, [])
            row[metric]["extremes"] = [
                {"value": v, "session": ss, "window_end":
                 datetime.fromtimestamp(e, tz=timezone.utc).isoformat()}
                for v, ss, e in (sorted(xs)[:1] + sorted(xs)[-2:] if xs else [])]
        rows.append(row)
    out = {"sessions": len(sessions), "windows": sizer_detail["windows"],
           "span_minutes": SPAN_MINUTES, "sizer": DEFAULT_SIZER.name,
           "serving_floor": floor, "uninformative_band": UNINFORMATIVE_BAND,
           "min_compared_rate": MIN_COMPARED_RATE, "band_w": BAND_W,
           "always_rate": ALWAYS_RATE, "material": MATERIAL,
           "fallback_rate": round(sizer_detail["fallback"] / sizer_detail["windows"], 4),
           "slice_minutes_med": _q(sizer_detail["slice_minutes"], 0.5),
           "elapsed_s": round(time.time() - t0, 1), "rows": rows,
           "per_session": {s: {n: dict(c) for n, c in v.items()} for s, v in per_session.items()}}
    with open(os.path.join(OUT, "dynamics-dist.json"), "w") as f:
        json.dump(out, f, indent=2)
    render_dist(out)
    return out


def render_dist(out):
    print(f"\n## n = {out['sessions']} sessions, {out['windows']} windows, sizer "
          f"{out['sizer']} (fallback on {out['fallback_rate']:.1%}, median slice "
          f"{out['slice_minutes_med']}m), floor {out['serving_floor']}\n")
    print(f"{'dimension':16}{'kind':11}{'compared':>9}" + "".join(
        f"{s:>10}" for s in ("absent", "thin")))
    for r in out["rows"]:
        st = r["status"]
        ab = sum(v for k, v in st.items() if k.endswith("absent"))
        th = sum(v for k, v in st.items() if k.endswith("thin"))
        print(f"{r['dimension']:16}{r['kind']:11}{r['compared_rate']:9.1%}"
              f"{ab/r['windows']:10.1%}{th/r['windows']:10.1%}")
    for metric, lo in (("turnover", 0.0), ("decay", 0.0), ("concentration_shift", -1.0)):
        print(f"\n## {metric}  (over `compared` windows only)\n")
        print(f"{'dimension':16}{'n':>6}{'mean':>7}{'sd':>7}{'p10':>7}{'med':>7}{'p90':>7}"
              f"{'zero':>8}{'band':>8}{'@':>7}{'d2dp':>6}  verdict")
        for r in out["rows"]:
            m = r[metric]
            if not m["n"]:
                print(f"{r['dimension']:16}{0:>6}   -- never reported")
                continue
            nz = 1.0 - m["zero"] if metric != "concentration_shift" else None
            bad = []
            if m["band"] >= out["uninformative_band"]:
                bad.append(f"CONSTANT ({m['band']:.0%} in one {out['band_w']} band)")
            if nz is not None and nz >= out["always_rate"]:
                bad.append(f"ALWAYS-YES (non-zero on {nz:.0%})")
            if r["compared_rate"] < out["min_compared_rate"]:
                bad.append(f"RARE (compared {r['compared_rate']:.1%})")
            print(f"{r['dimension']:16}{m['n']:6d}{m['mean']:7.3f}{m['sd']:7.3f}"
                  f"{m['p10']:7.3f}{m['med']:7.3f}{m['p90']:7.3f}{m['zero']:8.1%}"
                  f"{m['band']:8.1%}{m['band_at']:7.2f}{m['distinct_2dp']:6d}  "
                  + ("; ".join(bad) if bad else "informative"))
    print("\n## changed (allocation only)\n")
    print(f"{'dimension':16}{'True':>9}{'False':>9}{'None':>9}")
    for r in out["rows"]:
        if r["kind"] != "allocation":
            continue
        c, tot = r["changed"], r["windows"]
        print(f"{r['dimension']:16}{c.get('True', 0)/tot:9.1%}{c.get('False', 0)/tot:9.1%}"
              f"{c.get('None', 0)/tot:9.1%}")
    print("\n## TURNOVER LIFT: does it read higher where the work really changed?\n")
    print("A CHANGE-OF-WORK metric lifts inside a window holding a ground-truth transition.\n"
          "A BREADTH metric reads the same either way — the surface is sampled incompletely\n"
          "in every window, transition or not. This is the inventory question, measured.\n")
    print(f"{'dimension':16}{'kind':11}{'n_tr':>6}{'n_flat':>7}{'mean|tr':>9}{'mean|flat':>10}"
          f"{'lift':>8}{'nz|tr':>8}{'nz|flat':>9}")
    for r in out["rows"]:
        L = r["lift"]
        if L["mean_trans"] is None or L["mean_flat"] is None:
            continue
        print(f"{r['dimension']:16}{r['kind']:11}{L['n_trans']:6d}{L['n_flat']:7d}"
              f"{L['mean_trans']:9.3f}{L['mean_flat']:10.3f}"
              f"{L['mean_trans']-L['mean_flat']:+8.3f}{L['nonzero_trans']:8.1%}"
              f"{L['nonzero_flat']:9.1%}")
    print("\n## BREADTH TEST (no ground truth needed): does turnover track SURFACE SIZE?\n")
    print("If turnover is the chance a slice value went unsampled in the baseline, it rises with\n"
          "the number of distinct values on the level. A change-of-work metric does not.\n")
    print(f"{'dimension':16}{'kind':11}{'med distinct':>13}{'pearson r':>11}")
    for r in out["rows"]:
        if r["surface_r"] is None:
            continue
        print(f"{r['dimension']:16}{r['kind']:11}{r['surface_med']:13.1f}{r['surface_r']:11.3f}")
    print("\n## is `decay` a second fact, or a restatement of `turnover`?\n")
    print(f"{'dimension':16}{'pearson r':>10}{'both zero':>11}{'exactly one zero':>18}")
    for r in out["rows"]:
        if r["turnover_decay_r"] is None:
            continue
        print(f"{r['dimension']:16}{r['turnover_decay_r']:10.3f}{r['both_zero']:11.1%}"
              f"{r['one_zero']:18.1%}")
    print(f"\n## the PROPOSED stated `reading` (material move = 1/MIN_EVIDENCE = "
          f"{out['material']:.2f})\n")
    order = READINGS
    print(f"{'dimension':16}" + "".join(f"{k[:9]:>11}" for k in order))
    for r in out["rows"]:
        if r["kind"] != "allocation":
            continue
        tot = sum(v for k, v in r["reading"].items() if k != "None") or 1
        print(f"{r['dimension']:16}" + "".join(
            f"{r['reading'].get(k, 0)/tot:11.1%}" for k in order))
    print("\n## do the `emerged` / `decayed` TOP-LISTS carry a fact the block lacks?\n")
    print(f"{'dimension':16}{'non-empty':>11}{'em_n med':>10}{'dc_n med':>10}"
          f"{'top < 0.50 floor':>18}{'top == dominant':>17}")
    for r in out["rows"]:
        if r["kind"] != "allocation" or not r["compared"]:
            continue
        bf = r["emerged_top_below_floor"]
        dm = r["emerged_top_is_slice_dominant"]
        print(f"{r['dimension']:16}{r['emerged_nonempty']:11.1%}"
              f"{str(r['emerged_n_med']):>10}{str(r['decayed_n_med']):>10}"
              f"{(f'{bf:.1%}' if bf is not None else '--'):>18}"
              f"{(f'{dm:.1%}' if dm is not None else '--'):>17}")
    print("\n## the windows behind the extremes (re-derivable by hand)\n")
    for r in out["rows"]:
        for metric in ("turnover", "concentration_shift"):
            for e in r[metric].get("extremes", []):
                print(f"{r['dimension']:16}{metric:20}{e['value']:+7.3f}  {e['session']}  "
                      f"{e['window_end']}")


def do_render(a):
    render(json.load(open(os.path.join(OUT, "sizer-eval.json"))))


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("cmd", choices=("calibrate", "truth", "run", "control", "guards",
                                    "dist", "render"))
    args = ap.parse_args()
    {"calibrate": do_calibrate, "truth": do_truth, "run": do_run,
     "control": do_control, "guards": do_guards, "dist": do_dist,
     "render": do_render}[args.cmd](args)
