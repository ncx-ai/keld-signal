#!/usr/bin/env python3
"""Does `window.MIN_EVIDENCE` have to change when the window gets shorter?

`/analyze` is gaining DYNAMICS: a recent SLICE read against a longer BASELINE. The slice is
minutes, not an hour, and the plan flagged the obvious hazard — `MIN_EVIDENCE = 5` was derived
against a 60-minute window, so carrying it over unchanged might mark nearly every slice
unattributed, and an attribution rate that is an artefact of a floor is exactly the plausible
wrong number this project keeps paying for.

This script is the measurement that decides it. It does NOT decide the derivation — that is an
argument, and it is written up in `sidecar/app/analysis/window.py` beside the constant. What this
answers is the empirical half: at 5, 10, 15, 30 and 60 minutes, WHAT DOES THE FLOOR COST, per
allocation dimension.

WHY PER DIMENSION, NEVER AN AGGREGATE
-------------------------------------
Roughly twenty defects in this study surfaced as plausible wrong numbers and essentially none was
caught by reading an aggregate. `project` and `branch` carry tens of observations an hour;
`workflow` (skill) and `output_type` (artifact) are sparse. A single "attribution rate" averages a
dimension that is fine with one that is empty, and reports a number that is true of neither. So
every table below is keyed on (slice_minutes, dimension), and the reason a slot is unattributed is
broken out — `thin` (fewer than the floor), `no_majority` (enough evidence, no winner), `tie`,
`absent` (no evidence at that level at all). Those are four different facts and a floor only moves
one of them, and they are `window.REASONS` — this script calls the SHIPPED `window.attribution`,
so what is measured here is what /analyze would answer, precedence and all.

WHAT IS COMPARED
----------------
For every (anchor prompt, slice length) the rollup is computed ONCE and then asked the dominance
question at each candidate floor in `CANDIDATE_FLOORS`. `min_evidence=1` is the counterfactual
"share floor only" — no evidence floor at all — and is what "how many slots the floor costs" is
measured against. The intermediate floors 2/3/4 are what a duration-scaled floor would actually
select (a sixth of an hour, rounded up, is 1), so their cost AND their null-hypothesis price are
both visible in the same table: unanimity under a fair coin has probability 0.5**n, so a floor of
n admits a fabricated attribution with that probability.

METHOD
------
The production path, not a re-implementation: the reference store answers each window
(`Store.window_rows` with the reconcile slot excluded) and `reconcile(pending_in(...))` re-scopes
the prose-path rows to the window, exactly as `analyze._rollup_from_store` does. The rollup and
the dominance test are the shipped functions.

The anchor of every slice is a real prompt's own timestamp and the slice looks BACK from it
(`[t - L, t)`), because that is what `analyze_window` does and the question is about what
`/analyze` would report.

CORPUS. `~/keld/refseries-context/frozen-corpus`, restricted to transcripts whose 8-character
filename prefix is UNIQUE. That is not a convenience: `ingest.session_of` keys the store on
`basename[:8]`, and the corpus's 445 `agent-*.jsonl` subagent transcripts collide onto 16 such
keys, so ingesting them into one store would merge unrelated sessions' evidence and inflate every
count measured here. The 55 surviving transcripts are 542 of the corpus's 731 MB (74%) and are the
ones whose session id `/analyze` can actually address.

PRIVACY. Reads real transcripts; writes only counts, and the durable output lives outside the
repo. No prompt text, no ref values, no session ids leave this process — the rendered tables are
(slice, dimension, reason, count) and nothing else.

    ~/.keld/sidecar-venv/bin/python scripts/evidence_floor.py ingest
    ~/.keld/sidecar-venv/bin/python scripts/evidence_floor.py measure
    ~/.keld/sidecar-venv/bin/python scripts/evidence_floor.py render
"""
import argparse
import collections
import json
import math
import os
import random
import statistics
import sys
import time
from datetime import datetime, timedelta

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))
from app.analysis import COMPONENT_DEPTH                          # noqa: E402
from app.analysis.ingest import RECONCILE_SLOT, ingest_file, pending_in, session_of  # noqa: E402
from app.analysis.levels import quantize                          # noqa: E402
from app.analysis.reconcile import reconcile                      # noqa: E402
from app.analysis.store import open_store                         # noqa: E402
from app.analysis.window import REASONS, attribution, rollup      # noqa: E402
from app.analysis.workstreams import ALLOCATION                   # noqa: E402

# --- PRE-REGISTERED, fixed before any result was looked at --------------------------------
SLICE_MINUTES = (5, 10, 15, 30, 60)
CANDIDATE_FLOORS = (1, 2, 3, 4, 5)   # 1 == share floor only, the counterfactual
SAMPLE_N = 4000                      # anchor prompts, seeded; 5 slices each = 20k windows
SEED = 20260824
CORPUS = os.path.expanduser("~/keld/refseries-context/frozen-corpus")
ROOTS = ("projects", "john-projects")
OUT = os.path.expanduser("~/keld/refseries-context/dynamics")
DB = os.path.join(OUT, "frozen-corpus.db")
# -----------------------------------------------------------------------------------------


