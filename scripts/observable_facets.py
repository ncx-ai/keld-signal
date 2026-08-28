#!/usr/bin/env python3
"""Score the two observable binary facets (`app/analysis/observable.py`) on real windows.

Pre-registration: `~/keld/refseries-context/facets/OBSERVABLE-FACETS-PREREGISTRATION.md`. The
mapping was committed BEFORE this script existed (git `a2f50e1`) and the hand labels are committed
before `score` is ever run. That ordering is provable from history, which is the only thing that
makes these numbers falsifiable.

Procedure, each step frozen before the next:
    1. mapping + its tests committed          (a2f50e1)
    2. `frame`  build the sampling frame — every window in the corpus, with an ASSERTED count
    3. `sample` a seeded uniform draw of N_SAMPLE, exclusions reported
    4. `dump`   render TEXT-ONLY views: prompts + assistant prose, every tool_use block dropped
    5. label by hand into a labels file, then COMMIT it
    6. `score`  apply the mapping and report

## The blind, and how it is enforced rather than promised

`dump` emits no level data at all: no tool name, no program, no action, no count, no volume. It
is the activity refutation's mechanism reused verbatim in intent (that study's `dump` did the same)
and re-implemented here over this frame. The labeller therefore reads what was SAID and the mapping
predicts from what was DONE — two disjoint views of the same window. `text.text_of` yields only
`str` and `text` parts of a message's content list, so a `tool_use` block contributes nothing; the
assertion in `dump` re-checks that no rendered view contains a tool name, so a change to `text_of`
cannot silently unblind the labeller.

## PER-FILE IDENTITY — the correctness dependency this study was warned about

`levels.events_for_turns` labels each event with `basename(path)[:8]`, which is **NOT unique**:
Claude Code writes subagent transcripts as `agent-<hash>.jsonl`, so on this corpus 500 transcripts
collapse onto ~71 prefixes and 445 sit in a colliding group (`agent-a6` alone covers 37 files). The
routing-class study keyed its first frame on that value and **silently merged 550 windows where the
truth was 1,022 — a 46% loss with no error raised.**

So this frame never reads that field. Windows are cut per file, inside the loop over files, and
`wid` carries the file's index in the sorted file list. `assert_frame` then proves the merge did
not happen three ways: the row count equals an independent per-file recount, every `wid` is
distinct, and the count that KEYING ON THE PREFIX WOULD HAVE GIVEN is computed and asserted to be
strictly smaller. That last one is the important one — it fails loudly on a corpus where the trap
is live, instead of quietly agreeing when it is not.
"""
import argparse, collections, glob, json, math, os, random, re, sys
from datetime import datetime, timedelta

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))
from app.analysis.levels import events_for_turns
from app.analysis.observable import VARIANTS
from app.analysis.text import is_command_echo, text_of
from app.analysis.transcript import iter_turns
from app.analysis.window import rollup

SPAN, STRIDE = 60, 50      # minutes; stride must not divide span (established in this series)
SEED = 0
N_SAMPLE = 120             # the pre-registration's n: larger than the refutation's 100 because
                           # two facets are scored on the same sample
# The frame the activity refutation and the routing-class study both built from these roots with
# these window parameters. Pinned as an ASSERTION, not a comment: a frame that comes out at 550
# is the session-collision merge, and it looked perfectly plausible when it happened.
EXPECTED_WINDOWS = 1022

CODE_FENCE = re.compile(r"^```(\w+)?\s*$")
SENT = re.compile(r"(?<=[.!?])\s+")
# Markers of a SERIALIZED tool block. Deliberately not the bare word `tool_use`: this corpus is
# engineers discussing transcript formats, and one sampled window's prose contains the literal
# "thinking/text/tool_use lines" while carrying no tool block at all (verified by hand,
# 9a1c9df2#t0494-20260821T1701). A substring check on the bare word fails on legitimate prose and
# would have to be weakened under pressure — so the real guard is `assert_blind` below, which
# tests the MECHANISM, and this list only catches a serialized block leaking through.
BLOCK_MARKERS = ('"type": "tool_use"', "'type': 'tool_use'", '"type":"tool_use"',
                 "tool_use_id", "toolUseID", "toolu_01")


