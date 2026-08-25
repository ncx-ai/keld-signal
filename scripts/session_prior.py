#!/usr/bin/env python3
"""Measure the SESSION PRIOR (docs/superpowers/specs/2026-08-24-session-prior-design.md)
against the four bars that spec fixes, on the 1,022-window frozen-corpus frame.

    PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/session_prior.py frame
    PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/session_prior.py measure

MEASUREMENT ONLY. Nothing here ships: no facet, no vocabulary, no SchemaVersion bump. The spec
describes what WOULD ship; whether it does is the reader's decision, and bar 1 can refuse it.

## What a session prior is, and the one place this study departs from the spec

Per allocation dimension, over the session, the same three fields a window already carries —
`value` / `share` / `status`, under the SAME 0.50 share floor and `window.MIN_EVIDENCE`, with
`window.attribution`'s own REASONS. Then per window the contrast: `agrees`, `departure`
(window share − the session's share of the WINDOW's value), `novel` (the window's value is
absent from the session prior entirely).

The spec recommends option A, "prior over the session so far (causal)". Read literally — the
daemon recomputes the prior per tick over everything ingested, and the window it is
characterising has by then been ingested too — the window's own evidence is INSIDE its own
prior. That reading is measured here as `sofar`, and it is degenerate in a way the spec does
not anticipate:

  * `novel` CANNOT FIRE. The window's dominant value is in the prior by construction, because
    the window's events are part of the session. Bar 2 — "the signal's real product" — is
    structurally zero, not empirically low.
  * a session's FIRST window IS its own prior. Every field agrees, departure is 0, and the
    contrast is vacuous rather than informative.

So this study measures both, and takes as PRIMARY the strict reading `before`: the prior over
`[session_start, window_start)` — the session as it stood BEFORE this window. That is still
causal (it is a subset of what the daemon knew), it is the only reading under which all three
contrast measures are non-degenerate, and it is what "a frame of reference the window is read
AGAINST" has to mean if the frame is not to contain the thing being framed. `sofar` is
reported in full beside it so the cost of the literal reading is visible rather than asserted.

`before` empties the first window of every session (`absent` prior), and that is the honest
answer: at a session's first window there is no session yet. It is reported, never filled.

## Recompute, never accumulate

Every prior here is a fresh `events_for_turns` + `reconcile` + `rollup` over the causal prefix,
per window. Nothing is carried forward from the previous window's rollup. That is the spec's
own rule, and reconcile's `pending` is the reason it is not negotiable: chunked incrementally
it differed from the whole-batch answer by up to 4,179 rows on a single file.

## The frame

The frozen corpus's 500 transcripts, span 60 / stride 50 minutes, cut INSIDE the per-file loop,
identity `(file, start)` with a per-file `fid` in the wid — the same frame as the act/artifact
study, 1,022 windows, ASSERTED. `levels.display_session` is `basename(path)[:8]` and collides
for 445 of the 500 files; a study keyed on it reported 550 windows where the truth was 1,022 and
raised nothing. The collision is checked LIVE, so the assertion is known to be testing something.

## Privacy

Reads real transcripts; writes reference VALUES (project/branch/language names and the like,
already what the payload publishes) and counts. No prompt text, no message text, no `term`
level. Durable output lives outside the repo.
"""
import argparse
import collections
import glob
import json
import math
import os
import statistics
import sys
import time
from datetime import datetime, timedelta

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
                                "sidecar"))

from app.analysis import COMPONENT_DEPTH                       # noqa: E402
from app.analysis.levels import events_for_turns               # noqa: E402
from app.analysis.reconcile import reconcile                   # noqa: E402
from app.analysis.transcript import iter_turns                 # noqa: E402
from app.analysis.window import MIN_EVIDENCE, REASONS, attribution, rollup   # noqa: E402
from app.analysis.workspace import scan_workspace              # noqa: E402
from app.analysis.workstreams import ALLOCATION                # noqa: E402

SPAN, STRIDE = 60, 50                  # minutes; the frame this series has used throughout
EXPECTED_WINDOWS = 1022
OUT = os.path.expanduser("~/keld/refseries-context/session-prior")
ROOTS = [os.path.expanduser("~/keld/refseries-context/frozen-corpus/projects"),
         os.path.expanduser("~/keld/refseries-context/frozen-corpus/john-projects")]

