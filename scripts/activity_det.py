#!/usr/bin/env python3
"""Score the deterministic `activity_type` mapping (app/analysis/activity.py) on real windows.

Pre-registration: ~/keld/refseries-context/facets/DETERMINISTIC-PREREGISTRATION.md. The mapping
itself was committed BEFORE this script existed (git 4a64c9b) — the ordering is the only thing
that makes the number falsifiable, so it is provable from history rather than asserted here.

## Why hand-labelled WINDOWS and not the gold set

`gold.jsonl` is text-only: `{text, task_type, domain, activity, ...}` with no transcript
coordinates, so no reference level can be computed for the 103 rows carrying a human `activity`
label. The model's 0.670 is therefore not directly reachable — a property of the fixture, not a
choice made here. Of the three options:

  (a) hand-label real windows from the frozen corpus     <- CHOSEN
  (b) score against prose-activity's action-derived truth — CIRCULAR. That truth is a precedence
      rollup of the `action` level, which is the ONE level this mapping reads. It would be scoring
      the mapping against a coarser version of itself and would return a near-perfect number that
      means nothing.
  (c) —

(a) is chosen because it is the only option whose truth is INDEPENDENT of the mapping's input.
The labeller sees the window's TEXT ONLY — user prompts and assistant prose, code elided, every
`tool_use` block dropped. Tool names, programs and actions are never rendered. So the label comes
from what was SAID and the prediction comes from what was DONE: two disjoint views of the same
window. That is a genuine blind rather than a promise to be careful, which is why `dump` refuses
to emit any level data at all.

Procedure, in this order, each step frozen before the next:
    1. mapping committed (4a64c9b)
    2. `frame`  — build the sampling frame and draw the sample, seeded
    3. `dump`   — render text-only views
    4. label by hand into a labels file
    5. `score`  — apply the mapping and report

## Stated limitations

  - The labeller is the same agent that wrote the mapping. The blind is procedural (it cannot see
    the actions) and not a separate person. A human relabel of the same sample is the check this
    cannot substitute for.
  - The UNIT differs from the model's. Production classifies one prompt; this labels an HOUR of
    work ending at a prompt. For an activity-mix / cost-attribution product the window is the
    better unit — it measures what the system DID rather than what a prompt requested — but the
    two accuracies are not measurements of the same quantity and must not be subtracted.
  - The CORPUS differs. Gold is a curated cross-domain prompt set; this is agentic coding
    transcripts from two machines. The majority baselines therefore differ, and comparing lifts
    across the two sets is weak evidence, reported as such.
"""
import argparse, collections, glob, json, os, random, re, sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))
from app.analysis.activity import ACTIVITIES, activity, counts
from app.analysis.levels import events_for_turns
from app.analysis.text import is_command_echo, text_of
from app.analysis.transcript import iter_turns
from app.analysis.window import rollup
from datetime import datetime, timedelta

SPAN, STRIDE = 60, 50      # minutes; stride must not divide span (established in this series)
SEED = 0
N_SAMPLE = 100
CODE_FENCE = re.compile(r"^```(\w+)?\s*$")
SENT = re.compile(r"(?<=[.!?])\s+")


def _epoch(ts):
    return datetime.fromisoformat(ts.replace("Z", "+00:00"))


def bound(text, limit):
    """Cut at a SENTENCE boundary, never mid-clause, and make the drop VISIBLE. The repo
    convention (AGENTS.md): a 200-rune cap against a 'two or three sentences' instruction
    produced 46 of 47 beats mid-clause. Applies to every string this script renders."""
    text = text.strip()
    if len(text) <= limit:
        return text
    keep, n = [], 0
    for s in SENT.split(text):
        if n + len(s) > limit and keep:
            break
        keep.append(s)
        n += len(s) + 1
    if not keep:                                   # one sentence longer than the whole budget
        return f"[1 sentence of {len(text)} chars omitted: no boundary inside the budget]"
    dropped = len(text) - n
    return " ".join(keep) + (f" [... {dropped} chars omitted]" if dropped > 0 else "")


def elide_code(text):
    """Fenced blocks over 5 lines -> a visible placeholder. Verbatim in spirit from
    prose-activity/prose_activity.py: keeps the signal that code was written, and how much."""
    out, buf, lang, inside = [], [], None, False
    for line in text.split("\n"):
        m = CODE_FENCE.match(line)
        if m and not inside:
            inside, lang, buf = True, m.group(1) or "", []
            continue
        if inside and line.strip().startswith("```"):
            out.append(f"[code omitted: {len(buf)} lines {lang or 'text'}]" if len(buf) > 5
                       else "\n".join(buf))
            inside, buf = False, []
            continue
        (buf if inside else out).append(line)
    if inside:
        out.append(f"[code omitted: {len(buf)} lines {lang or 'text'}]")
    return "\n".join(out)


