#!/usr/bin/env python3
"""Re-measure the six-way deterministic `activity_type` on the CORRECTED `action` vocabulary.

Attempt FOUR. Pre-registration:
`~/keld/refseries-context/facets/ACTIVITY-RERUN-PREREGISTRATION.md`, written before any label was
assigned. Its six rules are adjudicated mechanically in `score` so the verdict cannot drift from
the numbers.

## What is new here, and what deliberately is not

NOT new: the mapping. `app.analysis.activity.activity` is scored **byte-unchanged since 4a64c9b**
(git-provable), and that is the whole experiment. The three prior refutations were all measured on
an `action` level that was substantially wrong — `4ad9add` moved `transform` 4345 -> 191 (96% of
records), `test` 1991 -> 3772, and added 1,095 create/edit events from heredoc writes. The only
justification for a fourth attempt is that the INPUT provably changed, so the honest test is the
same mapping on the corrected input.

Re-specification was considered and REJECTED, per act value, from the vocabulary's semantics:

    transform (act)  used to mean any sed/awk/sort/cut invocation; now means only the in-place
                     form (`sed -i`, `awk -i inplace`). `transform`->`transform` was WRONG-ish
                     before by inclusion and is exactly right now: an in-place stream edit IS
                     "reformat existing content". No change.
    test / build     now catch pytest/vitest/tsc invoked THROUGH a wrapper. Already -> `review`.
    create / edit    now include heredoc writes (`cat > f <<EOF`). Already -> generate/transform.
    query a database now populated (+234). Already -> `retrieve`.
    run a service    no longer swallows pnpm/npm/node. Still unmapped, still correctly so.
    install          now populated (+211). Still unmapped: environment setup is not one of the six.
    version control  unchanged by the fix, so its documented reason to stay unmapped stands.

So no action value's binding became mis-specified by `4ad9add`, and there is nothing to revise
that the fix licenses. The one change a reader will reach for — `edit` -> `generate`, since
`Activities.generate` names "code" explicitly — is NOT licensed: it addresses the act-vs-intent
mismatch, which the vocabulary fix does not touch, and choosing it now would be fitted to attempt
one's error table (where `transform`'s true support was 0). It is scored instead as a DECLARED,
NON-PROMOTABLE diagnostic (`collapse_gen_tra`), exactly as attempt one's post-hoc collapse A was.

New: this frame, this sample, these labels. **150 windows with ZERO overlap** against the
100-window activity sample and the 120-window observable sample — asserted in `sample`, not
promised. Fresh labels are mandatory because the `4ad9add` re-run's authoring 0.765 -> 0.904 was
scored on the labels the fix was developed against, so it is fitted to them and is not evidence.

Procedure, each step frozen before the next and provable from git:
    1. mapping                          4a64c9b, body byte-unchanged
    2. this harness committed           before any label exists
    3. `frame`   every window, with the session-collision merge ASSERTED away
    4. `sample`  seeded uniform n=150 over the frame MINUS both prior samples, overlap asserted 0
    5. `dump`    TEXT-ONLY views; the blind is enforced by `text_of`, asserted here
    6. label by hand, then COMMIT the labels
    7. `score`   apply the mapping and adjudicate the six rules

## The blind

`dump` emits no level data at all: no tool name, no program, no action, no count, no volume.
`assert_blind` tests the MECHANISM (`text.text_of` drops `tool_use`/`tool_result`/`thinking`), so a
change to `text_of` cannot silently unblind a future labeller; `BLOCK_MARKERS` additionally catches
a serialized block leaking into a view. Deliberately NOT a check for the bare word `tool_use`: this
corpus is engineers discussing transcript formats, and that check fires on legitimate prose (see
`observable_facets.py`, which found the case).

## PER-FILE IDENTITY

`levels.events_for_turns` labels events with `basename(path)[:8]`, which is NOT unique on this
corpus (500 transcripts -> ~71 prefixes; `agent-a6` alone covers 37 files). The routing-class study
keyed a frame on it and silently merged 550 windows where the truth was 1,022 — a 46% loss with no
error raised. This frame never reads that field for identity: windows are cut inside the per-file
loop and `wid` carries the file's index in the sorted file list.
"""
import argparse, collections, glob, json, math, os, random, re, sys
from datetime import datetime, timedelta

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))
from app.analysis.activity import ACTIVITIES, PRECEDENCE, activity
from app.analysis.levels import events_for_turns
from app.analysis.text import is_command_echo, text_of
from app.analysis.transcript import iter_turns
from app.analysis.window import rollup

