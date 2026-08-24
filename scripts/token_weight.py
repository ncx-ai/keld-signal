#!/usr/bin/env python3
"""Is the published `share` computed against the wrong denominator?

Workstream attribution publishes `share` and `evidence` from EVENT COUNTS — how many tool calls
touched a thing. The economic weight of a window is TOKENS, and it is already in every transcript
line the parse decodes. This script measures whether that distinction changes the answer, because
"the right denominator is sitting there unused" is an argument, not evidence: if the two agree
almost always, the signal is not worth publishing and this script's job is to say so.

WHAT IS COMPARED
----------------
For each window, two rollups over exactly the same rows:

  event-weighted   `window.rollup` — SUM(n) per (level, ref). What ships today.
  token-weighted   SUM(n * turn_weight) per (level, ref), where `turn_weight` is
                   `magnitude.token_weight` of that turn's `message.usage` — a price-weighted
                   token count in input-token equivalents (see app/analysis/magnitude.py).

Then the SHIPPED dominance test (`window.attribution`, floor 0.50, MIN_EVIDENCE 5) is asked of
each, per allocation dimension, and the headline number is HOW OFTEN THE DOMINANT VALUE CHANGES.
Not how often the shares differ — shares differ constantly and by amounts nobody consumes. What a
cost report shows is the winning value, so a divergence that never moves the winner is not a
divergence anyone would see.

Reported per dimension, never pooled: `project` and `branch` carry tens of observations an hour
while `workflow` and `tooling` are mostly empty, and one pooled rate would describe neither. Same
rule, same reason, as scripts/evidence_floor.py.

WHY THE COMPARISON IS FAIR
--------------------------
The token-weighted rollup REDUCES to the event-weighted one when every turn weighs the same (up to
the common factor) — asserted in app/test_magnitude.py. So it is a strict generalisation, and the
two can only disagree where turns genuinely differ in cost. That makes the divergence rate a
measure of real cost skew rather than of two arbitrary metrics drifting.

SIGNAL 2, DIFF MAGNITUDE
------------------------
`edit >= 5` was the useless predictor that sank `transform` in the activity studies. So the test
for byte magnitude is not "is it distributed" — of course it is — but whether it separates windows
the COUNT cannot: hold the edit count fixed and see whether the byte totals still span orders of
magnitude. If they collapse, the byte measure is the count in different units.

METHOD, AND THE TWO CLAIMS IT CHECKS
------------------------------------
Each transcript is parsed ONCE and every window is a bisect slice of its rows (the pattern from
`study(series): parse each transcript once, slice by bisect`), because re-parsing per window is
the O(file x windows) cost that made earlier passes take hours.

The rollups are the SHIPPED functions, and a sample of windows is additionally cross-checked
against `Store.weighted_rollup_window` — the SQL that will actually answer them — so this
measurement describes what ships rather than a second arithmetic that happens to agree.

It also verifies the two claims the whole design rests on, rather than assuming them:

  no tool_result exposure   every magnitude comes from a line `transcript.turns_in` yields, and
                            that filter skips `tool_result` before `json.loads` runs. Measured:
                            the count and BYTE VOLUME of tool_result lines never decoded.
  no ingest-cost regression the marginal parse cost of both magnitudes, measured against the same
                            parse with the magnitude calls stubbed to zero.

PRIVACY. Reads real transcripts. Writes counts, byte lengths, token counts and ratios — never a
`ref` value, never a session id, never a byte of message or file text. Durable output lives
outside the repo, in ~/keld/refseries-context/token-weight/.

    ~/.keld/sidecar-venv/bin/python scripts/token_weight.py measure
    ~/.keld/sidecar-venv/bin/python scripts/token_weight.py render
"""
import argparse
import bisect
import collections
import json
import os
import statistics
import sys
import tempfile
import time
from datetime import datetime, timedelta

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))
from app.analysis import COMPONENT_DEPTH, magnitude, window, workstreams   # noqa: E402
from app.analysis.levels import events_for_turns, quantize                 # noqa: E402
from app.analysis.reconcile import reconcile                              # noqa: E402
from app.analysis.store import open_store                                 # noqa: E402
from app.analysis.transcript import _order_key, turns_in                  # noqa: E402
from app.analysis.workspace import new_evidence, scan_tool_use            # noqa: E402
from app.analysis.transcript import tool_use_in                           # noqa: E402

