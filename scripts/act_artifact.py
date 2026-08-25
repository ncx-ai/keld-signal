#!/usr/bin/env python3
"""Measure `action` and the `(act, artifact)` pair against the three bars fixed in
~/keld/refseries-context/act-artifact/PREREGISTRATION.md, before any of this was written.

    PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/act_artifact.py frame
    PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/act_artifact.py measure

## What is being measured, and why it does not exist today

`action` appears ZERO times in `sidecar/app/analysis/workstreams.py`. The level is emitted by
`levels.events_for_turns` (from tool names and, via `shell.bash_refs`, from the programs a Bash
command actually runs), stored, and read by `dynamics`/`activity` — and never published. The
published `ALLOCATION` set is project / branch / model / output_type / language / skill /
tooling. Meanwhile `vocab.py`'s own docstring says the physical act is "what a reader needs to
picture the work". The module that defines it says it is needed; the module that decides what
ships omits it.

**This is not a fifth `activity_type` attempt.** Those four asked what a change MEANT, which is
unreachable from levels that record what physically touched a file. This asks what was physically
DONE, to what kind of thing. Both halves are closed vocabularies already extracted and stored, so
THERE IS NO GROUND TRUTH TO ESTABLISH AND NO BLIND TO ENFORCE — the action IS the action. No
labelling harness appears in this file, deliberately: one would be a category error.

## The three bars, quoted from the preregistration

    Q1 COVERAGE       >= 0.70 attributed at a 60-minute window, at the existing 0.50 share floor
                      and window.MIN_EVIDENCE == 5.
    Q2 CONCENTRATION  most common (act, artifact) pair < 60% of attributed windows, AND >= 5
                      distinct pairs each >= 3%.
    Q3 CONJUNCTION    normalised mutual information between act and artifact >= 0.10. Below it the
                      pair is reconstructible from the marginals -- `output_type` IS the artifact
                      half and already ships -- so publishing it is redundant.

`action` is the ONLY new dimension in question. `artifact` is measured too, because it is the
`output_type` that already ships and Q3 is a statement about the two together.

## Why the numbers are the production numbers

Nothing here reimplements a level. `levels.events_for_turns` + `reconcile` + `window.rollup` +
`window.attribution` are imported from `app.analysis` and called exactly as
`analyze._rollup_by_parse` calls them, including `COMPONENT_DEPTH` and the per-window reconcile
scope. `attribution`, not a local majority rule, is what decides a window: the 0.50 floor, the
`MIN_EVIDENCE` count floor, and the tie rule all arrive with it, and so does the distinction
between `absent` and `no_majority` that this study is required to report separately.

`artifact` rows come from TWO places -- `events_for_turns` (skills) and `reconcile` (extensions
and directory shapes) -- so `reconcile` MUST run here. The two prior studies on this frame
deliberately skipped it because their mappings read only `action`/`tool`; skipping it here would
measure an artifact level with its extension evidence missing, which is not the level that ships.

## The frame, and the trap it is asserted against

Span 60 / stride 50 minutes, cut INSIDE the per-file loop so no cross-file merge is even
representable, identity `(file, start)` with a per-file `fid` in the wid. 1,022 windows over 500
transcripts, ASSERTED. `levels.display_session` is `basename(path)[:8]`, which collides for 445 of
500 files; a study keyed on it once reported 550 windows against a true 1,022 and raised no error.
The collision is therefore checked LIVE (the prefix-keyed count must come out lower, or the
assertion is testing nothing on this corpus) rather than trusted.

## Vocabulary provenance

Measured on the post-`4ad9add` vocabulary, which moved `transform` 4345 -> 191, `test`
1991 -> 3772 and added 1,095 heredoc create/edit events. `assert_vocab_fixed` proves at RUN time
that the code being imported is the corrected one -- three probes, one per part of that fix -- so
a void measurement fails the run instead of the review.
"""
import argparse, collections, glob, json, math, os, sys, time
from datetime import datetime, timedelta

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
                                "sidecar"))