SPAN, STRIDE = 60, 50      # minutes; stride must not divide span (established in this series)
SEED = 0
N_SAMPLE = 150             # the pre-registration's n: larger than 100/120 because a SIX-way split
                           # needs support per class, and because the observable baseline moved
                           # 0.538 -> 0.653 on resampling alone — over half that apparent effect.
EXPECTED_WINDOWS = 1022    # pinned as an ASSERTION: 550 is what the session-collision bug
                           # produces here and it raises no error on its own.

# Rule thresholds, from the pre-registration. Named constants so `score` cannot quietly use a
# different number than the one that was registered.
RULE1_MIN_LIFT = 0.10
RULE2_MAX_PRED_SHARE = 0.70
RULE2_ZERO_CORRECT_AT = 10      # >10 predictions with zero correct is the `transform` signature
RULE3_MIN_COVERAGE = 0.60
RULE4_MIN_PRECISION = 0.40
RULE4_MIN_SUPPORT = 5
RULE5_MAX_R = 0.50

CODE_FENCE = re.compile(r"^```(\w+)?\s*$")
SENT = re.compile(r"(?<=[.!?])\s+")
BLOCK_MARKERS = ('"type": "tool_use"', "'type': 'tool_use'", '"type":"tool_use"',
                 "tool_use_id", "toolUseID", "toolu_01")

# Prior samples this run must not touch. The observable wids are unambiguous (they carry `fid`);
# the activity wids are `prefix-STAMP` and a prefix covers up to 37 files, so a match there is
# AMBIGUOUS. Exclusion is therefore by `(prefix, stamp)` for both, which is exact for the
# observable set and a deliberate SUPERSET for the activity set: it may exclude a window that was
# never labelled, and it cannot admit one that was.
PRIOR_LABELS = ("activity-hand-labels.txt", "observable-hand-labels.txt")
WID_RE = re.compile(r"^(?P<prefix>[^\s#]+?)(?:#t\d{4})?-(?P<stamp>\d{8}T\d{4})$")


def assert_blind():
    """Prove `text.text_of` drops every non-prose block, which is WHY the views are blind."""
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
    46 of 47 beats mid-clause)."""
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
    """One transcript's fixed-grid windows, cut INSIDE the per-file loop so no cross-file merge is
    representable."""
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
        # none of which this mapping reads. The `action` and `tool` levels come wholly from
        # events_for_turns.
        rows, _pending, _n = events_for_turns(sl, path, root, (), None)
        rl = rollup(rows)
        # volume == every ref event at every level except `term`, matching the routing-class and
        # observable studies' `_total`, so "log window volume" means the same thing in all three.
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
        prefix = os.path.basename(path)[:8]
        out.append({
            "wid": f"{prefix}#t{fid:04d}-{here:%Y%m%dT%H%M}",
            "key": f"{prefix}-{here:%Y%m%dT%H%M}",   # the ambiguous prior-sample key
            "file": path, "fid": fid, "prefix": prefix, "start": here.isoformat(),
            "n_prompts": len(prompts), "n_prose": len(prose),
            "actions": {k: int(v) for k, v in (rl.get("action") or [])},
            "volume": volume, "n_actions": int(sum(n for _r, n in (rl.get("action") or []))),
            "levels": sorted(rl), "tools": bool(rl.get("tool")), "nonempty": bool(rl),
            "prompts": prompts, "prose": prose})
    return out


def assert_frame(recs, per_file, files):
    """Prove the session-collision merge did not happen. An ASSERTION rather than a printed
    number because the merge's whole danger is that it looks plausible."""
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
    by_prefix = len({(r["prefix"], r["start"]) for r in recs})
    assert by_prefix < n, (
        "the prefix-keyed count did NOT come out lower, so this assertion is not testing "
        "anything on this corpus — check the trap is still live before trusting the frame")
    assert n == EXPECTED_WINDOWS, (
        f"frame is {n} windows, expected {EXPECTED_WINDOWS}. Verify a new number by hand before "
        f"editing this constant — {by_prefix} is what the session-collision bug produces here.")
    print(f"ASSERTED windows={n} files={len(per_file)} distinct_wids={n}")
    print(f"  session-collision check: prefix-keyed would give {by_prefix} "
          f"({n - by_prefix} lost, {round(100 * (n - by_prefix) / n, 1)}%)")


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
    print(f"files={len(files)} unreadable={n_err} empty={n_empty}")
    # Written UNVERIFIED first, then asserted, then renamed: a frame that has not passed the
    # collision assertions must never be sitting at the path `sample` reads.
    tmp = out + ".unverified"
    with open(tmp, "w") as fh:
        for r in recs:
            fh.write(json.dumps(r) + "\n")
    print(f"unverified frame -> {tmp}")
    assert_frame(recs, per_file, files)
    os.replace(tmp, out)
    print(f"frame -> {out}")