DIMS = [d for d, _lv, _f in ALLOCATION]
LEVEL_OF = {d: lv for d, lv, _f in ALLOCATION}
FLOOR_OF = {d: f for d, _lv, f in ALLOCATION}

# The two readings of "the session so far". `before` is primary — see the module docstring.
VARIANTS = ("before", "sofar")
PRIMARY = "before"

# Dimensions with structural room to differ from their session. `project` and `branch` are
# reported like every other dimension and are EXPECTED to agree ~always: a transcript is scoped
# to one project on one branch, and the `workspace` level has zero transitions across the whole
# corpus. A pooled agreement number would average those two into the five that can move and
# describe neither, so no pooled number is reported anywhere below.
CAN_DIFFER = ("model", "output_type", "language", "skill", "tooling")

# --------------------------------------------------------------------------- THE FOUR BARS
# Fixed here, in code, before any result was looked at. Each is justified by what would make the
# published field worth its place in the payload, NOT by what the corpus turns out to say.
#
# BAR 1 AGREEMENT. If a window's dominant value nearly always equals its prior's, the contrast
# carries no information and the design should not ship. A reader sees on the order of ten
# windows in a working day. At 0.95 agreement a departure surfaces once per twenty windows —
# once every two days — which is indistinguishable from a field that never fires, and this
# series has already dropped three dynamics and four of six transcript signals on exactly that
# reasoning. At 0.90 or below roughly one window a day carries a contrast, which is the point at
# which a reader would learn to look at the field. So: at least one of CAN_DIFFER must come in
# at or below 0.90 agreement, measured only where BOTH the window and its prior are attributed
# (where either is not, `agrees` is not defined and inventing a value for it is the fallback
# this design exists to refuse).
BAR1_AGREEMENT_MAX = 0.90
# BAR 2 NOVELTY. `novel` is the yield a per-window view structurally cannot produce. Below one
# window in twenty it is a curiosity rather than a product; same day-of-work reasoning as bar 1,
# one notch weaker because novelty is strictly rarer than disagreement by construction.
BAR2_NOVELTY_MIN = 0.05
# BAR 3 PRIOR COVERAGE. A prior that is itself mostly `no_majority`/`thin`/`absent` is not a
# frame of reference. 0.70 attributed is the coverage bar this series already used for a new
# allocation dimension (the act/artifact preregistration), reused rather than re-invented.
BAR3_COVERAGE_MIN = 0.70
# BAR 4 INDEPENDENCE. |r| with log window volume must be < 0.5. The most reliably informative
# test across five studies: derived routing clusters were size x did-anything-run; output volume
# +0.552; interactivity +0.497; an authoring split +0.737. Two signals passed and both shipped.
BAR4_ABS_R_MAX = 0.50
# ---------------------------------------------------------------------------------------------


def _epoch(ts):
    return datetime.fromisoformat(ts.replace("Z", "+00:00"))


# --------------------------------------------------------------------- prior + contrast

def causal_turns(turns, cutoff):
    """The turns a prior may rest on: strictly BEFORE `cutoff`.

    Half-open on the right, and that is the whole of the causal claim. `<=` would admit a turn
    at the boundary instant — for the `before` variant that is a turn inside the window being
    characterised, which puts the window into its own frame of reference.
    """
    return [o for o in turns if _epoch(o["timestamp"]) < cutoff]


def counts_at(rl, level):
    """{value: n} at one level of a rollup. `rollup` returns ranked (ref, n) pairs."""
    return {k: int(v) for k, v in (rl.get(level) or [])}


def prior_at(rl, dim):
    """The session prior for one dimension: value / share / status, under the SAME floor and
    MIN_EVIDENCE the window uses, with `window.attribution`'s own reason.

    `attribution` is called, not a local majority rule, so `no_majority` / `thin` / `tie` /
    `absent` arrive here as the four different facts they are. A prior that is itself
    `no_majority` is informative — it says the window's ambiguity is the session's — and must
    never be collapsed into "no prior".
    """
    a = attribution(rl, LEVEL_OF[dim], FLOOR_OF[dim], MIN_EVIDENCE)
    counts = counts_at(rl, LEVEL_OF[dim])
    return {"value": a.value, "share": round(a.share, 4), "evidence": a.evidence,
            "reason": a.reason, "distinct": len(counts),
            "top": [{"value": k, "n": n} for k, n in
                    sorted(counts.items(), key=lambda kv: (-kv[1], kv[0]))[:5]]}