def assert_blind():
    """Prove `text.text_of` drops every non-prose block, which is WHY the views are blind.

    The activity refutation enforced its blind by construction — its `dump` rendered prompts and
    assistant prose only. This asserts the property that construction relies on, so a change to
    `text_of` cannot silently unblind a future labeller: a tool call, its result, and the model's
    thinking must all contribute NOTHING.
    """
    probe = [{"type": "text", "text": "PROSE."},
             {"type": "tool_use", "id": "toolu_01ABC", "name": "Read",
              "input": {"file_path": "/x", "pattern": "SECRET"}},
             {"type": "tool_result", "tool_use_id": "toolu_01ABC", "content": "LEAKED"},
             {"type": "thinking", "thinking": "HIDDEN"}]
    got = text_of(probe)
    assert got.strip() == "PROSE.", f"text_of no longer drops tool blocks: {got!r}"
    for banned in ("Read", "toolu_01ABC", "LEAKED", "HIDDEN", "SECRET", "file_path"):
        assert banned not in got, f"text_of leaks {banned!r} — the blind is broken"


def _epoch(ts):
    return datetime.fromisoformat(ts.replace("Z", "+00:00"))


def bound(text, limit):
    """Cut at a SENTENCE boundary, never mid-clause, and make the drop VISIBLE — the repo
    convention (AGENTS.md: a 200-rune cap against a "two or three sentences" instruction produced
    46 of 47 beats mid-clause). Applies to every string this script renders."""
    text = text.strip()
    if len(text) <= limit:
        return text
    keep, n = [], 0
    for s in SENT.split(text):
        if n + len(s) > limit and keep:
            break
        keep.append(s)
        n += len(s) + 1
    if not keep:
        return f"[1 sentence of {len(text)} chars omitted: no boundary inside the budget]"
    dropped = len(text) - n
    return " ".join(keep) + (f" [... {dropped} chars omitted]" if dropped > 0 else "")


def elide_code(text):
    """Fenced blocks over 5 lines -> a visible placeholder: keeps the signal that code was
    written, and how much, without rendering it."""
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


def windows_of(path, fid, turns):
    """One transcript's fixed-grid windows. Cut INSIDE the per-file loop, so no cross-file merge
    is representable — see the module docstring."""
    root = os.path.dirname(os.path.dirname(path))
    t0, tN = _epoch(turns[0]["timestamp"]), _epoch(turns[-1]["timestamp"])
    start, out = t0, []
    while start < tN:
        end = start + timedelta(minutes=SPAN)
        sl = [o for o in turns if start <= _epoch(o["timestamp"]) < end]
        here, start = start, start + timedelta(minutes=STRIDE)
        if not sl:
            continue
        # `reconcile` is deliberately not run: it emits file/dir/ext/lang/component/artifact rows,
        # none of which these two mappings read. The `action` level comes wholly from
        # events_for_turns. `session=` is NOT passed — the default label is unused here, so this
        # frame is correct whether or not the sibling agent's levels.py fix has landed.
        rows, _pending, _n = events_for_turns(sl, path, root, (), None)
        rl = rollup(rows)
        # volume == every ref event at every level except `term`, matching the routing-class
        # study's `_total` so "log window volume" means the same thing in both.
        volume = int(sum(n for lv, items in rl.items() if lv != "term" for _r, n in items))
        prompts, prose = [], []
        for o in sl:
            txt = text_of((o.get("message") or {}).get("content"))
            if not txt.strip():
                continue
            if o.get("type") == "user":
                if not is_command_echo(txt):
                    prompts.append(bound(txt, 700))
            else:
                p = bound(elide_code(txt), 400)
                if p:
                    prose.append(p)
        out.append({
            "wid": f"{os.path.basename(path)[:8]}#t{fid:04d}-{here:%Y%m%dT%H%M}",
            "file": path, "fid": fid, "prefix": os.path.basename(path)[:8],
            "start": here.isoformat(),
            "n_prompts": len(prompts), "n_prose": len(prose),
            "actions": {k: int(v) for k, v in (rl.get("action") or [])},
            "volume": volume, "n_actions": int(sum(n for _r, n in (rl.get("action") or []))),
            "levels": sorted(rl), "tools": bool(rl.get("tool")), "nonempty": bool(rl),
            "prompts": prompts, "prose": prose})
    return out