def verify(path):
    recs = [json.loads(l) for l in open(path)]
    per_file = collections.Counter(r["file"] for r in recs)
    assert_frame(recs, per_file, sorted(per_file))
    if path.endswith(".unverified"):
        os.replace(path, path[: -len(".unverified")])
        print(f"frame -> {path[: -len('.unverified')]}")


def prior_keys(paths):
    """`(prefix, stamp)` for every window labelled by a prior study. Parses BOTH wid shapes —
    `prefix-STAMP` (activity, ambiguous) and `prefix#tNNNN-STAMP` (observable, exact) — and
    reduces both to the ambiguous key, which is the conservative direction."""
    keys, per = set(), {}
    for p in paths:
        got = set()
        for line in open(p):
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            wid = line.split()[0]
            m = WID_RE.match(wid)
            assert m, f"{p}: cannot parse wid {wid!r}"
            got.add(f"{m.group('prefix')}-{m.group('stamp')}")
        per[os.path.basename(p)] = len(got)
        keys |= got
    return keys, per


def sample(frame, out, prior_dir):
    """Seeded uniform draw over the frame MINUS every window either prior study labelled.

    ELIGIBILITY, fixed here: at least one real user prompt and one assistant prose turn, because
    a TEXT-ONLY labeller has nothing to read otherwise. A property of the labelling method, not of
    the mapping — an ineligible window is excluded and COUNTED, never silently dropped.
    """
    rows = [json.loads(l) for l in open(frame)]
    used, per = prior_keys([os.path.join(prior_dir, f) for f in PRIOR_LABELS])
    ok = [r for r in rows if r["n_prompts"] >= 1 and r["n_prose"] >= 1]
    fresh = [r for r in ok if r["key"] not in used]
    print(f"frame={len(rows)} eligible={len(ok)} "
          f"excluded_no_prompt_or_prose={len(rows) - len(ok)}")
    print(f"prior labels: {per} -> {len(used)} distinct (prefix,stamp) keys; "
          f"eligible windows removed={len(ok) - len(fresh)}; fresh pool={len(fresh)}")
    rnd = random.Random(SEED)
    pick = rnd.sample(sorted(fresh, key=lambda r: r["wid"]), min(N_SAMPLE, len(fresh)))
    # The required assertion. Not a promise: the sample cannot contain a prior-labelled window.
    overlap = sorted(r["wid"] for r in pick if r["key"] in used)
    assert not overlap, f"OVERLAP with a prior sample: {overlap}"
    assert len(pick) == N_SAMPLE, f"drew {len(pick)}, wanted {N_SAMPLE}"
    with open(out, "w") as fh:
        for r in pick:
            fh.write(json.dumps(r) + "\n")
    print(f"sampled={len(pick)} seed={SEED} distinct_files={len({r['file'] for r in pick})}")
    print(f"OVERLAP WITH PRIOR SAMPLES = 0  (asserted over {len(used)} prior keys)")


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


# Rule 5 needs a scalar for a SIX-valued prediction, where the observable study needed only a
# binary. The pre-registration says "the predicted label's RANK", so the rank is the mapping's own
# precedence position — with `converse` last, because `converse` is the shape of a window that
# reached for nothing at all, i.e. the least-evidence end of the same axis. A size bucket shows up
# as a strong NEGATIVE r: more volume -> an earlier (more specific) class wins.
RANK = {c: i for i, c in enumerate(PRECEDENCE)}
RANK["converse"] = len(PRECEDENCE)


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