CORPUS = os.path.expanduser("~/keld/refseries-context/frozen-corpus")
OUTDIR = os.path.expanduser("~/keld/refseries-context/token-weight")
SPAN_MINUTES = 60
# One anchor per user prompt, capped per transcript so one enormous session cannot dominate the
# population. 40 x 500 transcripts is the same order as evidence_floor.py's 20,000 windows.
MAX_ANCHORS = 40
# Windows cross-checked against the shipped SQL. A sample, not all of them: the point is that the
# two implementations agree, and that is a property of the code, not of the corpus.
SQL_CHECK_WINDOWS = 200

DIMENSIONS = [(name, level) for name, level, _floor in workstreams.ALLOCATION]


# --- parsing one transcript ------------------------------------------------------------------

def _parse(path):
    """One transcript -> (ref_rows sorted by ts, per-turn magnitudes, anchors, line stats).

    `evidence` is accumulated from the same lines rather than by a second whole-file pre-pass,
    and `reconcile` runs once over the whole file's `pending` — this is a study of relative
    magnitudes across windows, and re-scoping reconcile per window (what /analyze does) would
    cost O(windows) for rows that do not move the comparison either way.
    """
    stats = collections.Counter()
    raw = []
    with open(path, errors="replace") as fh:
        for line in fh:
            stats["lines"] += 1
            stats["bytes"] += len(line)
            # Exactly `turns_in`'s first two rules, counted rather than assumed. A tool_result
            # line without a tool_use block is never decoded, and its bytes are never read as
            # anything but a length -- which is the "no exposure to file contents" claim.
            if '"type":"user"' not in line and '"type":"assistant"' not in line:
                continue
            if '"tool_result"' in line and '"tool_use"' not in line:
                stats["tool_result_lines_skipped"] += 1
                stats["tool_result_bytes_skipped"] += len(line)
                continue
            raw.append(line)

    turns = list(turns_in(raw))
    if not turns:
        return None
    turns.sort(key=lambda o: _order_key(o["timestamp"]))
    root = os.path.dirname(os.path.dirname(path))
    evidence = new_evidence()
    scan_tool_use(tool_use_in(raw), into=evidence)
    rows, pending, _n = events_for_turns(turns, path, root, (), None, evidence=evidence)
    recon, _s = reconcile(pending, COMPONENT_DEPTH)

    refs = sorted((r for r in rows + recon if r[5] == "ref"), key=lambda r: r[0])
    weight = collections.defaultdict(float)      # ts -> ROLLUP WEIGHT (request cost, per line)
    spend = collections.defaultdict(float)       # ts -> SPEND (request cost, once per request)
    ebytes = collections.defaultdict(float)      # ts -> edit bytes
    per_edit = []                                # every edit event's own magnitude
    for r in rows:
        if r[5] != "mag":
            continue
        if r[6] == magnitude.TOKENS:
            weight[r[0]] += r[8]
        elif r[6] == magnitude.REQUEST_TOKENS:
            spend[r[0]] += r[8]
        elif r[6] == magnitude.EDIT_BYTES:
            ebytes[r[0]] += r[8]
            per_edit.append(r[8])

    anchors = [o["timestamp"] for o in turns if o.get("type") == "user"]
    if len(anchors) > MAX_ANCHORS:
        step = len(anchors) / float(MAX_ANCHORS)
        anchors = [anchors[int(i * step)] for i in range(MAX_ANCHORS)]
    return refs, weight, spend, ebytes, per_edit, anchors, stats


def _slice(refs, keys, lo, hi):
    return refs[bisect.bisect_left(keys, lo):bisect.bisect_left(keys, hi)]


def _weighted_rollup(rows, weight):
    """The in-memory twin of `Store.weighted_rollup_window`: SUM(n * turn_weight) per
    (level, ref), handed to `window.rollup` so the ordering rule is not reimplemented either.
    A turn with no weight contributes nothing, matching the store's INNER join."""
    out = []
    for r in rows:
        w = weight.get(r[0])
        if w:
            out.append(r[:8] + (r[8] * w,))
    return window.rollup(out)


# --- the measurement -------------------------------------------------------------------------