def transcripts():
    """Every corpus transcript whose `session_of` key is unique, in a stable order."""
    out = []
    for r in ROOTS:
        for dirpath, _, names in os.walk(os.path.join(CORPUS, r)):
            for n in names:
                if n.endswith(".jsonl"):
                    out.append(os.path.join(dirpath, n))
    seen = collections.Counter(session_of(p) for p in out)
    return sorted(p for p in out if seen[session_of(p)] == 1)


def do_ingest(args):
    os.makedirs(OUT, exist_ok=True)
    files = transcripts()
    st = open_store(DB)
    t0, mb = time.time(), 0.0
    for i, p in enumerate(files, 1):
        mb += os.path.getsize(p) / 1e6
        r = ingest_file(st, p, None)                 # nlp=None: `term` is inventory, not allocation
        print(f"  [{i}/{len(files)}] {session_of(p)} {r.new_lines} lines "
              f"({mb:.0f} MB, {time.time()-t0:.0f}s)", flush=True)
    print(f"ingested {len(files)} transcripts, {mb:.0f} MB in {time.time()-t0:.0f}s -> {DB}")


def anchors(st, files):
    """Every (session, path, prompt_id, ts) the store's prompt index holds, stable order."""
    by_session = {session_of(p): p for p in files}
    c = st._conn()
    out = []
    for session, pid, ts in c.execute("SELECT session, prompt_id, ts FROM prompt "
                                      "ORDER BY session, ts, prompt_id"):
        p = by_session.get(session)
        if p is not None:
            out.append((session, p, pid, ts))
    return out


def slice_rollup(st, session, path, end_iso, minutes):
    """The production window query, at an arbitrary span. Mirrors `_rollup_from_store`."""
    end = datetime.fromisoformat(end_iso.replace("Z", "+00:00"))
    start = end - timedelta(minutes=minutes)
    lo, hi = quantize(start.timestamp()), quantize(end.timestamp())
    rows = st.window_rows(session, lo, hi, exclude_slots=(RECONCILE_SLOT,))
    recon, _ = reconcile(pending_in(st, path, lo, hi), COMPONENT_DEPTH)
    return rollup(rows + recon)


def do_measure(args):
    os.makedirs(OUT, exist_ok=True)
    st = open_store(DB)
    files = transcripts()
    frame = anchors(st, files)
    print(f"frame: {len(frame)} anchor prompts over {len(files)} transcripts", flush=True)
    n = min(SAMPLE_N, len(frame))
    sample = random.Random(SEED).sample(frame, n)
    sample.sort()
    print(f"sample: {n} anchors (SAMPLE_N={SAMPLE_N}, seed={SEED})", flush=True)

    # (minutes, dim, floor) -> Counter of reason;  (minutes, dim) -> list of evidence totals
    reasons = collections.defaultdict(collections.Counter)
    evidence = collections.defaultdict(list)
    windows = 0
    t0 = time.time()
    for k, (session, path, pid, ts) in enumerate(sample, 1):
        for m in SLICE_MINUTES:
            try:
                rl = slice_rollup(st, session, path, ts, m)
            except Exception as e:                       # a transcript that will not re-scope
                print(f"  skip {session}#{pid[:8]}@{m}m: {type(e).__name__}", flush=True)
                continue
            windows += 1
            for dim, level, share_floor in ALLOCATION:
                items = rl.get(level) or []
                evidence[(m, dim)].append(int(sum(x for _, x in items)))
                for f in CANDIDATE_FLOORS:
                    reasons[(m, dim, f)][attribution(rl, level, share_floor, f).reason] += 1
        if k % 250 == 0:
            print(f"  {k}/{n} anchors, {windows} windows ({time.time()-t0:.0f}s)", flush=True)

    rows = []
    for (m, dim, f), c in sorted(reasons.items()):
        tot = sum(c.values())
        ev = evidence[(m, dim)]
        rows.append({
            "slice_minutes": m, "dimension": dim, "min_evidence": f,
            "null_unanimity_p": round(0.5 ** f, 4),
            "slots": tot,
            "attributed": c["attributed"], "thin": c["thin"],
            "no_majority": c["no_majority"], "tie": c["tie"], "absent": c["absent"],
            "attribution_rate": round(c["attributed"] / tot, 4) if tot else 0.0,
            "evidence_median": round(statistics.median(ev), 1) if ev else 0.0,
            "evidence_mean": round(statistics.fmean(ev), 2) if ev else 0.0,
            "evidence_ge_5_rate": round(sum(1 for x in ev if x >= 5) / len(ev), 4) if ev else 0.0,
        })
    # Cost of the shipped floor against the no-floor counterfactual, per (slice, dimension).
    base = {(r["slice_minutes"], r["dimension"]): r for r in rows if r["min_evidence"] == 1}
    for r in rows:
        b = base[(r["slice_minutes"], r["dimension"])]
        r["lost_vs_no_floor"] = b["attributed"] - r["attributed"]
        r["lost_vs_no_floor_rate"] = (round(r["lost_vs_no_floor"] / b["attributed"], 4)
                                      if b["attributed"] else 0.0)

    stats = {"sample_n": SAMPLE_N, "seed": SEED, "corpus": CORPUS,
             "transcripts": len(files), "frame_anchors": len(frame), "sampled_anchors": n,
             "windows": windows, "slice_minutes": list(SLICE_MINUTES),
             "candidate_floors": list(CANDIDATE_FLOORS),
             "dimensions": [d for d, _, _ in ALLOCATION],
             "elapsed_s": round(time.time() - t0, 1),
             "ms_per_window": round((time.time() - t0) * 1000 / windows, 3) if windows else 0.0}
    with open(os.path.join(OUT, "evidence-floor-stats.json"), "w") as f:
        json.dump(stats, f, indent=2)
    with open(os.path.join(OUT, "evidence-floor.jsonl"), "w") as f:
        for r in rows:
            f.write(json.dumps(r) + "\n")
    print(json.dumps(stats, indent=2))
    print(f"\n{len(rows)} (slice x dimension x floor) rows -> {OUT}/evidence-floor.jsonl")
    do_render(args)