def eta(groups, values):
    """Correlation ratio: the encoding-free companion to rule 5's rank correlation. For a BINARY
    grouping it equals |r| exactly, so it is the natural generalisation of the observable study's
    rule to a six-valued label, and it cannot be moved by choosing a different rank order."""
    n = len(values)
    if n < 2:
        return None
    gm = sum(values) / n
    tot = sum((v - gm) ** 2 for v in values)
    if tot <= 0:
        return None
    by = collections.defaultdict(list)
    for g, v in zip(groups, values):
        by[g].append(v)
    between = sum(len(vs) * (sum(vs) / len(vs) - gm) ** 2 for vs in by.values())
    return math.sqrt(between / tot)


def read_labels(path):
    """`wid activity`, one per line; `#` only at the START of a line is a comment.

    NOT `line.split("#")[0]`: a `wid` contains a `#` (the per-file id that keeps this frame off
    the colliding session prefix), and that idiom truncated every label to its prefix in the
    observable study — it failed loudly on line 1, before any score existed.
    """
    out = {}
    for ln, line in enumerate(open(path), 1):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        parts = line.split()
        assert len(parts) == 2, f"{path}:{ln}: want `wid activity`, got {line!r}"
        wid, lab = parts
        assert lab in ACTIVITIES, f"{path}:{ln}: {lab!r} not in the production vocabulary"
        assert wid not in out, f"{path}:{ln}: duplicate {wid}"
        out[wid] = lab
    return out


def _rl(r):
    """The window record back into the rollup shape the mapping accepts."""
    rl = {"action": sorted(r["actions"].items(), key=lambda kv: (-kv[1], kv[0]))}
    if not rl["action"]:
        del rl["action"]
    for lv in r["levels"]:
        if lv != "action":
            rl.setdefault(lv, [("_", 1)])
    return rl


def per_class(answered, truth, pred, classes):
    tbl = {}
    for c in classes:
        sup = sum(1 for w in answered if truth[w] == c)
        prd = sum(1 for w in answered if pred[w] == c)
        tp = sum(1 for w in answered if truth[w] == c and pred[w] == c)
        tbl[c] = {"support": sup, "predicted": prd, "tp": tp,
                  "recall": round(tp / sup, 3) if sup else None,
                  "precision": round(tp / prd, 3) if prd else None}
    return tbl


def adjudicate(res, tbl, classes):
    """The six rules, applied mechanically to the numbers already computed. Rule 6 is a decision
    about what happens next, not a computation, so it is recorded rather than evaluated."""
    zero_correct = sorted(c for c, d in tbl.items()
                          if d["predicted"] > RULE2_ZERO_CORRECT_AT and d["tp"] == 0)
    # Rule 4: a class is JUDGED only when its true support reaches the floor — precision on a
    # support of 2 is noise. A judged class with zero predictions has undefined precision and
    # FAILS: never predicting a real class is not a pass.
    judged = [c for c in classes if tbl[c]["support"] >= RULE4_MIN_SUPPORT]
    below = sorted(c for c in judged
                   if (tbl[c]["precision"] or 0.0) < RULE4_MIN_PRECISION)
    r5 = res["r_predrank_vs_log_volume"]
    res["rule_detail"] = {
        "2_labels_predicted_over_10x_with_zero_correct": zero_correct,
        "4_classes_judged": judged,
        "4_classes_below_precision_floor": below,
        "4_classes_unjudged_support_under_5": sorted(set(classes) - set(judged)),
    }
    res["rules"] = {
        "1_beats_constant_by_10pts": res["lift"] >= RULE1_MIN_LIFT,
        "2_not_degenerate": (res["top_predicted_share"] <= RULE2_MAX_PRED_SHARE
                             and not zero_correct),
        "3_coverage_at_least_60pct": res["coverage"] >= RULE3_MIN_COVERAGE,
        "4_per_class_precision_floor": bool(judged) and not below,
        "5_not_a_size_bucket": r5 is not None and abs(r5) < RULE5_MAX_R,
    }
    res["passes_all_rules"] = all(res["rules"].values())
    return res