def contrast(w, prior, prior_counts):
    """The three per-window contrast measures. NEVER a fallback: when the window has no value of
    its own, all three are None and the window stays unattributed.

    `departure` is the window's share MINUS the session's share OF THE WINDOW'S VALUE — not the
    difference of the two dominant shares, which would compare two different values. `novel` is
    that same value having no presence in the session at all, which is only askable when the
    session HAS evidence at this level; against an empty prior everything is trivially novel and
    that is reported as `prior_empty`, not as yield.
    """
    v = w["value"]
    if v is None:
        return {"agrees": None, "departure": None, "novel": None, "prior_empty": None}
    total = sum(prior_counts.values())
    if not total:
        return {"agrees": None, "departure": None, "novel": None, "prior_empty": True}
    ps = prior_counts.get(v, 0) / total
    return {
        # Defined only where the prior is itself attributed. A `no_majority` prior has no value
        # to agree with, and scoring it as disagreement would count the session's ambiguity as
        # the window departing from it.
        "agrees": (prior["value"] == v) if prior["reason"] == "attributed" else None,
        "departure": round(w["share"] - ps, 4),
        "prior_share_of_window_value": round(ps, 4),
        "novel": v not in prior_counts,
        "prior_empty": False,
    }


# --------------------------------------------------------------------- the frame

def windows_of(path, fid, turns, evidence):
    """One transcript's fixed-grid windows, each with BOTH prior variants recomputed whole.

    `evidence` is the whole-file `scan_workspace` triple, passed in rather than re-derived per
    call. That is not an approximation: `events_for_turns` re-scans the WHOLE file when it is
    not supplied, so the triple is identical either way — it is the same value, computed once
    instead of once per rollup, and there are three rollups per window here.
    """
    root = os.path.dirname(os.path.dirname(path))

    def roll(sl):
        rows, pending, _n = events_for_turns(sl, path, root, (), None, evidence)
        recon, _stats = reconcile(pending, COMPONENT_DEPTH)
        return rollup(rows + recon)

    t0, tN = _epoch(turns[0]["timestamp"]), _epoch(turns[-1]["timestamp"])
    start, out = t0, []
    while start < tN:
        end = start + timedelta(minutes=SPAN)
        sl = [o for o in turns if start <= _epoch(o["timestamp"]) < end]
        here, start = start, start + timedelta(minutes=STRIDE)
        if not sl:
            continue
        rl = roll(sl)
        rec = {"wid": f"{os.path.basename(path)[:8]}#t{fid:04d}-{here:%Y%m%dT%H%M}",
               "file": path, "fid": fid, "prefix": os.path.basename(path)[:8],
               "start": here.isoformat(), "end": (here + timedelta(minutes=SPAN)).isoformat(),
               "n_turns": len(sl),
               # volume == every ref event at every level except `term`, the same definition the
               # three prior studies on this frame used, so "window volume" means one thing.
               "volume": int(sum(n for lv, items in rl.items() if lv != "term"
                                 for _r, n in items))}
        # RECOMPUTE, never accumulate: a fresh parse of the causal prefix per window per variant.
        cut = {"before": here, "sofar": here + timedelta(minutes=SPAN)}
        pref = {k: causal_turns(turns, c) for k, c in cut.items()}
        pr_rl = {k: (roll(t) if t else {}) for k, t in pref.items()}
        rec["prior_turns"] = {k: len(t) for k, t in pref.items()}
        for dim in DIMS:
            a = attribution(rl, LEVEL_OF[dim], FLOOR_OF[dim], MIN_EVIDENCE)
            w = {"value": a.value, "share": round(a.share, 4), "evidence": a.evidence,
                 "reason": a.reason}
            rec[dim] = {"window": w}
            for k in VARIANTS:
                p = prior_at(pr_rl[k], dim)
                rec[dim][k] = {"prior": p,
                               "contrast": contrast(w, p, counts_at(pr_rl[k], LEVEL_OF[dim]))}
        out.append(rec)
    return out