def measure(limit=None):
    files = sorted(f for f in
                   (os.path.join(d, n) for d, _, ns in os.walk(CORPUS) for n in ns)
                   if f.endswith(".jsonl"))
    if limit:
        files = files[:limit]

    # (dimension) -> Counter of outcome. Outcomes are exhaustive and mutually exclusive.
    verdict = {name: collections.Counter() for name, _ in DIMENSIONS}
    verdict_gated = {name: collections.Counter() for name, _ in DIMENSIONS}
    skew = []                    # per window: max turn weight / median turn weight
    win_tokens = []              # per window: total token weight
    per_edit_all = []            # every edit event's byte magnitude
    by_edit_count = collections.defaultdict(list)   # edit count -> [window edit bytes]
    lines = collections.Counter()
    n_windows = 0
    sql_checks = sql_ok = 0
    t_parse = 0.0

    with tempfile.TemporaryDirectory() as tmp:
        store = open_store(os.path.join(tmp, "check.db"))
        for i, path in enumerate(files):
            t0 = time.perf_counter()
            got = _parse(path)
            t_parse += time.perf_counter() - t0
            if got is None:
                continue
            refs, weight, spend, ebytes, per_edit, anchors, stats = got
            lines.update(stats)
            per_edit_all += per_edit
            keys = [r[0] for r in refs]

            session = f"s{i:04d}"
            if sql_checks < SQL_CHECK_WINDOWS:
                store.clear_session(session)
                store.upsert_events(session, refs, source_line=1)
                store.upsert_events(session, [
                    (ts, session, None, None, False, "mag", magnitude.TOKENS, "", v)
                    for ts, v in weight.items()], source_line=1)

            for a in anchors:
                end = _order_key(a)
                start = end - timedelta(minutes=SPAN_MINUTES)
                lo, hi = quantize(start.timestamp()), quantize(end.timestamp())
                rows = _slice(refs, keys, lo, hi)
                if not rows:
                    continue
                n_windows += 1
                plain = window.rollup(rows)
                weighted = _weighted_rollup(rows, weight)

                if sql_checks < SQL_CHECK_WINDOWS:
                    sql = store.weighted_rollup_window(session, lo, hi, kind=magnitude.TOKENS)
                    sql_checks += 1
                    # Exact on KEYS, relative-tolerant on VALUES: SQLite and Python sum the same
                    # terms in different orders, and at 1e9-magnitude weights the last bits
                    # legitimately differ. A key that exists in one and not the other is a real
                    # disagreement and must not be tolerated -- that is how the zero-magnitude
                    # defect above was found.
                    if _agree(sql, weighted):
                        sql_ok += 1

                # SPEND, not the rollup weight: the skew and the window total are questions
                # about what the hour cost, and `tokens` carries a request's cost on every line
                # of that request, so summing it would multiply requests by their line count.
                ws = [v for t, v in spend.items() if lo <= t < hi]
                if ws:
                    win_tokens.append(sum(ws))
                    med = statistics.median(ws)
                    if med > 0:
                        skew.append(max(ws) / med)

                # The COUNT the byte magnitude has to beat is the count of edit EVENTS — the
                # `tool` refs the bytes came from — not the `action` level, which also counts
                # shell heredoc writes that carry no measurable payload.
                eb = sum(v for t, v in ebytes.items() if lo <= t < hi)
                ecount = sum(n for k, n in (plain.get("tool") or [])
                             if k in ("Edit", "Write", "MultiEdit", "NotebookEdit"))
                by_edit_count[int(ecount)].append(eb)

                for name, level in DIMENSIONS:
                    a_plain = window.attribution(plain, level)
                    # NAIVE: the shipped dominance test applied to each rollup as-is. Reported
                    # because it is what a careless implementation would do, and its
                    # `only token-weighted` column is an ARTEFACT, not a finding -- see below.
                    _tally(verdict[name], a_plain.value,
                           window.attribution(weighted, level).value)
                    # GATED: the evidence floor comes from the EVENT COUNT for both, and only
                    # the share/winner comes from the weight. `window.MIN_EVIDENCE` is a
                    # threshold on a COUNT of observations -- its whole derivation is "could this
                    # unanimity have come from a coin, given how many times it was flipped" -- and
                    # a weighted total is a token sum in the millions, which clears a floor of 5
                    # unconditionally. Applying it to a weighted total does not relax the floor,
                    # it DELETES it, and every window it appears to newly attribute is a window
                    # with too little evidence to attribute. So this is the only comparison that
                    # isolates the effect of the denominator.
                    gated = None
                    if a_plain.reason != "absent" and a_plain.evidence >= window.MIN_EVIDENCE:
                        gated = window.attribution(weighted, level, min_evidence=1).value
                    _tally(verdict_gated[name], a_plain.value, gated)
        store.close()

    cost = _ingest_cost(files[:40])

    out = {
        "corpus": {"transcripts": len(files), "windows": n_windows,
                   "span_minutes": SPAN_MINUTES,
                   "parse_seconds": round(t_parse, 2)},
        "lines": dict(lines),
        "sql_crosscheck": {"windows": sql_checks, "agreed": sql_ok},
        "verdict": {k: dict(v) for k, v in verdict.items()},
        "verdict_gated": {k: dict(v) for k, v in verdict_gated.items()},
        "skew": _dist(skew),
        "window_token_weight": _dist(win_tokens),
        "edit_event_bytes": _dist(per_edit_all),
        "edit_bytes_by_edit_count": {
            str(k): _dist(v) for k, v in sorted(by_edit_count.items()) if len(v) >= 30},
        "ingest_cost": cost,
        "weight_definition": {
            "units": "input-token equivalents",
            "cache_read": magnitude.CACHE_READ, "cache_write_5m": magnitude.CACHE_WRITE_5M,
            "cache_write_1h": magnitude.CACHE_WRITE_1H, "output": magnitude.OUTPUT,
            "batch_tier": magnitude.TIER.get("batch")},
    }
    os.makedirs(OUTDIR, exist_ok=True)
    with open(os.path.join(OUTDIR, "measurement.json"), "w") as fh:
        json.dump(out, fh, indent=1, sort_keys=True)
    print(json.dumps(out["corpus"], indent=1))
    return out