def score_one(name, kind, rows, truth, pred, reason, classes, rank=None):
    answered = [w for w in sorted(rows) if pred[w] is not None]
    hits = [w for w in answered if pred[w] == truth[w]]
    tdist = collections.Counter(truth[w] for w in answered)
    pdist = collections.Counter(pred[w] for w in answered)
    acc = len(hits) / len(answered) if answered else 0.0
    maj = max(tdist.values()) / len(answered) if answered else 0.0
    rank = RANK if rank is None else rank
    lv = [math.log1p(rows[w]["volume"]) for w in answered]
    r_pred = pearson([rank[pred[w]] for w in answered], lv)
    r_truth = pearson([rank[truth[w]] for w in answered], lv)
    tbl = per_class(answered, truth, pred, classes)
    res = {
        "mapping": name, "kind": kind, "classes": list(classes),
        "n_sampled": len(rows), "n_answered": len(answered),
        "coverage": round(len(answered) / len(rows), 3) if rows else 0.0,
        "abstention_reasons": dict(collections.Counter(
            reason[w] for w in rows if pred[w] is None)),
        "truth_distribution_all": dict(collections.Counter(truth.values()).most_common()),
        "truth_distribution_answered": dict(tdist.most_common()),
        "majority_class_answered": (tdist.most_common(1) or [(None, 0)])[0][0],
        "majority_baseline_on_answered": round(maj, 3),
        "accuracy_on_answered": round(acc, 3),
        "lift": round(acc - maj, 3),
        "accuracy_over_all_sampled": round(len(hits) / len(rows), 3) if rows else 0.0,
        "predicted_distribution": dict(pdist.most_common()),
        "top_predicted_share": round(max(pdist.values()) / len(answered), 3) if answered else 0.0,
        "r_predrank_vs_log_volume": None if r_pred is None else round(r_pred, 3),
        "r_truthrank_vs_log_volume": None if r_truth is None else round(r_truth, 3),
        "eta_pred_vs_log_volume": None if (e := eta(
            [pred[w] for w in answered], lv)) is None else round(e, 3),
        "eta_truth_vs_log_volume": None if (e := eta(
            [truth[w] for w in answered], lv)) is None else round(e, 3),
        "mean_log_volume_by_predicted": {
            c: round(sum(math.log1p(rows[w]["volume"]) for w in answered if pred[w] == c)
                     / max(1, pdist[c]), 2) for c, _ in pdist.most_common()},
        "per_class": tbl,
        "confusion": {},
        "errors": [],
    }
    for w in answered:
        res["confusion"].setdefault(truth[w], {}).setdefault(pred[w], 0)
        res["confusion"][truth[w]][pred[w]] += 1
    # NAMED examples with their feature profile, never the aggregate alone: roughly twenty defects
    # in this series surfaced as plausible wrong numbers and essentially none was caught by
    # reading a mean.
    for w in sorted(answered):
        if pred[w] != truth[w]:
            r = rows[w]
            res["errors"].append({
                "wid": w, "truth": truth[w], "pred": pred[w],
                "n_actions": r["n_actions"], "volume": r["volume"],
                "actions": dict(sorted(r["actions"].items(), key=lambda kv: -kv[1])[:8]),
                "first_prompt": (r["prompts"] or [""])[0][:220]})
    # Abstained windows are a result too — which classes the mapping cannot reach.
    res["abstained_truth_distribution"] = dict(collections.Counter(
        truth[w] for w in rows if pred[w] is None).most_common())
    return adjudicate(res, tbl, classes)