def assert_frame(recs, per_file, files):
    """Per-file identity, asserted. The session-collision merge looked entirely plausible when it
    happened — 550 windows against a true 1,022, no error raised — so the count is an assertion
    and the trap is re-checked live rather than assumed still live."""
    n = len(recs)
    assert n == sum(per_file.values()), \
        f"frame rows {n} != per-file recount {sum(per_file.values())}"
    assert len({r["wid"] for r in recs}) == n, "colliding wids — the frame merged windows"
    assert len({(r["file"], r["start"]) for r in recs}) == n, "duplicate (file, start)"
    windowed = {f for f, k in per_file.items() if k}
    assert len({r["file"] for r in recs}) == len(windowed), \
        f"{len(windowed)} files windowed but the frame holds {len({r['file'] for r in recs})}"
    by_prefix = len({(r["prefix"], r["start"]) for r in recs})
    colliding = sum(c for _p, c in collections.Counter(
        os.path.basename(f)[:8] for f in files).items() if c > 1)
    assert by_prefix < n, ("the prefix-keyed count did NOT come out lower, so this assertion is "
                           "not testing anything on this corpus — check the trap first")
    assert n == EXPECTED_WINDOWS, (
        f"frame is {n} windows, expected {EXPECTED_WINDOWS}. Verify a new number by hand: "
        f"{by_prefix} is what the known session-collision bug produces here and it raises no "
        f"error on its own.")
    first = sum(1 for r in recs if r["prior_turns"]["before"] == 0)
    print(f"ASSERTED windows={n} files={len(per_file)} distinct_wids={n}")
    print(f"  session-collision check: prefix-keyed would give {by_prefix} "
          f"({n - by_prefix} lost, {round(100 * (n - by_prefix) / n, 1)}%); "
          f"{colliding} of {len(files)} files sit in a colliding prefix group")
    print(f"  session-first windows (empty `before` prior): {first} "
          f"({first / n:.1%}) over {len(windowed)} sessions")


def build(roots, out):
    files = sorted(f for r in roots
                   for f in glob.glob(os.path.join(r, "**", "*.jsonl"), recursive=True))
    if not files:
        sys.exit("no transcripts under " + ", ".join(roots))
    t0, recs, per_file, empty = time.time(), [], {}, 0
    for fid, path in enumerate(files):
        turns = [o for o in iter_turns(path)]
        if not turns:
            empty += 1
            per_file[path] = 0
            continue
        w = windows_of(path, fid, turns, scan_workspace(path))
        per_file[path] = len(w)
        recs.extend(w)
        if (fid + 1) % 50 == 0:
            print(f"  [{fid + 1}/{len(files)}] {len(recs)} windows "
                  f"({time.time() - t0:.0f}s)", flush=True)
    print(f"parsed {len(files)} files in {time.time() - t0:.1f}s (empty={empty})")
    assert_frame(recs, per_file, files)
    os.makedirs(out, exist_ok=True)
    dst = os.path.join(out, "prior-frame.ndjson")
    with open(dst, "w") as fh:
        for r in recs:
            fh.write(json.dumps(r) + "\n")
    print("frame ->", dst)
    return recs


def load_frame(out):
    with open(os.path.join(out, "prior-frame.ndjson")) as fh:
        recs = [json.loads(l) for l in fh]
    assert len(recs) == EXPECTED_WINDOWS, f"{len(recs)} windows, expected {EXPECTED_WINDOWS}"
    assert len({(r["file"], r["start"]) for r in recs}) == len(recs), "duplicate (file, start)"
    return recs


# --------------------------------------------------------------------- the bars

def pearson(xs, ys):
    """Pearson r, pure Python. On a 0/1 x this is the point-biserial correlation."""
    n = len(xs)
    if n < 2:
        return 0.0
    mx, my = statistics.fmean(xs), statistics.fmean(ys)
    sxy = sum((a - mx) * (b - my) for a, b in zip(xs, ys))
    sxx = sum((a - mx) ** 2 for a in xs)
    syy = sum((b - my) ** 2 for b in ys)
    if sxx <= 0 or syy <= 0:
        return 0.0
    return sxy / math.sqrt(sxx * syy)


def agreement(recs, dim, variant):
    """Bar 1, for one dimension. Denominator is windows where BOTH sides are attributed."""
    pop = [r for r in recs if r[dim][variant]["contrast"]["agrees"] is not None]
    agree = sum(1 for r in pop if r[dim][variant]["contrast"]["agrees"])
    w_attr = sum(1 for r in recs if r[dim]["window"]["reason"] == "attributed")
    return {"dim": dim, "variant": variant, "n_both_attributed": len(pop),
            "window_attributed": w_attr, "agree": agree,
            "disagree": len(pop) - agree,
            "agreement": round(agree / len(pop), 4) if pop else None}