def do_render(args):
    rows = [json.loads(l) for l in open(os.path.join(OUT, "evidence-floor.jsonl"))]
    dims = [d for d, _, _ in ALLOCATION]
    shipped = max(CANDIDATE_FLOORS)

    print(f"\n## Attribution rate at the SHIPPED floor (min_evidence={shipped}), per dimension\n")
    hdr = f"{'dimension':12}" + "".join(f"{m:>8}m" for m in SLICE_MINUTES)
    print(hdr)
    for d in dims:
        line = f"{d:12}"
        for m in SLICE_MINUTES:
            r = next(x for x in rows if x["slice_minutes"] == m and x["dimension"] == d
                     and x["min_evidence"] == shipped)
            line += f"{r['attribution_rate']:8.1%} "
        print(line)

    print(f"\n## Slots the floor COSTS vs no floor (min_evidence=1), per dimension\n")
    print(hdr)
    for d in dims:
        line = f"{d:12}"
        for m in SLICE_MINUTES:
            r = next(x for x in rows if x["slice_minutes"] == m and x["dimension"] == d
                     and x["min_evidence"] == shipped)
            line += f"{r['lost_vs_no_floor']:5d}/{r['lost_vs_no_floor_rate']:.0%}".rjust(9)
        print(line)

    print("\n## WHY a slot is unattributed at the shipped floor (share of slots)\n")
    print(f"{'slice':>6} {'dimension':12} {'attrib':>8}{'thin':>8}{'no_maj':>8}{'tie':>8}{'absent':>8}"
          f"{'ev_med':>8}")
    for m in SLICE_MINUTES:
        for d in dims:
            r = next(x for x in rows if x["slice_minutes"] == m and x["dimension"] == d
                     and x["min_evidence"] == shipped)
            t = r["slots"]
            print(f"{m:5d}m {d:12} {r['attributed']/t:8.1%}{r['thin']/t:8.1%}"
                  f"{r['no_majority']/t:8.1%}{r['tie']/t:8.1%}{r['absent']/t:8.1%}"
                  f"{r['evidence_median']:8.1f}")

    print("\n## What a LOWER floor would buy, and what it would cost in false attribution\n")
    print(f"{'floor':>5} {'P(unanimous|p=0.5)':>19} " + "".join(f"{m:>7}m" for m in SLICE_MINUTES)
          + "   (attribution rate, all dimensions pooled -- see caveat)")
    for f in CANDIDATE_FLOORS:
        line = f"{f:5d} {0.5**f:19.4f} "
        for m in SLICE_MINUTES:
            sub = [x for x in rows if x["slice_minutes"] == m and x["min_evidence"] == f]
            a = sum(x["attributed"] for x in sub)
            t = sum(x["slots"] for x in sub)
            line += f"{a/t:7.1%} "
        print(line)
    print("  CAVEAT: this last table is the one pooled number in the report, and it is here only"
          "\n  to price the floor choice itself. Every conclusion above is per dimension.")


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("cmd", choices=("ingest", "measure", "render"))
    a = ap.parse_args()
    {"ingest": do_ingest, "measure": do_measure, "render": do_render}[a.cmd](a)