def build(roots, out):
    """Every window in the corpus -> its rollup counts and its text view, as ndjson.

    Windows are fixed-grid (span/stride) rather than prompt-anchored, so the frame does not
    over-weight the transcripts that prompt most often.
    """
    files = sorted(f for r in roots for f in glob.glob(os.path.join(r, "**", "*.jsonl"),
                                                       recursive=True))
    n_win = n_err = 0
    with open(out, "w") as fh:
        for path in files:
            try:
                turns = [o for o in iter_turns(path) if o.get("timestamp")]
            except Exception:
                n_err += 1
                continue
            if not turns:
                continue
            root = os.path.dirname(os.path.dirname(path))
            t0, tN = _epoch(turns[0]["timestamp"]), _epoch(turns[-1]["timestamp"])
            start = t0
            while start < tN:
                end = start + timedelta(minutes=SPAN)
                sl = [o for o in turns if start <= _epoch(o["timestamp"]) < end]
                start += timedelta(minutes=STRIDE)
                if not sl:
                    continue
                # `reconcile` is deliberately not run: it emits only file/dir/ext/lang/component/
                # artifact rows, none of which this mapping reads, and skipping it keeps the
                # frame cheap. The `action` and `tool` levels come wholly from events_for_turns.
                rows, _pending, _n = events_for_turns(sl, path, root, (), None)
                rl = rollup(rows)
                by, total = counts(rl)
                prompts, prose = [], []
                for o in sl:
                    msg = o.get("message") or {}
                    txt = text_of(msg.get("content"))
                    if not txt.strip():
                        continue
                    if o.get("type") == "user":
                        if not is_command_echo(txt):
                            prompts.append(bound(txt, 700))
                    else:
                        p = bound(elide_code(txt), 400)
                        if p:
                            prose.append(p)
                n_win += 1
                fh.write(json.dumps({
                    "wid": f"{os.path.basename(path)[:8]}-{start:%Y%m%dT%H%M}",
                    "file": path, "start": start.isoformat(),
                    "n_prompts": len(prompts), "n_prose": len(prose),
                    "actions": {k: v for k, v in (rl.get("action") or [])},
                    "tools": bool(rl.get("tool")), "nonempty": bool(rl),
                    "class_counts": dict(by), "total_actions": total,
                    "prompts": prompts, "prose": prose}) + "\n")
    print(f"files={len(files)} unreadable={n_err} windows={n_win}")


def sample(frame, out):
    """The frame's labelable windows -> a seeded uniform sample of N_SAMPLE.

    ELIGIBILITY, fixed here and reported: a window must carry at least one real user prompt and
    at least one assistant prose turn, because a text-only labeller has nothing to read
    otherwise. That is a property of the LABELLING METHOD, not of the mapping — an ineligible
    window is excluded from the scored set and counted, never silently dropped.
    """
    rows = [json.loads(l) for l in open(frame)]
    ok = [r for r in rows if r["n_prompts"] >= 1 and r["n_prose"] >= 1]
    print(f"frame={len(rows)} eligible={len(ok)} "
          f"excluded_no_prompt_or_prose={len(rows) - len(ok)}")
    rnd = random.Random(SEED)
    pick = rnd.sample(ok, min(N_SAMPLE, len(ok)))
    with open(out, "w") as fh:
        for r in pick:
            fh.write(json.dumps(r) + "\n")
    print(f"sampled={len(pick)} seed={SEED}")


def dump(samp, out):
    """Text-only views for hand labelling. Emits NO level data: no tool name, no program, no
    action, no count. The blind is enforced here rather than promised."""
    rows = [json.loads(l) for l in open(samp)]
    with open(out, "w") as fh:
        for i, r in enumerate(rows, 1):
            fh.write(f"\n{'='*100}\n[{i:03d}] {r['wid']}   "
                     f"prompts={r['n_prompts']} assistant_turns={r['n_prose']}\n{'='*100}\n")
            for p in r["prompts"][:12]:
                fh.write(f"\nUSER: {p}\n")
            fh.write("\n--- assistant prose ---\n")
            for p in r["prose"][:14]:
                fh.write(f"* {p}\n")
    print(f"wrote {len(rows)} views -> {out}")