def novelty(recs, dim, variant):
    """Bar 2, for one dimension. Denominator is windows with a value AND a non-empty prior —
    novelty against an empty prior is not yield, and is counted separately."""
    have = [r for r in recs if r[dim]["window"]["reason"] == "attributed"]
    pop = [r for r in have if r[dim][variant]["contrast"]["prior_empty"] is False]
    nov = [r for r in pop if r[dim][variant]["contrast"]["novel"]]
    return {"dim": dim, "variant": variant, "n_window_attributed": len(have),
            "n_with_prior_evidence": len(pop),
            "prior_empty": sum(1 for r in have
                               if r[dim][variant]["contrast"]["prior_empty"]),
            "novel": len(nov),
            "novelty": round(len(nov) / len(pop), 4) if pop else None,
            "examples": [{"wid": r["wid"], "value": r[dim]["window"]["value"],
                          "share": r[dim]["window"]["share"],
                          "evidence": r[dim]["window"]["evidence"],
                          "prior_status": r[dim][variant]["prior"]["reason"],
                          "prior_evidence": r[dim][variant]["prior"]["evidence"],
                          "prior_top": r[dim][variant]["prior"]["top"][:3]}
                         for r in sorted(nov, key=lambda x: -x[dim]["window"]["evidence"])[:8]]}