def assert_frame(recs, per_file, files):
    """Prove the session-collision merge did not happen. Required by the brief, and it is an
    ASSERTION rather than a printed number because the merge's whole danger is that it looks
    plausible: 550 windows and 1,022 windows are equally believable in a log line."""
    n = len(recs)
    recount = sum(per_file.values())
    assert n == recount, f"frame rows {n} != per-file recount {recount}"
    assert len({r["wid"] for r in recs}) == n, (
        f"{n - len({r['wid'] for r in recs})} colliding wids — the frame merged windows")
    assert len({(r["file"], r["start"]) for r in recs}) == n, "duplicate (file, start)"
    windowed = {f for f, k in per_file.items() if k}
    assert len({r["file"] for r in recs}) == len(windowed), (
        f"{len(windowed)} files produced windows but the frame holds "
        f"{len({r['file'] for r in recs})} — a file's windows split or merged across keys")

    # What keying on `basename(path)[:8]` would have produced: windows are (prefix, start) pairs,
    # so two files whose windows share a start collapse into one.
    by_prefix = len({(r["prefix"], r["start"]) for r in recs})
    prefixes = {r["prefix"] for r in recs}
    colliding = sum(1 for p, fs in collections.Counter(
        os.path.basename(f)[:8] for f in files).items() if fs > 1 for _ in range(fs))
    assert by_prefix < n, (
        "the prefix-keyed count did NOT come out lower, so this assertion is not testing "
        "anything on this corpus — check the trap is still live before trusting the frame")
    assert n == EXPECTED_WINDOWS, (
        f"frame is {n} windows, expected {EXPECTED_WINDOWS}. If the corpus changed, verify the "
        f"new number by hand before editing this constant — {by_prefix} is what the known "
        f"session-collision bug produces here, and it raises no error on its own.")
    print(f"ASSERTED windows={n} files={len(per_file)} distinct_wids={n}")
    print(f"  session-collision check: prefix-keyed would give {by_prefix} "
          f"({n - by_prefix} windows lost, {round(100 * (n - by_prefix) / n, 1)}%); "
          f"{len(prefixes)} distinct prefixes over {len(files)} files, "
          f"{colliding} files in a colliding group")


def build(roots, out):
    files = sorted(f for r in roots for f in glob.glob(os.path.join(r, "**", "*.jsonl"),
                                                       recursive=True))
    if not files:
        sys.exit("no transcripts under " + ", ".join(roots))
    recs, per_file, n_err, n_empty = [], {}, 0, 0
    for fid, path in enumerate(files):
        try:
            turns = [o for o in iter_turns(path) if o.get("timestamp")]
        except Exception:
            n_err += 1
            continue
        if not turns:
            n_empty += 1
            continue
        w = windows_of(path, fid, turns)
        per_file[path] = len(w)
        recs.extend(w)
    print(f"files={len(files)} unreadable={n_err} empty={n_empty} "
          f"zero_window_files={sum(1 for k in per_file.values() if not k)}")
    # Written UNVERIFIED first, then asserted, then renamed. Two reasons: a ten-minute parse must
    # not have to be repeated to re-check an assertion, and a frame that has NOT passed the
    # session-collision assertions must never be sitting at the path `sample` reads.
    tmp = out + ".unverified"
    with open(tmp, "w") as fh:
        for r in recs:
            fh.write(json.dumps(r) + "\n")
    print(f"unverified frame -> {tmp}")
    assert_frame(recs, per_file, files)
    os.replace(tmp, out)
    print(f"frame -> {out}")