def score(samp, labels, out):
    """Apply the mapping and report. Accuracy is over windows where BOTH sides answer; coverage
    is reported separately, because a mapping that is right on the tenth of windows it can answer
    has replaced nothing."""
    rows = {json.loads(l)["wid"]: json.loads(l) for l in open(samp)}
    truth = {}
    for line in open(labels):
        line = line.split("#")[0].strip()
        if not line:
            continue
        wid, lab = line.split()[:2]
        assert lab in ACTIVITIES, f"{wid}: {lab} not in the production vocabulary"
        truth[wid] = lab
    missing = set(rows) - set(truth)
    assert not missing, f"unlabelled windows: {sorted(missing)}"

    pred, reason = {}, {}
    for wid, r in rows.items():
        rl = {"action": sorted(r["actions"].items(), key=lambda kv: (-kv[1], kv[0]))}
        if r["tools"]:
            rl["tool"] = [("_", 1)]
        elif r["nonempty"]:
            rl["_present"] = [("_", 1)]
        a = activity(rl)
        pred[wid], reason[wid] = a.value, a.reason

    answered = [w for w in rows if pred[w] is not None]
    hits = [w for w in answered if pred[w] == truth[w]]
    tdist = collections.Counter(truth[w] for w in answered)
    pdist = collections.Counter(pred[w] for w in answered)
    full_t = collections.Counter(truth.values())
    maj = max(tdist.values()) / len(answered) if answered else 0.0
    acc = len(hits) / len(answered) if answered else 0.0

    res = {
        "n_sampled": len(rows), "n_answered": len(answered),
        "coverage": round(len(answered) / len(rows), 3),
        "abstention_reasons": dict(collections.Counter(
            reason[w] for w in rows if pred[w] is None)),
        "accuracy_on_answered": round(acc, 3),
        "majority_baseline_on_answered": round(maj, 3),
        "lift": round(acc - maj, 3),
        "accuracy_over_all_sampled": round(len(hits) / len(rows), 3),
        "truth_distribution_answered": dict(tdist.most_common()),
        "truth_distribution_all": dict(full_t.most_common()),
        "predicted_distribution": dict(pdist.most_common()),
        "top_predicted_share": round(max(pdist.values()) / len(answered), 3) if answered else 0,
        "confusion": {},
        "per_class": {},
        "errors": [],
    }
    for w in answered:
        res["confusion"].setdefault(truth[w], {}).setdefault(pred[w], 0)
        res["confusion"][truth[w]][pred[w]] += 1
    for c in ACTIVITIES:
        sup = sum(1 for w in answered if truth[w] == c)
        prd = sum(1 for w in answered if pred[w] == c)
        tp = sum(1 for w in answered if truth[w] == c and pred[w] == c)
        res["per_class"][c] = {
            "support": sup, "predicted": prd,
            "recall": round(tp / sup, 3) if sup else None,
            "precision": round(tp / prd, 3) if prd else None}
    # Per rule 4 of the parent preregistration: named examples, never the aggregate alone.
    for w in sorted(answered):
        if pred[w] != truth[w]:
            r = rows[w]
            res["errors"].append({
                "wid": w, "truth": truth[w], "pred": pred[w],
                "class_counts": r["class_counts"], "total_actions": r["total_actions"],
                "actions": dict(sorted(r["actions"].items(), key=lambda kv: -kv[1])[:6]),
                "first_prompt": (r["prompts"] or [""])[0][:200]})
    json.dump(res, open(out, "w"), indent=1)
    for k in ("n_sampled", "n_answered", "coverage", "abstention_reasons",
              "accuracy_on_answered", "majority_baseline_on_answered", "lift",
              "accuracy_over_all_sampled", "top_predicted_share",
              "truth_distribution_answered", "predicted_distribution"):
        print(f"{k:34s} {res[k]}")
    print("\nper class (support / predicted / recall / precision)")
    for c, d in res["per_class"].items():
        print(f"  {c:10s} {d['support']:4d} {d['predicted']:4d}  "
              f"{d['recall']}  {d['precision']}")
    print(f"\nfull report -> {out}")


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    b = sub.add_parser("frame"); b.add_argument("--roots", nargs="+", required=True)
    b.add_argument("-o", required=True)
    s = sub.add_parser("sample"); s.add_argument("frame"); s.add_argument("-o", required=True)
    d = sub.add_parser("dump"); d.add_argument("sample"); d.add_argument("-o", required=True)
    c = sub.add_parser("score"); c.add_argument("sample"); c.add_argument("labels")
    c.add_argument("-o", required=True)
    a = ap.parse_args()
    if a.cmd == "frame":
        build(a.roots, a.o)
    elif a.cmd == "sample":
        sample(a.frame, a.o)
    elif a.cmd == "dump":
        dump(a.sample, a.o)
    else:
        score(a.sample, a.labels, a.o)


if __name__ == "__main__":
    main()