def prior_coverage(recs, dim, variant):
    """Bar 3, for one dimension: the REASONS breakdown of the PRIOR itself, every reason its own
    row including the zero ones."""
    by = collections.Counter(r[dim][variant]["prior"]["reason"] for r in recs)
    for reason in by:
        assert reason in REASONS, f"unknown reason {reason!r} — window.REASONS drifted"
    n = len(recs)
    ev = sorted(r[dim][variant]["prior"]["evidence"] for r in recs)
    # `absent` is TWO different facts and the split is the whole reframe. A session's FIRST
    # window has no session behind it — no floor and no dimension fixes that, it is the boundary
    # the spec's own "honest answer" section is about. A LATER window whose prior is still absent
    # is a level that has never fired in this session, which is the `tooling`-is-77.8%-absent
    # shape. Pooling them would describe neither, the same way pooling `thin` with `no_majority`
    # once made a thin facet look like a confident one.
    exists = [r for r in recs if r["prior_turns"][variant] > 0]
    first = [r for r in recs if r["prior_turns"][variant] == 0]
    return {"dim": dim, "variant": variant, "n": n,
            "coverage": round(by["attributed"] / n, 4) if n else 0.0,
            "reasons": {k: by[k] for k in REASONS},
            "reason_share": {k: round(by[k] / n, 4) if n else 0.0 for k in REASONS},
            "n_session_first": len(first),
            "n_prior_exists": len(exists),
            "absent_session_first": sum(1 for r in first
                                        if r[dim][variant]["prior"]["reason"] == "absent"),
            "absent_level_never_fired": sum(1 for r in exists
                                            if r[dim][variant]["prior"]["reason"] == "absent"),
            "coverage_given_prior_exists":
                round(sum(1 for r in exists
                          if r[dim][variant]["prior"]["reason"] == "attributed") / len(exists), 4)
                if exists else None,
            "prior_evidence_median": ev[n // 2] if n else 0}


def independence(recs, dim, variant):
    """Bar 4, for one dimension: each contrast measure against log window volume."""
    out = {"dim": dim, "variant": variant}
    pop = [r for r in recs if r[dim][variant]["contrast"]["agrees"] is not None]
    if len(pop) >= 2:
        out["r_agrees"] = round(pearson(
            [1.0 if r[dim][variant]["contrast"]["agrees"] else 0.0 for r in pop],
            [math.log1p(r["volume"]) for r in pop]), 4)
        out["n_agrees"] = len(pop)
    pop = [r for r in recs if r[dim]["window"]["reason"] == "attributed"
           and r[dim][variant]["contrast"]["prior_empty"] is False]
    if len(pop) >= 2:
        out["r_novel"] = round(pearson(
            [1.0 if r[dim][variant]["contrast"]["novel"] else 0.0 for r in pop],
            [math.log1p(r["volume"]) for r in pop]), 4)
        out["r_departure"] = round(pearson(
            [r[dim][variant]["contrast"]["departure"] for r in pop],
            [math.log1p(r["volume"]) for r in pop]), 4)
        out["r_abs_departure"] = round(pearson(
            [abs(r[dim][variant]["contrast"]["departure"]) for r in pop],
            [math.log1p(r["volume"]) for r in pop]), 4)
        out["n_novel"] = len(pop)
    return out


def departures(recs, dim, variant, k=8):
    """The largest |departure| windows, named. Roughly twenty defects in this study surfaced as
    plausible wrong numbers and essentially none was caught by reading an aggregate."""
    pop = [r for r in recs if r[dim]["window"]["reason"] == "attributed"
           and r[dim][variant]["contrast"].get("departure") is not None]
    pop.sort(key=lambda r: -abs(r[dim][variant]["contrast"]["departure"]))
    return [{"wid": r["wid"], "volume": r["volume"],
             "window": f'{r[dim]["window"]["value"]} {r[dim]["window"]["share"]:.3f}'
                       f' (n={r[dim]["window"]["evidence"]})',
             "prior": f'{r[dim][variant]["prior"]["reason"]}'
                      f' {r[dim][variant]["prior"]["value"]}'
                      f' {r[dim][variant]["prior"]["share"]:.3f}'
                      f' (n={r[dim][variant]["prior"]["evidence"]})',
             "prior_share_of_window_value":
                 r[dim][variant]["contrast"]["prior_share_of_window_value"],
             "departure": r[dim][variant]["contrast"]["departure"],
             "novel": r[dim][variant]["contrast"]["novel"],
             "prior_top": r[dim][variant]["prior"]["top"][:3]}
            for r in pop[:k]]


def motivating_case(recs, variant):
    """The excursion this design exists for: `language: Python` just over the 0.50 floor inside a
    session that is overwhelmingly something else. Found, not assumed — and the test is whether
    the CONTRAST MEASURES FLAG IT, not whether it exists."""
    hits = []
    for r in recs:
        w = r["language"]["window"]
        if w["reason"] != "attributed":
            continue
        c = r["language"][variant]["contrast"]
        p = r["language"][variant]["prior"]
        if c["prior_empty"] is not False:
            continue
        if w["share"] >= 0.70:            # not near the floor; not the shape in question
            continue
        if p["reason"] == "attributed" and p["value"] == w["value"]:
            continue                      # the session agrees; not an excursion
        hits.append({
            "wid": r["wid"], "file": os.path.basename(r["file"]), "volume": r["volume"],
            "window_value": w["value"], "window_share": w["share"],
            "window_evidence": w["evidence"],
            "prior_status": p["reason"], "prior_value": p["value"],
            "prior_share": p["share"], "prior_evidence": p["evidence"],
            "prior_top": p["top"][:4],
            "prior_share_of_window_value": c["prior_share_of_window_value"],
            "departure": c["departure"], "novel": c["novel"], "agrees": c["agrees"]})
    hits.sort(key=lambda h: (h["window_share"] - 0.576) ** 2)
    return hits


def do_frame(_a):
    build(ROOTS, OUT)


def do_measure(_a):
    recs = load_frame(OUT)
    print(f"loaded {len(recs)} windows over {len({r['file'] for r in recs})} sessions\n")
    res = {"windows": len(recs), "sessions": len({r["file"] for r in recs}),
           "span_minutes": SPAN, "stride_minutes": STRIDE, "primary_variant": PRIMARY,
           "bars": {"agreement_max": BAR1_AGREEMENT_MAX, "novelty_min": BAR2_NOVELTY_MIN,
                    "coverage_min": BAR3_COVERAGE_MIN, "abs_r_max": BAR4_ABS_R_MAX},
           "can_differ": list(CAN_DIFFER), "by_variant": {}}
    for variant in VARIANTS:
        v = {"agreement": [agreement(recs, d, variant) for d in DIMS],
             "novelty": [novelty(recs, d, variant) for d in DIMS],
             "coverage": [prior_coverage(recs, d, variant) for d in DIMS],
             "independence": [independence(recs, d, variant) for d in DIMS],
             "departures": {d: departures(recs, d, variant) for d in DIMS}}
        res["by_variant"][variant] = v
    res["motivating_case"] = {k: motivating_case(recs, k)[:10] for k in VARIANTS}
    res["motivating_case_n"] = {k: len(motivating_case(recs, k)) for k in VARIANTS}

    # Verdicts, computed from the pre-registered constants rather than eyeballed against them.
    P = res["by_variant"][PRIMARY]
    ag = {a["dim"]: a["agreement"] for a in P["agreement"]}
    nv = {a["dim"]: a["novelty"] for a in P["novelty"]}
    cv = {a["dim"]: a["coverage"] for a in P["coverage"]}
    cvx = {a["dim"]: a["coverage_given_prior_exists"] for a in P["coverage"]}
    rs = []
    for row in P["independence"]:
        rs += [(row["dim"], k, abs(v)) for k, v in row.items()
               if k.startswith("r_") and v is not None]
    res["verdict"] = {
        "bar1_agreement": {
            "pass": any(ag[d] is not None and ag[d] <= BAR1_AGREEMENT_MAX for d in CAN_DIFFER),
            "dims_at_or_below": [d for d in CAN_DIFFER
                                 if ag[d] is not None and ag[d] <= BAR1_AGREEMENT_MAX],
            "per_dim": ag},
        "bar2_novelty": {
            "pass": any(nv[d] is not None and nv[d] >= BAR2_NOVELTY_MIN for d in DIMS),
            "dims_at_or_above": [d for d in DIMS
                                 if nv[d] is not None and nv[d] >= BAR2_NOVELTY_MIN],
            "per_dim": nv},
        "bar3_coverage": {
            "pass_dims": [d for d in DIMS if cv[d] >= BAR3_COVERAGE_MIN],
            "fail_dims": [d for d in DIMS if cv[d] < BAR3_COVERAGE_MIN],
            "per_dim": cv,
            # Same bar, taken only over windows that HAVE a session behind them. Reported
            # separately, never in place of the number above: a session's first window is a real
            # window a reader will see, and moving it out of the denominator would answer an
            # easier question than the one the bar asks.
            "per_dim_given_prior_exists": cvx,
            "pass_dims_given_prior_exists": [d for d in DIMS
                                             if cvx[d] is not None
                                             and cvx[d] >= BAR3_COVERAGE_MIN]},
        "bar4_independence": {
            "pass": all(x < BAR4_ABS_R_MAX for _d, _k, x in rs),
            "max_abs_r": round(max((x for _d, _k, x in rs), default=0.0), 4),
            "max_at": max(rs, key=lambda t: t[2])[:2] if rs else None,
            "violations": [{"dim": d, "measure": k, "abs_r": round(x, 4)}
                           for d, k, x in rs if x >= BAR4_ABS_R_MAX]},
        "sofar_novelty_structurally_zero":
            all((a["novel"] == 0) for a in res["by_variant"]["sofar"]["novelty"]),
    }
    os.makedirs(OUT, exist_ok=True)
    with open(os.path.join(OUT, "session-prior-results.json"), "w") as fh:
        json.dump(res, fh, indent=2)
    render(res)
    print("\nresults ->", os.path.join(OUT, "session-prior-results.json"))


def render(res):
    for variant in VARIANTS:
        v = res["by_variant"][variant]
        tag = "PRIMARY" if variant == PRIMARY else "spec-literal"
        print(f"\n{'=' * 92}\n## variant `{variant}` ({tag})\n{'=' * 92}")
        print("\n### BAR 1 AGREEMENT — window value == prior value, where BOTH are attributed\n")
        print(f"{'dimension':12}{'both attr':>11}{'agree':>8}{'disagree':>10}{'agreement':>12}"
              f"{'w.attrib':>10}")
        for a in v["agreement"]:
            s = "n/a" if a["agreement"] is None else f"{a['agreement']:.1%}"
            print(f"{a['dim']:12}{a['n_both_attributed']:11d}{a['agree']:8d}"
                  f"{a['disagree']:10d}{s:>12}{a['window_attributed']:10d}")
        print("\n### BAR 2 NOVELTY — window value absent from the prior entirely\n")
        print(f"{'dimension':12}{'w.attrib':>10}{'w/ prior ev':>13}{'prior empty':>13}"
              f"{'novel':>8}{'novelty':>10}")
        for a in v["novelty"]:
            s = "n/a" if a["novelty"] is None else f"{a['novelty']:.1%}"
            print(f"{a['dim']:12}{a['n_window_attributed']:10d}{a['n_with_prior_evidence']:13d}"
                  f"{a['prior_empty']:13d}{a['novel']:8d}{s:>10}")
        print("\n### BAR 3 PRIOR COVERAGE — the prior's OWN attribution reasons\n")
        print(f"{'dimension':12}{'attrib':>9}{'thin':>8}{'no_maj':>8}{'tie':>7}{'absent':>8}"
              f"{'cover':>8}{'ev_med':>8}   absent split (session-first / level-never-fired)"
              f"   cover|prior exists")
        for a in v["coverage"]:
            r = a["reasons"]
            cg = "  n/a" if a["coverage_given_prior_exists"] is None \
                else f"{a['coverage_given_prior_exists']:.1%}"
            print(f"{a['dim']:12}{r['attributed']:9d}{r['thin']:8d}{r['no_majority']:8d}"
                  f"{r['tie']:7d}{r['absent']:8d}{a['coverage']:8.1%}"
                  f"{a['prior_evidence_median']:8d}   {a['absent_session_first']:5d}"
                  f" / {a['absent_level_never_fired']:5d}                     {cg:>8}"
                  f"  (n={a['n_prior_exists']})")
        print("\n### BAR 4 INDEPENDENCE — |r| with log(1+window volume), must be < 0.50\n")
        print(f"{'dimension':12}{'r_agrees':>10}{'r_novel':>10}{'r_depart':>10}{'r_|dep|':>10}"
              f"{'n':>8}")
        for a in v["independence"]:
            f4 = lambda k: ("     --  " if a.get(k) is None else f"{a[k]:10.3f}")  # noqa: E731
            print(f"{a['dim']:12}{f4('r_agrees')}{f4('r_novel')}{f4('r_departure')}"
                  f"{f4('r_abs_departure')}{a.get('n_novel', 0):8d}")

    P = PRIMARY
    print(f"\n{'=' * 92}\n## Named windows (variant `{P}`)\n{'=' * 92}")
    for d in DIMS:
        nv = next(a for a in res["by_variant"][P]["novelty"] if a["dim"] == d)
        if nv["examples"]:
            print(f"\nNOVEL — {d} ({nv['novel']} of {nv['n_with_prior_evidence']}):")
            for e in nv["examples"]:
                top = ", ".join(f"{t['value']}:{t['n']}" for t in e["prior_top"])
                print(f"  {e['wid']:44} {e['value']}  share={e['share']:.3f} "
                      f"n={e['evidence']}  | prior {e['prior_status']} n={e['prior_evidence']} "
                      f"top=[{top}]")
    for d in DIMS:
        dep = res["by_variant"][P]["departures"][d]
        if dep:
            print(f"\nLARGEST |departure| — {d}:")
            for e in dep[:5]:
                top = ", ".join(f"{t['value']}:{t['n']}" for t in e["prior_top"])
                print(f"  {e['wid']:44} dep={e['departure']:+.3f} vol={e['volume']:5d} "
                      f"win={e['window']} | prior={e['prior']} top=[{top}]")

    print(f"\n{'=' * 92}\n## The motivating case — `language` near the floor, session disagrees"
          f"\n{'=' * 92}")
    for k in VARIANTS:
        hits = res["motivating_case"][k]
        print(f"\nvariant `{k}`: {res['motivating_case_n'][k]} such windows")
        for h in hits[:6]:
            top = ", ".join(f"{t['value']}:{t['n']}" for t in h["prior_top"])
            print(f"  {h['wid']:44} {h['window_value']} {h['window_share']:.3f} "
                  f"(n={h['window_evidence']})")
            print(f"      prior {h['prior_status']} {h['prior_value']} {h['prior_share']:.3f} "
                  f"(n={h['prior_evidence']}) top=[{top}]")
            print(f"      departure={h['departure']:+.3f} novel={h['novel']} "
                  f"agrees={h['agrees']} prior_share_of_window_value="
                  f"{h['prior_share_of_window_value']:.3f}")

    print(f"\n{'=' * 92}\n## VERDICT (primary variant `{PRIMARY}`)\n{'=' * 92}")
    print(json.dumps(res["verdict"], indent=2))


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("cmd", choices=("frame", "measure"))
    a = ap.parse_args()
    {"frame": do_frame, "measure": do_measure}[a.cmd](a)