def _tally(counter, plain_value, weighted_value):
    if plain_value is None and weighted_value is None:
        counter["neither attributed"] += 1
    elif plain_value is None:
        counter["only token-weighted attributed"] += 1
    elif weighted_value is None:
        counter["only event-weighted attributed"] += 1
    elif plain_value == weighted_value:
        counter["same dominant value"] += 1
    else:
        counter["DOMINANT VALUE CHANGED"] += 1


def _agree(a, b, rel=1e-9):
    if set(a) != set(b):
        return False
    for lv in a:
        xs, ys = dict(a[lv]), dict(b[lv])
        if set(xs) != set(ys):
            return False
        for k in xs:
            if abs(xs[k] - ys[k]) > rel * max(abs(xs[k]), abs(ys[k]), 1.0):
                return False
    return True


def _dist(xs):
    if not xs:
        return {"n": 0}
    s = sorted(xs)
    def p(q):
        return s[min(len(s) - 1, int(q * len(s)))]
    return {"n": len(s), "min": round(s[0], 1), "p10": round(p(0.10), 1),
            "median": round(p(0.50), 1), "p90": round(p(0.90), 1),
            "p99": round(p(0.99), 1), "max": round(s[-1], 1),
            "mean": round(sum(s) / len(s), 1), "total": round(sum(s), 1)}


def _ingest_cost(files):
    """The marginal parse cost of both magnitudes: the same parse, with the two magnitude calls
    stubbed to return zero. Measures what the feature ADDED, not what a parse costs."""
    def run():
        t0 = time.perf_counter()
        for p in files:
            _parse(p)
        return time.perf_counter() - t0

    with_mag = min(run() for _ in range(2))
    real_tw, real_eb = magnitude.token_weight, magnitude.edit_bytes
    magnitude.token_weight = lambda u: 0.0
    magnitude.edit_bytes = lambda n, i: 0
    try:
        without = min(run() for _ in range(2))
    finally:
        magnitude.token_weight, magnitude.edit_bytes = real_tw, real_eb
    return {"transcripts": len(files), "with_magnitudes_s": round(with_mag, 3),
            "magnitudes_stubbed_s": round(without, 3),
            "marginal_pct": round(100.0 * (with_mag - without) / without, 2) if without else None}


# --- rendering -------------------------------------------------------------------------------

class _Tee:
    """Render to stdout AND to the durable output dir, so the tables a report quotes are on disk
    beside the JSON they came from rather than only in a terminal that scrolled away."""

    def __init__(self, path):
        self.fh = open(path, "w")

    def __call__(self, *a):
        print(*a)
        print(*a, file=self.fh)