from app.analysis import COMPONENT_DEPTH
from app.analysis.levels import events_for_turns
from app.analysis.reconcile import reconcile
from app.analysis.transcript import iter_turns
from app.analysis.vocab import action_for
from app.analysis.window import MIN_EVIDENCE, REASONS, attribution, rollup
from app.analysis.workstreams import ALLOCATION

SPAN, STRIDE = 60, 50                  # minutes; stride must not divide span (this series)
EXPECTED_WINDOWS = 1022
FLOOR = 0.50                           # the existing share floor, not a new one
OUT = os.path.expanduser("~/keld/refseries-context/act-artifact")
ROOTS = [os.path.expanduser("~/keld/refseries-context/frozen-corpus/projects"),
         os.path.expanduser("~/keld/refseries-context/frozen-corpus/john-projects")]

# The three bars. Named constants so the verdict is computed from the preregistration's numbers
# rather than eyeballed against them.
Q1_COVERAGE_MIN = 0.70
Q2_TOP_PAIR_MAX = 0.60
Q2_MIN_PAIRS, Q2_PAIR_SHARE_MIN = 5, 0.03
Q3_NMI_MIN = 0.10

# The normalisation the VERDICT is taken on: MI / mean(H(X), H(Y)) -- what an unqualified "NMI"
# means in the tool a reader would reach for (sklearn.normalized_mutual_info_score's default).
# All four are reported, because a bar that depends on which denominator was chosen is not a bar;
# if they disagree the report must say so.
NMI_PRIMARY = "arithmetic"


def assert_vocab_fixed():
    """Prove the imported vocabulary is post-`4ad9add`. Three probes, one per part of that fix.

    A measurement of `action` taken on the pre-fix vocabulary is VOID by preregistration rule 4,
    and the pre-fix code is a `git checkout` away at all times. This fails the run.
    """
    # 1. A stream filter in a read pipeline is `read`, not `transform` (transform 4345 -> 191).
    got = action_for(exe="sed", verb="sed -n", args=["-n", "1,20p", "x.txt"])
    assert got == "read", f"sed in a read pipeline reads {got!r}, want 'read' — pre-4ad9add vocab"
    assert action_for(exe="sed", verb="sed -i", args=["-i", "s/a/b/", "x"]) == "transform", \
        "sed -i is not `transform` — the in-place case was lost, not just the blanket one"
    # 2. A task runner is not a service; the act is the script it runs (test 1991 -> 3772).
    got = action_for(exe="pnpm", verb="pnpm run", args=["run", "test"])
    assert got == "test", f"`pnpm run test` reads {got!r}, want 'test' — pre-4ad9add vocab"
    # 3. A heredoc redirected into a path is a write (+1,095 create/edit events).
    got = action_for(exe="cat", verb="cat", args=[">", "probe.go", "<<GO"])
    assert got == "create", f"heredoc `cat > probe.go <<GO` reads {got!r} — pre-4ad9add vocab"
    print("VOCAB post-4ad9add confirmed: sed-read / sed -i-transform / pnpm-run-test / "
          "heredoc-create all present")


# --------------------------------------------------------------------- the frame

def _epoch(ts):
    return datetime.fromisoformat(ts.replace("Z", "+00:00"))