def report(res):
    print(f"\n{'=' * 78}\n{res['mapping']}   [{res['kind']}]\n{'=' * 78}")
    # Rule 1 requires the baseline STATED BEFORE the accuracy. Printed in that order on purpose.
    for k in ("n_sampled", "n_answered", "coverage", "abstention_reasons",
              "truth_distribution_all", "truth_distribution_answered",
              "majority_class_answered", "majority_baseline_on_answered",
              "accuracy_on_answered", "lift", "accuracy_over_all_sampled",
              "predicted_distribution", "top_predicted_share",
              "r_predrank_vs_log_volume", "r_truthrank_vs_log_volume",
              "eta_pred_vs_log_volume", "eta_truth_vs_log_volume",
              "mean_log_volume_by_predicted", "abstained_truth_distribution"):
        print(f"  {k:34s} {res[k]}")
    print("\n  per class      support  predicted    tp   recall  precision")
    for c, d in res["per_class"].items():
        print(f"    {c:12s} {d['support']:7d} {d['predicted']:10d} {d['tp']:5d}   "
              f"{str(d['recall']):6s}  {d['precision']}")
    print()
    for k, v in res["rule_detail"].items():
        print(f"  detail {k:52s} {v}")
    for k, ok in res["rules"].items():
        print(f"  RULE {k:44s} {'PASS' if ok else 'FAIL'}")
    print(f"  {'VERDICT (rules 1-5)':49s} "
          f"{'PASS' if res['passes_all_rules'] else 'FAIL'}   errors={len(res['errors'])}")


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
    assert len(rows) == N_SAMPLE, f"{len(rows)} windows in the sample, expected {N_SAMPLE}"

    pred, reason = {}, {}
    for wid, r in rows.items():
        a = activity(_rl(r))
        pred[wid], reason[wid] = a.value, a.reason

    primary = score_one("activity.activity (4a64c9b, byte-unchanged)", "PRIMARY",
                        rows, truth, pred, reason, ACTIVITIES)
    report(primary)

    # DECLARED DIAGNOSTIC, NOT PROMOTABLE. Collapsing generate+transform is attempt one's post-hoc
    # collapse A: it measures how much of the failure is the one contentious labelling call
    # (editing code to add a feature is `generate`, not `transform`). It is a FIVE-value
    # vocabulary, so it is not `activity_type` and cannot ship as it — the primary is fixed above
    # before either number exists, exactly so that promoting a variant afterwards is unavailable.
    coll = {"generate": "authoring", "transform": "authoring"}
    cls5 = ("authoring", "analyze", "retrieve", "converse", "review")
    rank5 = dict(RANK)
    rank5["authoring"] = 0
    diag = score_one("collapse_gen_tra (generate+transform as one class)",
                     "DIAGNOSTIC — five-value vocabulary, NOT activity_type, not promotable",
                     rows, {w: coll.get(t, t) for w, t in truth.items()},
                     {w: coll.get(p, p) for w, p in pred.items()}, reason, cls5,
                     rank=rank5)
    report(diag)

    json.dump({"seed": SEED, "n_sample": N_SAMPLE, "span": SPAN, "stride": STRIDE,
               "expected_windows": EXPECTED_WINDOWS,
               "thresholds": {"rule1_min_lift": RULE1_MIN_LIFT,
                              "rule2_max_pred_share": RULE2_MAX_PRED_SHARE,
                              "rule2_zero_correct_at": RULE2_ZERO_CORRECT_AT,
                              "rule3_min_coverage": RULE3_MIN_COVERAGE,
                              "rule4_min_precision": RULE4_MIN_PRECISION,
                              "rule4_min_support": RULE4_MIN_SUPPORT,
                              "rule5_max_r": RULE5_MAX_R},
               "results": [primary, diag]}, open(out, "w"), indent=1)
    print(f"\nfull report -> {out}")


def main():
    here = os.path.dirname(os.path.abspath(__file__))
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    b = sub.add_parser("frame"); b.add_argument("--roots", nargs="+", required=True)
    b.add_argument("-o", required=True)
    s = sub.add_parser("sample"); s.add_argument("frame"); s.add_argument("-o", required=True)
    s.add_argument("--prior-dir", default=os.path.expanduser(
        "~/keld/refseries-context/facets"))
    d = sub.add_parser("dump"); d.add_argument("sample"); d.add_argument("-o", required=True)
    c = sub.add_parser("score"); c.add_argument("sample"); c.add_argument("labels")
    c.add_argument("-o", required=True)
    v = sub.add_parser("verify"); v.add_argument("frame")
    a = ap.parse_args()
    if a.cmd == "frame":
        build(a.roots, a.o)
    elif a.cmd == "sample":
        sample(a.frame, a.o, a.prior_dir)
    elif a.cmd == "dump":
        dump(a.sample, a.o)
    elif a.cmd == "verify":
        verify(a.frame)
    else:
        score(a.sample, a.labels, a.o)


if __name__ == "__main__":
    main()