def verify(path):
    """Re-run the assertions over an already-written frame. Same checks, no re-parse."""
    recs = [json.loads(l) for l in open(path)]
    per_file = collections.Counter(r["file"] for r in recs)
    assert_frame(recs, per_file, sorted(per_file))
    if path.endswith(".unverified"):
        os.replace(path, path[: -len(".unverified")])
        print(f"frame -> {path[: -len('.unverified')]}")


def sample(frame, out):
    """Seeded uniform draw. ELIGIBILITY, fixed here: a window must carry at least one real user
    prompt and one assistant prose turn, because a TEXT-ONLY labeller has nothing to read
    otherwise. That is a property of the labelling method, not of the mapping — an ineligible
    window is excluded and COUNTED, never silently dropped."""
    rows = [json.loads(l) for l in open(frame)]
    ok = [r for r in rows if r["n_prompts"] >= 1 and r["n_prose"] >= 1]
    print(f"frame={len(rows)} eligible={len(ok)} "
          f"excluded_no_prompt_or_prose={len(rows) - len(ok)}")
    rnd = random.Random(SEED)
    pick = rnd.sample(sorted(ok, key=lambda r: r["wid"]), min(N_SAMPLE, len(ok)))
    with open(out, "w") as fh:
        for r in pick:
            fh.write(json.dumps(r) + "\n")
    print(f"sampled={len(pick)} seed={SEED} distinct_files={len({r['file'] for r in pick})}")


def dump(samp, out):
    """Text-only views for hand labelling. Emits NO level data: no tool name, no program, no
    action, no count, no volume. Asserted, not promised."""
    rows = [json.loads(l) for l in open(samp)]
    body = []
    for i, r in enumerate(rows, 1):
        v = [f"\n{'=' * 100}\n[{i:03d}] {r['wid']}   "
             f"prompts={r['n_prompts']} assistant_turns={r['n_prose']}\n{'=' * 100}\n"]
        for p in r["prompts"][:12]:
            v.append(f"\nUSER: {p}\n")
        v.append("\n--- assistant prose ---\n")
        for p in r["prose"][:14]:
            v.append(f"* {p}\n")
        body.append("".join(v))
    text = "".join(body)
    assert_blind()
    for t in BLOCK_MARKERS:
        assert t not in text, f"the view leaks {t!r} — a serialized tool block reached the view"
    open(out, "w").write(text)
    print(f"wrote {len(rows)} views -> {out}")
    print("blind: text_of verified to drop tool_use/tool_result/thinking; "
          f"no serialized-block marker in the views ({len(BLOCK_MARKERS)} checked)")


def pearson(xs, ys):
    n = len(xs)
    if n < 2:
        return None
    mx, my = sum(xs) / n, sum(ys) / n
    sxy = sum((x - mx) * (y - my) for x, y in zip(xs, ys))
    sxx = sum((x - mx) ** 2 for x in xs)
    syy = sum((y - my) ** 2 for y in ys)
    if sxx <= 0 or syy <= 0:
        return None
    return sxy / math.sqrt(sxx * syy)


def read_labels(path):
    """`wid authoring verification` with y/n/? per facet. `?` is a labeller abstention — the text
    genuinely does not say — and is reported as an exclusion rather than forced into a guess."""
    out = {}
    for ln, line in enumerate(open(path), 1):
        # A comment is a line that STARTS with `#`, not everything after the first `#`. The
        # activity study's `line.split("#")[0]` idiom is wrong here and fails loudly: a `wid`
        # contains a `#` (`fff01ac3#t0298-...`, the per-file id that keeps this frame off the
        # colliding session prefix), so that idiom truncated every label to its prefix.
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        parts = line.split()
        assert len(parts) == 3, f"{path}:{ln}: want `wid authoring verification`, got {line!r}"
        wid, a, v = parts
        for x in (a, v):
            assert x in ("y", "n", "?"), f"{path}:{ln}: {x!r} not in y/n/?"
        assert wid not in out, f"{path}:{ln}: duplicate {wid}"
        out[wid] = {"authoring": {"y": True, "n": False, "?": None}[a],
                    "verification": {"y": True, "n": False, "?": None}[v]}
    return out