def render():
    with open(os.path.join(OUTDIR, "measurement.json")) as fh:
        m = json.load(fh)
    print_ = _Tee(os.path.join(OUTDIR, "TOKEN-WEIGHT.txt"))
    c = m["corpus"]
    print_(f"\nCORPUS  {c['transcripts']} transcripts, {c['windows']} windows of "
          f"{c['span_minutes']}m, parsed in {c['parse_seconds']}s\n")

    ln = m["lines"]
    print_("CLAIM 1 — no tool_result is ever decoded")
    print_(f"  transcript lines                 {ln.get('lines', 0):>12,}")
    print_(f"  tool_result lines never decoded  {ln.get('tool_result_lines_skipped', 0):>12,}")
    print_(f"  bytes never decoded              "
          f"{ln.get('tool_result_bytes_skipped', 0) / 1e6:>12.1f} MB")
    ic = m["ingest_cost"]
    print_("\nCLAIM 2 — no ingest-cost regression")
    print_(f"  {ic['transcripts']} transcripts: {ic['with_magnitudes_s']}s with magnitudes vs "
          f"{ic['magnitudes_stubbed_s']}s stubbed  ->  {ic['marginal_pct']:+.2f}%")

    x = m["sql_crosscheck"]
    print_(f"\nSHIPPED SQL agrees with the study's arithmetic on "
          f"{x['agreed']}/{x['windows']} sampled windows")

    def _table(key, title, note):
        print_(f"\n{title}\n  {note}")
        print_(f"  {'dimension':<12} {'compared':>9} {'changed':>9} {'rate':>7}   "
              f"{'only-ev':>8} {'only-tok':>9} {'neither':>8}")
        tot_cmp = tot_chg = 0
        for name, _ in DIMENSIONS:
            v = m[key][name]
            same = v.get("same dominant value", 0)
            chg = v.get("DOMINANT VALUE CHANGED", 0)
            cmp_ = same + chg
            tot_cmp += cmp_; tot_chg += chg
            rate = (100.0 * chg / cmp_) if cmp_ else 0.0
            print_(f"  {name:<12} {cmp_:>9,} {chg:>9,} {rate:>6.1f}%   "
                  f"{v.get('only event-weighted attributed', 0):>8,} "
                  f"{v.get('only token-weighted attributed', 0):>9,} "
                  f"{v.get('neither attributed', 0):>8,}")
        print_(f"  {'(all seven)':<12} {tot_cmp:>9,} {tot_chg:>9,} "
              f"{(100.0 * tot_chg / tot_cmp if tot_cmp else 0):>6.2f}%")

    print_("\nSIGNAL 1 — DOES THE TOKEN-WEIGHTED SHARE MOVE THE PUBLISHED ANSWER?")
    _table("verdict_gated", "  [A] THE HONEST COMPARISON",
           "evidence floor from the EVENT COUNT for both; only the winner comes from the weight")
    _table("verdict", "  [B] THE NAIVE COMPARISON (for contrast only)",
           "MIN_EVIDENCE applied to a weighted total -- a count floor of 5 against a token sum "
           "in\n  the millions, which DELETES the floor rather than relaxing it. Its 'only-tok' "
           "column\n  is that artefact, not a finding.")

    s = m["skew"]
    print_(f"\n  WHY: per-window turn-cost skew (max/median turn weight) — median {s['median']}x, "
          f"p90 {s['p90']}x, p99 {s['p99']}x, max {s['max']}x")
    w = m["window_token_weight"]
    print_(f"  window token weight (input-token equivalents): median {w['median']:,.0f}, "
          f"p90 {w['p90']:,.0f}, max {w['max']:,.0f}")

    print_("\nSIGNAL 2 — DIFF MAGNITUDE")
    e = m["edit_event_bytes"]
    print_(f"  per edit event, bytes: n={e['n']:,}  min {e['min']:.0f}  p10 {e['p10']:.0f}  "
          f"median {e['median']:.0f}  p90 {e['p90']:.0f}  p99 {e['p99']:.0f}  "
          f"max {e['max']:,.0f}  total {e['total']/1e6:.1f} MB")
    print_("\n  Does it separate windows the COUNT cannot? Hold the edit count fixed:")
    print_(f"  {'edits':>6} {'windows':>8} {'p10 B':>9} {'median B':>10} {'p90 B':>10} "
          f"{'max B':>10} {'p90/p10':>8}")
    for k, d in sorted(m["edit_bytes_by_edit_count"].items(), key=lambda kv: int(kv[0])):
        ratio = (d["p90"] / d["p10"]) if d["p10"] else float("inf")
        print_(f"  {k:>6} {d['n']:>8,} {d['p10']:>9,.0f} {d['median']:>10,.0f} "
              f"{d['p90']:>10,.0f} {d['max']:>10,.0f} "
              f"{('inf' if ratio == float('inf') else f'{ratio:.0f}x'):>8}")
    print_()


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    mm = sub.add_parser("measure"); mm.add_argument("--limit", type=int, default=None)
    sub.add_parser("render")
    a = ap.parse_args()
    if a.cmd == "measure":
        measure(a.limit)
        render()
    else:
        render()