def windows_of(path, fid, turns):
    """One transcript's fixed-grid windows, cut INSIDE the per-file loop.

    `reconcile` IS run, unlike the two prior studies on this frame: `artifact` rows come from both
    `events_for_turns` (skills) and `reconcile` (extensions, unpacked-office directory shapes),
    and `output_type` -- the dimension that already ships -- is the rollup of both. Scope is this
    window's own `pending`, which is exactly `analyze._rollup_by_parse`'s scope.
    """
    root = os.path.dirname(os.path.dirname(path))
    t0, tN = _epoch(turns[0]["timestamp"]), _epoch(turns[-1]["timestamp"])
    start, out = t0, []
    while start < tN:
        end = start + timedelta(minutes=SPAN)
        sl = [o for o in turns if start <= _epoch(o["timestamp"]) < end]
        here, start = start, start + timedelta(minutes=STRIDE)
        if not sl:
            continue
        rows, pending, _n = events_for_turns(sl, path, root, (), None)
        recon, _stats = reconcile(pending, COMPONENT_DEPTH)
        rl = rollup(rows + recon)
        rec = {"wid": f"{os.path.basename(path)[:8]}#t{fid:04d}-{here:%Y%m%dT%H%M}",
               "file": path, "fid": fid, "prefix": os.path.basename(path)[:8],
               "start": here.isoformat(), "n_turns": len(sl),
               # volume == every ref event at every level except `term`, matching this series'
               # three prior studies so "window volume" means the same thing in all four.
               "volume": int(sum(n for lv, items in rl.items() if lv != "term"
                                 for _r, n in items))}
        for name, level, floor in ALLOCATION + [("action", "action", FLOOR)]:
            a = attribution(rl, level, floor, MIN_EVIDENCE)
            rec[name] = {"value": a.value, "share": round(a.share, 4),
                         "evidence": a.evidence, "reason": a.reason}
        # The full distributions of the two levels in question, so every number below is
        # recomputable from the frame file alone.
        rec["action_counts"] = {k: int(v) for k, v in (rl.get("action") or [])}
        rec["artifact_counts"] = {k: int(v) for k, v in (rl.get("artifact") or [])}
        rec["levels"] = sorted(rl)
        out.append(rec)
    return out


def assert_frame(recs, per_file, files):
    """The frame assertions. The session-collision merge looked perfectly plausible when it
    happened (550 windows against a true 1,022, no error raised), so the count is an ASSERTION and
    the trap is checked live rather than assumed still live."""
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
                           "not testing anything on this corpus — check the trap before "
                           "trusting the frame")
    assert n == EXPECTED_WINDOWS, (
        f"frame is {n} windows, expected {EXPECTED_WINDOWS}. Verify a new number by hand: "
        f"{by_prefix} is what the known session-collision bug produces here and it raises no "
        f"error on its own.")
    print(f"ASSERTED windows={n} files={len(per_file)} distinct_wids={n}")
    print(f"  session-collision check: prefix-keyed would give {by_prefix} "
          f"({n - by_prefix} lost, {round(100 * (n - by_prefix) / n, 1)}%); "
          f"{colliding} of {len(files)} files sit in a colliding prefix group")


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
        w = windows_of(path, fid, turns)
        per_file[path] = len(w)
        recs.extend(w)
    print(f"parsed {len(files)} files in {time.time() - t0:.1f}s (empty={empty})")
    assert_frame(recs, per_file, files)
    os.makedirs(out, exist_ok=True)
    dst = os.path.join(out, "act-frame.ndjson")
    with open(dst, "w") as fh:
        for r in recs:
            fh.write(json.dumps(r) + "\n")
    print("frame ->", dst)
    return recs


def load_frame(out):
    """Read the durable frame, normalising the ONE published key that has been renamed since it
    was written.

    The frame lives outside the repo and is immutable measurement output: it was emitted while
    the `skill` level published as `workflow`, and rewriting a completed study's durable file to
    match a later vocabulary would falsify the record of what the run produced. So the rename is
    absorbed HERE, at the read boundary, keyed on the old name being present and the new one
    absent — a frame written after the rename passes through untouched, and a frame written
    before it is read under the name `ALLOCATION` now uses. Nothing else in the file moved: the
    level, the values and every count are the same.
    """
    with open(os.path.join(out, "act-frame.ndjson")) as fh:
        recs = [json.loads(l) for l in fh]
    for r in recs:
        if "workflow" in r and "skill" not in r:
            r["skill"] = r.pop("workflow")
    assert len(recs) == EXPECTED_WINDOWS, f"{len(recs)} windows, expected {EXPECTED_WINDOWS}"
    assert len({(r["file"], r["start"]) for r in recs}) == len(recs), "duplicate (file, start)"
    return recs