def score_one(name, fn, facet, rows, truth):
    pred = {w: fn(_rl(r)) for w, r in rows.items()}
    labelled = [w for w in rows if truth[w][facet] is not None]
    answered = [w for w in labelled if pred[w].value is not None]
    hits = [w for w in answered if pred[w].value == truth[w][facet]]
    tdist = collections.Counter(truth[w][facet] for w in answered)
    pdist = collections.Counter(pred[w].value for w in answered)
    acc = len(hits) / len(answered) if answered else 0.0
    maj = max(tdist.values()) / len(answered) if answered else 0.0
    lv = [math.log1p(rows[w]["volume"]) for w in answered]
    r_pred = pearson([1.0 if pred[w].value else 0.0 for w in answered], lv)
    r_truth = pearson([1.0 if truth[w][facet] else 0.0 for w in answered], lv)
    la = [math.log1p(rows[w]["n_actions"]) for w in answered]
    res = {
        "facet": facet, "mapping": name,
        "n_sampled": len(rows), "n_labeller_abstained": len(rows) - len(labelled),
        "n_scorable": len(labelled), "n_answered": len(answered),
        "coverage": round(len(answered) / len(labelled), 3) if labelled else 0.0,
        "abstention_reasons": dict(collections.Counter(
            pred[w].reason for w in labelled if pred[w].value is None)),
        "accuracy_on_answered": round(acc, 3),
        "majority_baseline_on_answered": round(maj, 3),
        "lift": round(acc - maj, 3),
        "majority_class": (tdist.most_common(1) or [(None, 0)])[0][0],
        "truth_base_rate_true": round(tdist[True] / len(answered), 3) if answered else 0.0,
        "predicted_true_share": round(pdist[True] / len(answered), 3) if answered else 0.0,
        "top_predicted_share": round(max(pdist.values()) / len(answered), 3) if answered else 0.0,
        "r_pred_vs_log_volume": None if r_pred is None else round(r_pred, 3),
        "r_truth_vs_log_volume": None if r_truth is None else round(r_truth, 3),
        "r_pred_vs_log_n_actions": None if (r := pearson(
            [1.0 if pred[w].value else 0.0 for w in answered], la)) is None else round(r, 3),
        "confusion": {"tp": sum(1 for w in answered if pred[w].value and truth[w][facet]),
                      "fp": sum(1 for w in answered if pred[w].value
                                and not truth[w][facet]),
                      "fn": sum(1 for w in answered if not pred[w].value
                                and truth[w][facet]),
                      "tn": sum(1 for w in answered if not pred[w].value
                                and not truth[w][facet])},
        "errors": [],
    }
    c = res["confusion"]
    res["precision"] = round(c["tp"] / (c["tp"] + c["fp"]), 3) if c["tp"] + c["fp"] else None
    res["recall"] = round(c["tp"] / (c["tp"] + c["fn"]), 3) if c["tp"] + c["fn"] else None
    # Rule: NAMED examples, never the aggregate alone. Roughly twenty defects in this series
    # surfaced as plausible wrong numbers and essentially none was caught by reading a mean.
    for w in sorted(answered):
        if pred[w].value != truth[w][facet]:
            r = rows[w]
            res["errors"].append({
                "wid": w, "truth": truth[w][facet], "pred": pred[w].value,
                "evidence": pred[w].evidence, "n_actions": r["n_actions"],
                "volume": r["volume"],
                "actions": dict(sorted(r["actions"].items(), key=lambda kv: -kv[1])[:8]),
                "first_prompt": (r["prompts"] or [""])[0][:180]})
    # Rules 1-4 adjudicated mechanically, so the verdict cannot drift from the numbers.
    res["rules"] = {
        "1_beats_constant_by_10pts": res["lift"] >= 0.10,
        "2_not_degenerate_max_85pct": res["top_predicted_share"] <= 0.85,
        "3_coverage_at_least_60pct": res["coverage"] >= 0.60,
        "4_independent_r_under_0.5": (res["r_pred_vs_log_volume"] is not None
                                     and abs(res["r_pred_vs_log_volume"]) < 0.5),
    }
    res["passes_rules_1_to_4"] = all(res["rules"].values())
    return res


def _rl(r):
    """The window record back into a rollup shape the mapping accepts."""
    rl = {"action": sorted(r["actions"].items(), key=lambda kv: (-kv[1], kv[0]))}
    if not rl["action"]:
        del rl["action"]
    for lv in r["levels"]:
        if lv != "action":
            rl.setdefault(lv, [("_", 1)])
    return rl


def score(samp, labels, out):
    rows = {}
    for l in open(samp):
        r = json.loads(l)
        rows[r["wid"]] = r
    truth = read_labels(labels)
    missing = set(rows) - set(truth)
    assert not missing, f"unlabelled windows: {sorted(missing)[:5]} ({len(missing)} total)"
    extra = set(truth) - set(rows)
    assert not extra, f"labels for windows not in the sample: {sorted(extra)[:5]}"

    plan = [("authoring", "authoring", "PRIMARY"),
            ("verification", "verification", "PRIMARY"),
            ("authoring_narrow", "authoring", "sensitivity: WRITING set only"),
            ("authoring_sustained", "authoring", "variant: floor on the positive class"),
            ("authoring_collapse", "authoring", "replication of the post-hoc 0.756")]
    all_res = []
    for name, facet, kind in plan:
        res = score_one(name, VARIANTS[name], facet, rows, truth)
        res["kind"] = kind
        all_res.append(res)
        print(f"\n{'=' * 78}\n{name}  [{kind}]  facet={facet}\n{'=' * 78}")
        for k in ("n_sampled", "n_labeller_abstained", "n_scorable", "n_answered", "coverage",
                  "abstention_reasons", "accuracy_on_answered",
                  "majority_baseline_on_answered", "lift", "majority_class",
                  "truth_base_rate_true", "predicted_true_share", "top_predicted_share",
                  "precision", "recall", "confusion", "r_pred_vs_log_volume",
                  "r_truth_vs_log_volume", "r_pred_vs_log_n_actions"):
            print(f"  {k:32s} {res[k]}")
        for rk, ok in res["rules"].items():
            print(f"  RULE {rk:34s} {'PASS' if ok else 'FAIL'}")
        print(f"  {'VERDICT (rules 1-4)':34s} "
              f"{'PASS' if res['passes_rules_1_to_4'] else 'FAIL'}   errors={len(res['errors'])}")
    # Do the two primaries carry different information, or is one a restatement of the other?
    both = [w for w in rows if truth[w]["authoring"] is not None
            and truth[w]["verification"] is not None]
    agree = sum(1 for w in both if truth[w]["authoring"] == truth[w]["verification"])
    r_tt = pearson([1.0 if truth[w]["authoring"] else 0.0 for w in both],
                   [1.0 if truth[w]["verification"] else 0.0 for w in both])
    joint = {"n": len(both), "truth_agreement": round(agree / len(both), 3) if both else None,
             "r_truth_authoring_vs_truth_verification": None if r_tt is None else round(r_tt, 3),
             "truth_joint": dict(collections.Counter(
                 f"auth={truth[w]['authoring']},ver={truth[w]['verification']}"
                 for w in both).most_common())}
    print(f"\nJOINT: {joint}")
    json.dump({"seed": SEED, "n_sample": N_SAMPLE, "span": SPAN, "stride": STRIDE,
               "expected_windows": EXPECTED_WINDOWS, "results": all_res, "joint": joint},
              open(out, "w"), indent=1)
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
    v = sub.add_parser("verify"); v.add_argument("frame")
    a = ap.parse_args()
    if a.cmd == "frame":
        build(a.roots, a.o)
    elif a.cmd == "sample":
        sample(a.frame, a.o)
    elif a.cmd == "dump":
        dump(a.sample, a.o)
    elif a.cmd == "verify":
        verify(a.frame)
    else:
        score(a.sample, a.labels, a.o)


if __name__ == "__main__":
    main()