# --------------------------------------------------------------------- Q1 coverage

def coverage(recs, dim):
    """Attributed rate for one dimension, plus every unattributed reason SEPARATELY.

    `absent` and `no_majority` are different facts and pooling them is what would make a thin
    facet look like a confident one (`tooling` is 77.8% ABSENT at 5 minutes and 19% thin — one
    number describing both would describe neither). Every reason in `window.REASONS` gets a row,
    including the ones that come out zero, so a missing reason is visibly zero rather than absent
    from the table.
    """
    n = len(recs)
    by = collections.Counter(r[dim]["reason"] for r in recs)
    for reason in by:
        assert reason in REASONS, f"unknown reason {reason!r} — window.REASONS drifted"
    ev = sorted(r[dim]["evidence"] for r in recs)
    return {"dim": dim, "n": n,
            "coverage": round(by["attributed"] / n, 4) if n else 0.0,
            "reasons": {k: by[k] for k in REASONS},
            "reason_share": {k: round(by[k] / n, 4) if n else 0.0 for k in REASONS},
            "evidence_median": ev[n // 2] if n else 0,
            "distinct_values": len({r[dim]["value"] for r in recs if r[dim]["value"]})}


# --------------------------------------------------------------------- Q2 / Q3

def pairs(recs):
    """(act, artifact) for every window where BOTH halves are attributed.

    That is the population in which the pair EXISTS: a pair with one half unattributed is not a
    pair, and inventing a placeholder half would manufacture mass for a cell nothing observed.
    The share of windows that population covers is reported alongside, so its cost is visible
    rather than hidden in a denominator.
    """
    return [(r["action"]["value"], r["output_type"]["value"], r["wid"]) for r in recs
            if r["action"]["reason"] == "attributed"
            and r["output_type"]["reason"] == "attributed"]


def concentration(pr):
    c = collections.Counter((a, b) for a, b, _w in pr)
    n = sum(c.values())
    ranked = [{"pair": f"{a} x {b}", "act": a, "artifact": b, "n": k,
               "share": round(k / n, 4) if n else 0.0} for (a, b), k in c.most_common()]
    return {"n_attributed": n, "distinct_pairs": len(c), "ranked": ranked,
            "top_share": ranked[0]["share"] if ranked else 0.0,
            "pairs_over_min": sum(1 for r in ranked if r["share"] >= Q2_PAIR_SHARE_MIN)}


def _entropy(counter):
    n = sum(counter.values())
    return -sum((v / n) * math.log2(v / n) for v in counter.values() if v) if n else 0.0


def mutual_information(pr):
    """MI(act; artifact) in bits, with all four standard normalisations.

    Pure Python: the numbers in RESULTS.md must not depend on which numpy or sklearn is installed
    (this package is pandas/numpy-free by policy and the study follows it).

    Cramer's V is reported beside it as the "how far the joint departs from independence" reading
    the preregistration offers as equivalent -- a second, differently-shaped statistic on the same
    contingency table, so a bar cleared on one and missed on the other is visible instead of a
    choice of formula.
    """
    joint = collections.Counter((a, b) for a, b, _w in pr)
    n = sum(joint.values())
    if not n:
        return {"n": 0, "mi_bits": 0.0, "nmi": {k: 0.0 for k in
                ("arithmetic", "geometric", "min", "max")}, "cramers_v": 0.0}
    px = collections.Counter(a for a, _b, _w in pr)
    py = collections.Counter(b for _a, b, _w in pr)
    hx, hy = _entropy(px), _entropy(py)
    mi = chi2 = 0.0
    cells = []
    for (a, b), k in joint.items():
        pxy, pa, pb = k / n, px[a] / n, py[b] / n
        mi += pxy * math.log2(pxy / (pa * pb))
        exp = pa * pb * n
        chi2 += (k - exp) ** 2 / exp
        cells.append({"pair": f"{a} x {b}", "n": k, "expected": round(exp, 2),
                      "lift": round(pxy / (pa * pb), 3),
                      # This cell's own contribution to MI. A conjunction carried by cells nothing
                      # observed more than once or twice is an artefact of the estimator, not a
                      # property of the work -- see mi_null.
                      "mi_bits": round(pxy * math.log2(pxy / (pa * pb)), 5)})
    denom = min(len(px), len(py)) - 1
    nmi = {"arithmetic": mi / ((hx + hy) / 2) if hx + hy else 0.0,
           "geometric": mi / math.sqrt(hx * hy) if hx and hy else 0.0,
           "min": mi / min(hx, hy) if min(hx, hy) else 0.0,
           "max": mi / max(hx, hy) if max(hx, hy) else 0.0}
    thin = sum(c["mi_bits"] for c in cells if c["n"] < MIN_EVIDENCE)
    return {"n": n, "mi_bits": round(mi, 4),
            "mi_bits_from_cells_under_min_evidence": round(thin, 5),
            "mi_share_from_thin_cells": round(thin / mi, 4) if mi else 0.0,
            "cells_under_min_evidence": sum(1 for c in cells if c["n"] < MIN_EVIDENCE), "h_act": round(hx, 4),
            "h_artifact": round(hy, 4), "nmi": {k: round(v, 4) for k, v in nmi.items()},
            "cramers_v": round(math.sqrt(chi2 / (n * denom)), 4) if denom > 0 else 0.0,
            "chi2": round(chi2, 2), "df": (len(px) - 1) * (len(py) - 1),
            "distinct_act": len(px), "distinct_artifact": len(py),
            "top_lift": sorted(cells, key=lambda c: -c["n"])[:15]}


def profiles(recs, pr, top_n=6, per_pair=3):
    """Name real windows per pair, with the feature profile that produced them.

    A table of shares is not evidence that a partition is coherent -- the preregistration requires
    specific identifiers, because every refuted routing cluster looked fine as a table.
    """
    by_wid = {r["wid"]: r for r in recs}
    grouped = collections.defaultdict(list)
    for a, b, w in pr:
        grouped[(a, b)].append(w)
    out = []
    for (a, b), wids in sorted(grouped.items(), key=lambda kv: -len(kv[1]))[:top_n]:
        rows = []
        for w in wids[:per_pair]:
            r = by_wid[w]
            rows.append({
                "wid": w, "project": r["project"]["value"], "volume": r["volume"],
                "act": f"{a} {r['action']['share']:.2f} of {r['action']['evidence']}",
                "artifact": f"{b} {r['output_type']['share']:.2f} of "
                            f"{r['output_type']['evidence']}",
                "top_actions": dict(list(r["action_counts"].items())[:5]),
                "top_artifacts": dict(list(r["artifact_counts"].items())[:5]),
                "language": r["language"]["value"], "skill": r["skill"]["value"],
                "tooling": r["tooling"]["value"]})
        out.append({"pair": f"{a} x {b}", "n": len(wids), "windows": rows})
    return out


# --------------------------------------------------------------------- why, not just whether

def diffuseness(recs, dim, level_counts):
    """WHY a dimension is unattributed, as a distribution rather than a reason tally.

    A reason tally says `no_majority`; it does not say whether the top value missed the floor by a
    point or by half. Three things are needed to tell a floor problem from a shape problem:

      top_share percentiles   how far the modal value actually gets. If p50 sits just under 0.50
                              the floor is the obstacle; if it sits near 0.30 the work is
                              genuinely mixed and no floor recovers it.
      floor sweep             coverage at floors from 0.30 to 0.60. This is DIAGNOSTIC ONLY -- the
                              0.50 floor is the preregistered one and the verdict is taken there.
                              Lowering it is not on the table (window.MIN_EVIDENCE's derivation
                              takes 0.50 as the null hypothesis, so a different floor is a
                              different evidence floor too), but the shape of the curve says
                              whether the level is near-missing or nowhere near.
      values per window       how many distinct values hold the level at all. A level with one
                              value and five observations is dominated; a level with nine values
                              and forty is not, at any floor.
    """
    tops = sorted(r[dim]["share"] for r in recs)
    n = len(tops)
    def q(f):
        return round(tops[min(n - 1, int(f * n))], 4) if n else 0.0
    sweep = {}
    for fl in (0.30, 0.35, 0.40, 0.45, 0.50, 0.55, 0.60):
        ok = sum(1 for r in recs
                 if r[dim]["evidence"] >= MIN_EVIDENCE and r[dim]["share"] >= fl
                 and r[dim]["reason"] != "tie")
        sweep[f"{fl:.2f}"] = round(ok / len(recs), 4)
    nvals = sorted(len(r[level_counts]) for r in recs)
    m = len(nvals)
    top_values = collections.Counter(
        (max(r[level_counts].items(), key=lambda kv: (kv[1], kv[0]))[0]
         if r[level_counts] else "(none)") for r in recs)
    return {"top_share_p10": q(0.10), "top_share_p25": q(0.25), "top_share_p50": q(0.50),
            "top_share_p75": q(0.75), "top_share_p90": q(0.90),
            "coverage_by_floor": sweep,
            "values_per_window_p50": nvals[m // 2] if m else 0,
            "values_per_window_p90": nvals[min(m - 1, int(0.9 * m))] if m else 0,
            "values_per_window_max": nvals[-1] if m else 0,
            "modal_top_value": top_values.most_common(8),
            "distinct_values_seen": len({k for r in recs for k in r[level_counts]})}


def mi_null(pr, trials=2000, seed=20260824):
    """The permutation null for NMI, because MI is UPWARD-BIASED on a small sample with many cells.

    Q3's population is only the windows where BOTH halves are attributed, and MI's bias grows with
    the number of cells relative to n. A raw NMI of 0.16 over 126 windows and 6 cells could be
    entirely that bias, and the preregistered bar (>= 0.10) was written without a bias correction.
    So the same statistic is recomputed on `trials` random pairings of the SAME two marginals: any
    value the null reaches routinely is not evidence of a conjunction. Reported alongside the raw
    number, never in place of it -- the bar is applied as written, and this is the objection.

    Deterministic seed so the number in RESULTS.md is reproducible.
    """
    import random
    if not pr:
        return {"trials": 0}
    rng = random.Random(seed)
    xs = [a for a, _b, _w in pr]
    ys = [b for _a, b, _w in pr]
    obs = mutual_information(pr)["nmi"][NMI_PRIMARY]
    vals = []
    for _ in range(trials):
        sh = ys[:]
        rng.shuffle(sh)
        vals.append(mutual_information(list(zip(xs, sh, xs)))["nmi"][NMI_PRIMARY])
    vals.sort()
    ge = sum(1 for v in vals if v >= obs)
    return {"trials": trials, "observed": round(obs, 4),
            "null_p50": round(vals[trials // 2], 4),
            "null_p95": round(vals[int(0.95 * trials)], 4),
            "null_max": round(vals[-1], 4),
            "p_value": round((ge + 1) / (trials + 1), 4),
            "null_reaches_bar_rate": round(
                sum(1 for v in vals if v >= Q3_NMI_MIN) / trials, 4)}

# --------------------------------------------------------------------- verdicts

def verdicts(q1, q2, q3):
    v = {
        "Q1": {"bar": f"coverage >= {Q1_COVERAGE_MIN}", "got": q1["coverage"],
               "pass": q1["coverage"] >= Q1_COVERAGE_MIN},
        "Q2": {"bar": f"top pair < {Q2_TOP_PAIR_MAX} AND >= {Q2_MIN_PAIRS} pairs each >= "
                      f"{Q2_PAIR_SHARE_MIN}",
               "got": f"top={q2['top_share']} pairs_over_3pct={q2['pairs_over_min']}",
               "pass": q2["top_share"] < Q2_TOP_PAIR_MAX
                       and q2["pairs_over_min"] >= Q2_MIN_PAIRS},
        "Q3": {"bar": f"NMI({NMI_PRIMARY}) >= {Q3_NMI_MIN}", "got": q3["nmi"][NMI_PRIMARY],
               "pass": q3["nmi"][NMI_PRIMARY] >= Q3_NMI_MIN,
               "all_normalisations_agree": len({v >= Q3_NMI_MIN
                                                for v in q3["nmi"].values()}) == 1},
    }
    if v["Q1"]["pass"] and v["Q2"]["pass"] and v["Q3"]["pass"]:
        v["ships"] = "action AND the (act, artifact) pair as a routing key"
    elif v["Q1"]["pass"]:
        v["ships"] = "action alone, as an eighth ALLOCATION dimension. NOT the pair."
    else:
        v["ships"] = ("nothing. The densest-looking level in the vocabulary is not dense enough "
                      "to allocate on.")
    return v


def measure(out):
    recs = load_frame(out)
    res = {"windows": len(recs), "min_evidence": MIN_EVIDENCE, "floor": FLOOR,
           "span_minutes": SPAN, "stride_minutes": STRIDE}
    res["coverage"] = {name: coverage(recs, name)
                       for name, _l, _f in ALLOCATION + [("action", "action", FLOOR)]}
    pr = pairs(recs)
    res["pair_population"] = {
        "n": len(pr), "share_of_windows": round(len(pr) / len(recs), 4),
        "act_attributed": res["coverage"]["action"]["reasons"]["attributed"],
        "artifact_attributed": res["coverage"]["output_type"]["reasons"]["attributed"]}
    res["concentration"] = concentration(pr)
    res["mi"] = mutual_information(pr)
    res["diffuseness"] = {"action": diffuseness(recs, "action", "action_counts"),
                          "output_type": diffuseness(recs, "output_type", "artifact_counts")}
    res["mi_null"] = mi_null(pr)
    res["profiles"] = profiles(recs, pr)
    res["verdict"] = verdicts(res["coverage"]["action"], res["concentration"], res["mi"])
    with open(os.path.join(out, "act-artifact-results.json"), "w") as fh:
        json.dump(res, fh, indent=1)
    report(res)
    return res


def report(res):
    print(f"\nWINDOWS {res['windows']}  span={res['span_minutes']}m "
          f"floor={res['floor']} MIN_EVIDENCE={res['min_evidence']}")
    print("\nQ1 COVERAGE — attributed rate at 60 minutes, unattributed split by reason")
    print(f"  {'dimension':<12} {'cover':>7} {'attrib':>7} {'absent':>7} {'thin':>6} "
          f"{'tie':>5} {'nomaj':>6} {'values':>7} {'ev.med':>7}")
    for name, c in res["coverage"].items():
        r = c["reasons"]
        print(f"  {name:<12} {c['coverage']:>7.3f} {r['attributed']:>7} {r['absent']:>7} "
              f"{r['thin']:>6} {r['tie']:>5} {r['no_majority']:>6} "
              f"{c['distinct_values']:>7} {c['evidence_median']:>7}")
    p = res["pair_population"]
    print(f"\nPAIR POPULATION both halves attributed: {p['n']} of {res['windows']} "
          f"({p['share_of_windows']:.3f})  act={p['act_attributed']} "
          f"artifact={p['artifact_attributed']}")
    co = res["concentration"]
    print(f"\nQ2 CONCENTRATION distinct_pairs={co['distinct_pairs']} "
          f"top_share={co['top_share']} pairs>=3%={co['pairs_over_min']}")
    for r in co["ranked"][:15]:
        print(f"  {r['pair']:<34} {r['n']:>5} {r['share']:>7.3f}")
    mi = res["mi"]
    print(f"\nQ3 CONJUNCTION MI={mi['mi_bits']} bits  H(act)={mi['h_act']} "
          f"H(artifact)={mi['h_artifact']}")
    print(f"  NMI  arithmetic={mi['nmi']['arithmetic']}  geometric={mi['nmi']['geometric']}  "
          f"min={mi['nmi']['min']}  max={mi['nmi']['max']}")
    print(f"  Cramer's V={mi['cramers_v']} (chi2={mi['chi2']}, df={mi['df']})")
    print(f"  of {mi['mi_bits']} bits, {mi['mi_bits_from_cells_under_min_evidence']} "
          f"({100 * mi['mi_share_from_thin_cells']:.1f}%) comes from the "
          f"{mi['cells_under_min_evidence']} cells holding fewer than MIN_EVIDENCE windows")
    print("  cell lift vs independence (top by mass):")
    for c in mi["top_lift"]:
        print(f"    {c['pair']:<34} n={c['n']:>4} exp={c['expected']:>8} "
              f"lift={c['lift']:>6}")
    for dim, d in res["diffuseness"].items():
        print(f"\nWHY — `{dim}` top-share distribution and floor sweep (DIAGNOSTIC; the "
              f"verdict is at {FLOOR})")
        print(f"  top share p10={d['top_share_p10']} p25={d['top_share_p25']} "
              f"p50={d['top_share_p50']} p75={d['top_share_p75']} p90={d['top_share_p90']}")
        print(f"  coverage by floor: " + "  ".join(
            f"{k}->{v}" for k, v in d["coverage_by_floor"].items()))
        print(f"  distinct values per window: p50={d['values_per_window_p50']} "
              f"p90={d['values_per_window_p90']} max={d['values_per_window_max']} "
              f"(level has {d['distinct_values_seen']} values in all)")
        print(f"  modal top value: {d['modal_top_value']}")
    nl = res["mi_null"]
    print(f"\nQ3 PERMUTATION NULL ({nl['trials']} shuffles of the same marginals): "
          f"observed={nl['observed']} null p50={nl['null_p50']} p95={nl['null_p95']} "
          f"max={nl['null_max']} p={nl['p_value']}")
    print(f"  the null alone clears the {Q3_NMI_MIN} bar in "
          f"{100 * nl['null_reaches_bar_rate']:.1f}% of shuffles")
    print("\nWINDOWS PER PAIR — specific identifiers, with the profile that produced them")
    for g in res["profiles"]:
        print(f"  {g['pair']}  (n={g['n']})")
        for w in g["windows"]:
            print(f"    {w['wid']}  project={w['project']} volume={w['volume']}")
            print(f"      act={w['act']}  artifact={w['artifact']}")
            print(f"      actions={w['top_actions']}")
            print(f"      artifacts={w['top_artifacts']}")
            print(f"      language={w['language']} skill={w['skill']} "
                  f"tooling={w['tooling']}")
    print("\nVERDICT")
    for q in ("Q1", "Q2", "Q3"):
        v = res["verdict"][q]
        print(f"  {q}  {'PASS' if v['pass'] else 'FAIL'}  {v['bar']}  -> got {v['got']}")
    if not res["verdict"]["Q3"]["all_normalisations_agree"]:
        print("  !! the four NMI normalisations DISAGREE on the bar — see RESULTS.md")
    print(f"  SHIPS: {res['verdict']['ships']}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("cmd", choices=("frame", "measure"))
    ap.add_argument("--out", default=OUT)
    a = ap.parse_args()
    assert_vocab_fixed()
    if a.cmd == "frame":
        build(ROOTS, a.out)
    else:
        measure(a.out)


if __name__ == "__main__":
    main()
