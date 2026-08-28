#!/usr/bin/env python3
"""Tests for the act/artifact harness (`scripts/act_artifact.py`).

Standalone script with a `__main__` runner, never pytest — the repo convention (AGENTS.md).

Every load-bearing behaviour gets a test that BITES, and `--mutations` proves it: each mutation
breaks exactly one mechanism and the runner asserts the matching test then FAILS. A test that
still passes under its own mutation is reported as not biting — which is worse than a failing
test, because it looks fine. Every work unit on this branch found a test passing vacuously.

    PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/test_act_artifact.py
    PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/test_act_artifact.py --mutations
"""
import argparse, contextlib, math, os, sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))
import act_artifact as A
from app.analysis.window import MIN_EVIDENCE, REASONS


def _w(wid, action=None, artifact=None, **kw):
    """A synthetic window record shaped exactly like `windows_of`'s output, with `action` and
    `output_type` derived through the PRODUCTION `attribution` rather than hand-set — so a test
    that says "this window is no_majority" is asserting about the real rule, not about a literal
    I typed."""
    from app.analysis.window import attribution
    rl = {}
    # `A.MIN_EVIDENCE`, not the alias this module imported: the claim under test is that the
    # FRAME BUILDER's floor is the production constant, so the mutation must be able to reach it.
    if action:
        rl["action"] = sorted(action.items(), key=lambda kv: (-kv[1], kv[0]))
    if artifact:
        rl["artifact"] = sorted(artifact.items(), key=lambda kv: (-kv[1], kv[0]))
    rec = {"wid": wid, "file": "/f/" + wid, "fid": 0, "prefix": wid[:8],
           "start": "2026-08-01T00:00:00+00:00", "n_turns": 1, "volume": 10,
           "action_counts": dict(action or {}), "artifact_counts": dict(artifact or {}),
           "levels": sorted(rl)}
    for name, level, floor in A.ALLOCATION + [("action", "action", A.FLOOR)]:
        a = attribution(rl, level, floor, A.MIN_EVIDENCE)
        rec[name] = {"value": a.value, "share": round(a.share, 4), "evidence": a.evidence,
                     "reason": a.reason}
    rec.update(kw)
    return rec


# ------------------------------------------------------------------ 1. the void-measurement guard

def test_the_vocabulary_guard_accepts_the_corrected_vocabulary():
    """Preregistration rule 4: a measurement on the pre-`4ad9add` vocabulary is VOID. The guard
    must pass on the code as committed, or every run is blocked for the wrong reason."""
    A.assert_vocab_fixed()


def test_the_vocabulary_guard_bites_on_each_part_of_the_fix():
    """Three probes, three independent regressions. Each is reverted in isolation, so a guard that
    only checks one of them is caught."""
    import app.analysis.vocab as V
    reverts = {
        "sed in a read pipeline -> transform": lambda **k: "transform",
        "pnpm run test -> run a service": lambda tool=None, exe=None, verb=None, args=(): (
            "run a service" if exe == "pnpm" else V.action_for(tool=tool, exe=exe, verb=verb,
                                                               args=args)),
        "heredoc write invisible": lambda tool=None, exe=None, verb=None, args=(): (
            None if any(str(a).startswith("<<") for a in args)
            else V.action_for(tool=tool, exe=exe, verb=verb, args=args)),
    }
    for label, fn in reverts.items():
        with _patch("action_for", fn):
            try:
                A.assert_vocab_fixed()
            except AssertionError:
                continue
            raise AssertionError(f"the guard did NOT bite on: {label}")


# ------------------------------------------------------------------ 2. the frame

def test_the_frame_count_assertion_bites_on_the_session_collision_merge():
    """The known trap: `basename(path)[:8]` collides for 445 of 500 files, and a study keyed on it
    reported 550 windows against a true 1,022 with no error raised. Rebuild that exact shape — two
    files sharing a prefix, merged to one key — and the assertion must fail."""
    recs = [_w("aaaaaaaa#t0000-A"), _w("aaaaaaaa#t0001-A")]
    recs[1]["file"] = "/f/two"
    per_file = {"/f/aaaaaaaa#t0000-A": 1, "/f/two": 1}
    files = ["/f/aaaaaaaa-one.jsonl", "/f/aaaaaaaa-two.jsonl"]
    try:
        A.assert_frame(recs, per_file, files)
    except AssertionError:
        return
    raise AssertionError("assert_frame accepted a merged frame")


def test_the_frame_assertion_refuses_to_run_when_the_collision_trap_is_dead():
    """If every prefix is unique the collision check proves nothing, and a frame that silently
    stopped testing for the merge is exactly how the merge got through. That must be an error, not
    a quiet pass."""
    recs = [_w("aaaaaaaa#t0000-A"), _w("bbbbbbbb#t0001-A")]
    recs[1]["file"], recs[1]["prefix"] = "/f/two", "bbbbbbbb"
    recs[1]["start"] = "2026-08-02T00:00:00+00:00"
    per_file = {"/f/aaaaaaaa#t0000-A": 1, "/f/two": 1}
    try:
        A.assert_frame(recs, per_file, files=["/f/a.jsonl", "/f/b.jsonl"])
    except AssertionError as e:
        assert "not testing anything" in str(e), e
        return
    raise AssertionError("assert_frame passed with a dead collision trap")


def test_the_expected_window_count_is_asserted_not_printed():
    """1,022 is an ASSERTION. A frame of the right shape but the wrong size must fail."""
    # Two files must share BOTH prefix and start, or `by_prefix == n` and the dead-trap
    # assertion fires first — which would make this test pass for the wrong reason.
    recs, per_file, files = [], {}, []
    for i, (pre, start) in enumerate((("aaaaaaaa", "2026-08-01T00:00:00+00:00"),
                                      ("aaaaaaaa", "2026-08-01T00:00:00+00:00"),
                                      ("bbbbbbbb", "2026-08-02T00:00:00+00:00"))):
        r = _w(f"{pre}#t{i:04d}-A")
        r["file"], r["prefix"], r["start"] = f"/f/{i}", pre, start
        recs.append(r)
        per_file[f"/f/{i}"] = 1
        files.append(f"/f/{pre}-{i}.jsonl")
    try:
        A.assert_frame(recs, per_file, files)
    except AssertionError as e:
        assert str(A.EXPECTED_WINDOWS) in str(e), e
        return
    raise AssertionError("a 3-window frame was accepted as the 1,022-window frame")


# ------------------------------------------------------------------ 3. Q1: absent is not no_majority

def test_absent_is_reported_separately_from_no_majority():
    """The preregistration's rule 2, and the whole reason `window.REASONS` has five entries. A
    level that never fired and a level that fired without a winner are DIFFERENT FACTS; pooling
    them is what would make a thin facet look like a confident one."""
    recs = [_w("a", action=None),                                   # absent
            _w("b", action={"read": 5, "search": 5}),               # tie
            _w("c", action={"read": 4, "search": 3, "edit": 3}),    # no_majority (10 obs)
            _w("d", action={"read": 2}),                            # thin
            _w("e", action={"read": 9, "search": 1})]               # attributed
    c = A.coverage(recs, "action")
    assert c["reasons"] == {"attributed": 1, "absent": 1, "thin": 1, "tie": 1,
                            "no_majority": 1}, c["reasons"]
    assert c["coverage"] == 0.2, c["coverage"]
    assert set(c["reasons"]) == set(REASONS), "a reason vanished from the table"
    # The two that must never merge:
    assert c["reasons"]["absent"] != 0 and c["reasons"]["no_majority"] != 0
    assert c["reason_share"]["absent"] == 0.2 and c["reason_share"]["no_majority"] == 0.2


def test_coverage_counts_only_attributed_windows():
    recs = [_w(str(i), action={"read": 9, "search": 1}) for i in range(7)]
    recs += [_w("x", action={"read": 4, "search": 4, "edit": 2})]
    recs += [_w("y", action=None), _w("z", action={"read": 1})]
    c = A.coverage(recs, "action")
    assert c["coverage"] == 0.7, c["coverage"]
    assert c["distinct_values"] == 1, c["distinct_values"]


def test_the_evidence_floor_is_the_production_constant_not_a_local_number():
    """`MIN_EVIDENCE == 5` arrives with `window.attribution`. A window of 4 unanimous observations
    must be `thin`, not attributed at share 1.0 — the exact defect that constant exists for."""
    assert A.MIN_EVIDENCE == MIN_EVIDENCE == 5, (A.MIN_EVIDENCE, MIN_EVIDENCE)
    a4 = _w("a", action={"read": 4})["action"]
    assert a4["reason"] == "thin" and a4["value"] is None, a4
    assert a4["share"] == 1.0, "share is still reported for an unattributed window"
    a5 = _w("b", action={"read": 5})["action"]
    assert a5["reason"] == "attributed" and a5["value"] == "read" and a5["share"] == 1.0, a5


# ------------------------------------------------------------------ 4. Q2: the pair population

def test_a_pair_needs_both_halves_attributed():
    """A pair with one half unattributed is not a pair. Inventing a placeholder half would
    manufacture mass for a cell nothing observed."""
    recs = [_w("both", action={"read": 9, "x": 1}, artifact={"code": 9, "y": 1}),
            _w("act_only", action={"read": 9, "x": 1}, artifact={"code": 3, "y": 3}),
            _w("art_only", action={"read": 4, "x": 4}, artifact={"code": 9, "y": 1}),
            _w("neither", action=None, artifact=None)]
    pr = A.pairs(recs)
    assert [(a, b) for a, b, _ in pr] == [("read", "code")], pr
    assert [w for _a, _b, w in pr] == ["both"], pr


def test_concentration_reports_top_share_and_the_three_percent_count():
    pr = [("read", "code", f"w{i}") for i in range(60)]
    pr += [("edit", "code", f"e{i}") for i in range(20)]
    pr += [("search", "prose", f"s{i}") for i in range(10)]
    pr += [("test", "code", f"t{i}") for i in range(7)]
    pr += [("build", "config", f"b{i}") for i in range(3)]
    co = A.concentration(pr)
    assert co["n_attributed"] == 100 and co["distinct_pairs"] == 5, co
    assert co["top_share"] == 0.6, co["top_share"]
    assert co["ranked"][0]["pair"] == "read x code", co["ranked"][0]
    assert co["pairs_over_min"] == 5, co["pairs_over_min"]   # 3 windows == exactly 3%
    assert A.concentration(pr[:-1])["pairs_over_min"] == 4


# ------------------------------------------------------------------ 5. Q3: mutual information

def test_mutual_information_is_zero_on_an_independent_joint():
    """A product distribution carries no conjunction. Built as a full 2x2 cross with equal mass,
    so the joint is EXACTLY the product of the marginals."""
    pr = [(a, b, f"{a}{b}{i}") for a in ("read", "edit") for b in ("code", "prose")
          for i in range(25)]
    mi = A.mutual_information(pr)
    assert abs(mi["mi_bits"]) < 1e-9, mi["mi_bits"]
    for k, v in mi["nmi"].items():
        assert abs(v) < 1e-9, (k, v)
    assert abs(mi["cramers_v"]) < 1e-9, mi["cramers_v"]


def test_mutual_information_is_one_on_a_bijection():
    """A perfect conjunction: each act occurs with exactly one artifact. NMI must be 1.0 under
    every normalisation, so a mis-wired denominator cannot hide."""
    # FOUR categories, not two: a 2x2 bijection has MI == 1.0 bits AND NMI == 1.0, so dropping
    # the denominator would be invisible. At 4x4, MI is 2.0 bits and NMI is still 1.0.
    acts = ("read", "edit", "search", "test")
    arts = ("code", "prose", "data", "config")
    pr = [(a, b, f"{a}{i}") for a, b in zip(acts, arts) for i in range(25)]
    mi = A.mutual_information(pr)
    assert abs(mi["mi_bits"] - 2.0) < 1e-9, mi["mi_bits"]
    assert abs(mi["h_act"] - 2.0) < 1e-9 and abs(mi["h_artifact"] - 2.0) < 1e-9, mi
    for k, v in mi["nmi"].items():
        assert abs(v - 1.0) < 1e-9, (k, v)


def test_mutual_information_attributes_its_mass_to_the_cells_that_carry_it():
    """A conjunction carried by cells nothing observed more than once or twice is an artefact of
    the estimator, not a property of the work — so the share of MI coming from cells under
    MIN_EVIDENCE is reported, and it must actually track those cells."""
    pr = ([("read", "code", f"r{i}") for i in range(90)]
          + [("edit", "prose", "e0")] + [("edit", "code", "e1")])
    mi = A.mutual_information(pr)
    assert mi["cells_under_min_evidence"] == 2, mi["cells_under_min_evidence"]
    # 0.76 on this example, not 0.9: `read x code` is 90 of 92 windows but its own cell still
    # carries a little MI. The claim under test is that the MAJORITY of the statistic rides on
    # cells nothing observed five times — which is exactly the objection Q3 needs.
    assert mi["mi_share_from_thin_cells"] > 0.6, mi["mi_share_from_thin_cells"]
    clean = A.mutual_information([("read", "code", f"r{i}") for i in range(90)])
    assert clean["cells_under_min_evidence"] == 0 and clean["mi_bits"] == 0.0, clean


def test_the_permutation_null_is_deterministic_and_destroys_the_conjunction():
    """The bias correction. Shuffling one side against the other preserves both marginals and
    destroys any real association, so the null's median NMI must sit far below a true bijection's
    1.0 — and the same seed must give the same number twice, or RESULTS.md is not reproducible."""
    pr = ([("read", "code", f"r{i}") for i in range(50)]
          + [("edit", "prose", f"e{i}") for i in range(50)])
    a = A.mi_null(pr, trials=200)
    b = A.mi_null(pr, trials=200)
    assert a == b, "mi_null is not deterministic under its seed"
    assert a["observed"] == 1.0, a["observed"]
    assert a["null_p50"] < 0.2, a["null_p50"]
    # (0 + 1) / (200 + 1) == 0.004975, rounded to 4 places by mi_null itself.
    assert a["p_value"] == round(1.0 / 201, 4), a["p_value"]


# ------------------------------------------------------------------ 6. why, not just whether

def test_the_floor_sweep_separates_a_near_miss_from_a_diffuse_level():
    """A reason tally says `no_majority`; it does not say whether the top value missed by a point
    or by half. Two synthetic levels with the SAME reason tally must come out differently here."""
    # 49/100 == 0.49: one point under the floor. `{"read": 49, "search": 51}` would be
    # ATTRIBUTED at 0.51 and is not a near miss at all.
    near = [_w(f"n{i}", action={"read": 49, "search": 48, "edit": 3}) for i in range(10)]
    # A distinct top far below the floor — NOT ten equal counts, which `attribution` calls a
    # `tie` (evidence outranks shape there, deliberately) and which would test the wrong branch.
    diffuse = [_w(f"d{i}", action=dict({"a": 19}, **{c: 9 for c in "bcdefghij"}))
               for i in range(10)]
    for r in near + diffuse:
        assert r["action"]["reason"] == "no_majority", r["action"]
    dn = A.diffuseness(near, "action", "action_counts")
    dd = A.diffuseness(diffuse, "action", "action_counts")
    assert dn["coverage_by_floor"]["0.45"] == 1.0, dn["coverage_by_floor"]
    assert dd["coverage_by_floor"]["0.30"] == 0.0, dd["coverage_by_floor"]
    assert dn["values_per_window_p50"] == 3 and dd["values_per_window_p50"] == 10
    assert dn["top_share_p50"] > dd["top_share_p50"]


def test_the_floor_sweep_is_monotone_and_agrees_with_coverage_at_the_real_floor():
    """The sweep is diagnostic, but it must be the SAME statistic: at 0.50 it has to reproduce
    `coverage` exactly, or the curve says nothing about the verdict it sits beside."""
    recs = [_w("a", action={"read": 9, "x": 1}), _w("b", action={"read": 4, "x": 3, "y": 3}),
            _w("c", action=None), _w("d", action={"read": 3}),
            _w("e", action={"read": 6, "x": 4}), _w("f", action={"read": 5, "x": 5})]
    d = A.diffuseness(recs, "action", "action_counts")
    seq = [d["coverage_by_floor"][k] for k in sorted(d["coverage_by_floor"])]
    assert seq == sorted(seq, reverse=True), seq
    assert d["coverage_by_floor"]["0.50"] == A.coverage(recs, "action")["coverage"], (
        d["coverage_by_floor"]["0.50"], A.coverage(recs, "action")["coverage"])


# ------------------------------------------------------------------ 7. the pre-registered gate

def test_q1_failing_ships_nothing_whatever_q2_and_q3_say():
    """The preregistration's outcome table, applied as written: Q1 is the gate. A Q3 that passes
    on a population that barely exists must not ship anything."""
    q1 = {"coverage": 0.185}
    q2 = {"top_share": 0.10, "pairs_over_min": 9}
    q3 = {"nmi": {"arithmetic": 0.9, "geometric": 0.9, "min": 0.9, "max": 0.9}}
    v = A.verdicts(q1, q2, q3)
    assert not v["Q1"]["pass"] and v["Q2"]["pass"] and v["Q3"]["pass"], v
    assert v["ships"].startswith("nothing"), v["ships"]


def test_q1_alone_ships_the_dimension_and_not_the_pair():
    q1 = {"coverage": 0.75}
    q2 = {"top_share": 0.90, "pairs_over_min": 2}
    q3 = {"nmi": {"arithmetic": 0.02, "geometric": 0.02, "min": 0.02, "max": 0.02}}
    v = A.verdicts(q1, q2, q3)
    assert v["ships"].startswith("action alone"), v["ships"]
    assert "NOT the pair" in v["ships"], v["ships"]


def test_all_three_ship_the_pair():
    v = A.verdicts({"coverage": 0.75}, {"top_share": 0.40, "pairs_over_min": 6},
                   {"nmi": {k: 0.4 for k in ("arithmetic", "geometric", "min", "max")}})
    assert v["Q1"]["pass"] and v["Q2"]["pass"] and v["Q3"]["pass"], v
    assert "pair" in v["ships"] and "routing key" in v["ships"], v["ships"]


def test_q2_needs_both_halves_of_its_bar():
    """Concentrated-but-varied and diffuse-but-sparse must both FAIL: the bar is a conjunction."""
    q3 = {"nmi": {k: 0.4 for k in ("arithmetic", "geometric", "min", "max")}}
    assert not A.verdicts({"coverage": 0.8}, {"top_share": 0.61, "pairs_over_min": 9},
                          q3)["Q2"]["pass"]
    assert not A.verdicts({"coverage": 0.8}, {"top_share": 0.20, "pairs_over_min": 4},
                          q3)["Q2"]["pass"]
    assert A.verdicts({"coverage": 0.8}, {"top_share": 0.59, "pairs_over_min": 5},
                      q3)["Q2"]["pass"]


def test_disagreement_between_nmi_normalisations_is_surfaced():
    """A bar whose verdict depends on which denominator was chosen is not a bar. If the four
    disagree the report must say so rather than quietly reporting the primary."""
    v = A.verdicts({"coverage": 0.8}, {"top_share": 0.4, "pairs_over_min": 6},
                   {"nmi": {"arithmetic": 0.11, "geometric": 0.12, "min": 0.3, "max": 0.05}})
    assert v["Q3"]["all_normalisations_agree"] is False, v["Q3"]
    v2 = A.verdicts({"coverage": 0.8}, {"top_share": 0.4, "pairs_over_min": 6},
                    {"nmi": {k: 0.2 for k in ("arithmetic", "geometric", "min", "max")}})
    assert v2["Q3"]["all_normalisations_agree"] is True, v2["Q3"]


# ------------------------------------------------------------------ 8. the real frame, if present

def test_the_committed_frame_holds_the_asserted_window_count_and_only_known_reasons():
    """Runs against the durable frame when it exists (it lives outside the repo, like the corpus).
    Skipped rather than failed when absent, so this suite is runnable anywhere."""
    if not os.path.exists(os.path.join(A.OUT, "act-frame.ndjson")):
        print("    (skipped: no durable frame on this machine)")
        return
    recs = A.load_frame(A.OUT)
    assert len(recs) == A.EXPECTED_WINDOWS
    for r in recs:
        for name, _l, _f in A.ALLOCATION + [("action", "action", A.FLOOR)]:
            assert r[name]["reason"] in REASONS, (r["wid"], name, r[name])
            if r[name]["reason"] != "attributed":
                assert r[name]["value"] is None, (r["wid"], name)
    # The level must actually be present: `absent` is the one reason that would make the whole
    # measurement vacuous, and it is 2.2% here, not 98%.
    absent = sum(1 for r in recs if r["action"]["reason"] == "absent")
    assert absent < 0.1 * len(recs), f"`action` is absent in {absent} windows — measure nothing"


# ------------------------------------------------------------------ mutations

@contextlib.contextmanager
def _patch(name, value):
    import app.analysis.vocab as V
    target = A if hasattr(A, name) else V
    old = getattr(target, name)
    setattr(target, name, value)
    try:
        yield
    finally:
        setattr(target, name, old)


def _pooled_coverage(recs, dim):
    """`absent` folded into `no_majority` — the exact conflation the preregistration forbids."""
    import collections
    by = collections.Counter(r[dim]["reason"] for r in recs)
    by["no_majority"] += by.pop("absent", 0)
    n = len(recs)
    return {"dim": dim, "n": n, "coverage": round(by["attributed"] / n, 4),
            "reasons": {k: by.get(k, 0) for k in REASONS},
            "reason_share": {k: round(by.get(k, 0) / n, 4) for k in REASONS},
            "evidence_median": 0, "distinct_values": 0}


def _lenient_pairs(recs):
    """A pair formed from the top value even when it is unattributed — placeholder mass."""
    out = []
    for r in recs:
        a = r["action"]["value"] or (max(r["action_counts"], default=None))
        b = r["output_type"]["value"] or (max(r["artifact_counts"], default=None))
        if a and b:
            out.append((a, b, r["wid"]))
    return out


def _raw_mi_as_nmi(pr):
    """NMI reported as raw MI in bits — the denominator dropped."""
    import collections
    joint = collections.Counter((a, b) for a, b, _w in pr)
    n = sum(joint.values()) or 1
    px = collections.Counter(a for a, _b, _w in pr)
    py = collections.Counter(b for _a, b, _w in pr)
    mi = sum((k / n) * math.log2((k / n) / ((px[a] / n) * (py[b] / n)))
             for (a, b), k in joint.items())
    return {"n": n, "mi_bits": round(mi, 4), "h_act": 0.0, "h_artifact": 0.0,
            "mi_bits_from_cells_under_min_evidence": 0.0, "mi_share_from_thin_cells": 0.0,
            "cells_under_min_evidence": 0,
            "nmi": {k: round(mi, 4) for k in ("arithmetic", "geometric", "min", "max")},
            "cramers_v": 0.0, "chi2": 0.0, "df": 0, "distinct_act": len(px),
            "distinct_artifact": len(py), "top_lift": []}


def _q1_not_a_gate(q1, q2, q3):
    """Q1 demoted from a gate to one vote of three — the outcome table read wrong."""
    v = {"Q1": {"bar": "x", "got": q1["coverage"], "pass": q1["coverage"] >= A.Q1_COVERAGE_MIN},
         "Q2": {"bar": "x", "got": "", "pass": q2["top_share"] < A.Q2_TOP_PAIR_MAX
                and q2["pairs_over_min"] >= A.Q2_MIN_PAIRS},
         "Q3": {"bar": "x", "got": q3["nmi"][A.NMI_PRIMARY],
                "pass": q3["nmi"][A.NMI_PRIMARY] >= A.Q3_NMI_MIN,
                "all_normalisations_agree": len({x >= A.Q3_NMI_MIN
                                                 for x in q3["nmi"].values()}) == 1}}
    n = sum(1 for q in ("Q1", "Q2", "Q3") if v[q]["pass"])
    v["ships"] = ("action AND the (act, artifact) pair as a routing key" if n >= 2
                  else "action alone, as an eighth ALLOCATION dimension. NOT the pair."
                  if n == 1 else "nothing.")
    return v


def _seeded_null(pr, trials=2000, seed=20260824):
    """A permutation null that permutes nothing — the bias correction disabled."""
    obs = A.mutual_information(pr)["nmi"][A.NMI_PRIMARY]
    return {"trials": trials, "observed": round(obs, 4), "null_p50": round(obs, 4),
            "null_p95": round(obs, 4), "null_max": round(obs, 4), "p_value": 1.0,
            "null_reaches_bar_rate": 1.0}


def _wrap(orig, fn):
    """Bind the UNPATCHED function into a mutation, so the mutation degrades it instead of
    recursing into its own patch. A mutation that raises RecursionError "bites", but for the wrong
    reason — it never exercises the behaviour it claims to break, which is the same vacuous pass
    this suite exists to catch."""
    return lambda *a, **k: fn(orig, *a, **k)


def _flat_sweep(orig, recs, dim, level_counts):
    """A floor sweep that ignores the floor: every entry is the coverage at 0.50."""
    real = orig(recs, dim, level_counts)
    at50 = real["coverage_by_floor"]["0.50"]
    real["coverage_by_floor"] = {k: at50 for k in real["coverage_by_floor"]}
    real["top_share_p50"] = 0.0
    real["values_per_window_p50"] = 0
    return real


MUTATIONS = [
    ("the vocabulary guard is a no-op (a void measurement runs)",
     lambda: _patch("assert_vocab_fixed", lambda: None),
     "test_the_vocabulary_guard_bites_on_each_part_of_the_fix"),
    ("the frame count is printed, never asserted",
     lambda: _patch("assert_frame", lambda recs, per_file, files: None),
     "test_the_frame_count_assertion_bites_on_the_session_collision_merge"),
    ("the collision trap is allowed to be dead",
     lambda: _patch("assert_frame", lambda recs, per_file, files: (_ for _ in ()).throw(
         AssertionError("frame is 2 windows, expected 1022"))),
     "test_the_frame_assertion_refuses_to_run_when_the_collision_trap_is_dead"),
    ("absent is folded into no_majority",
     lambda: _patch("coverage", _pooled_coverage),
     "test_absent_is_reported_separately_from_no_majority"),
    ("a pair is formed from unattributed halves",
     lambda: _patch("pairs", _lenient_pairs),
     "test_a_pair_needs_both_halves_attributed"),
    ("the 3% pair share is read as 3 windows, not 3 percent",
     lambda: _patch("Q2_PAIR_SHARE_MIN", 3),
     "test_concentration_reports_top_share_and_the_three_percent_count"),
    ("NMI loses its denominator and reports raw bits",
     lambda: _patch("mutual_information", _raw_mi_as_nmi),
     "test_mutual_information_is_one_on_a_bijection"),
    ("MI's thin-cell attribution is switched off",
     lambda: _patch("mutual_information", _raw_mi_as_nmi),
     "test_mutual_information_attributes_its_mass_to_the_cells_that_carry_it"),
    ("the permutation null permutes nothing",
     lambda: _patch("mi_null", _seeded_null),
     "test_the_permutation_null_is_deterministic_and_destroys_the_conjunction"),
    ("the floor sweep ignores the floor",
     lambda: _patch("diffuseness", _wrap(A.diffuseness, _flat_sweep)),
     "test_the_floor_sweep_separates_a_near_miss_from_a_diffuse_level"),
    ("the sweep no longer reproduces coverage at 0.50",
     lambda: _patch("diffuseness", _wrap(A.diffuseness, lambda orig, r, d, lc: dict(
         orig(r, d, lc), coverage_by_floor={k: 0.99 for k in
                                            ("0.30", "0.35", "0.40", "0.45",
                                             "0.50", "0.55", "0.60")}))),
     "test_the_floor_sweep_is_monotone_and_agrees_with_coverage_at_the_real_floor"),
    ("Q1 is one vote of three instead of the gate",
     lambda: _patch("verdicts", _q1_not_a_gate),
     "test_q1_failing_ships_nothing_whatever_q2_and_q3_say"),
    ("Q2's bar becomes a disjunction",
     lambda: _patch("verdicts", _wrap(A.verdicts, lambda orig, q1, q2, q3: dict(
         orig(q1, q2, q3), Q2={"bar": "x", "got": "",
                               "pass": q2["top_share"] < A.Q2_TOP_PAIR_MAX
                               or q2["pairs_over_min"] >= A.Q2_MIN_PAIRS}))),
     "test_q2_needs_both_halves_of_its_bar"),
    ("normalisation disagreement is silently swallowed",
     lambda: _patch("verdicts", _wrap(A.verdicts, lambda orig, q1, q2, q3: dict(
         orig(q1, q2, q3), Q3=dict(orig(q1, q2, q3)["Q3"],
                                   all_normalisations_agree=True)))),
     "test_disagreement_between_nmi_normalisations_is_surfaced"),
    ("the evidence floor is dropped to 1",
     lambda: _patch("MIN_EVIDENCE", 1),
     "test_the_evidence_floor_is_the_production_constant_not_a_local_number"),
]


def tests():
    return {k: v for k, v in sorted(globals().items())
            if k.startswith("test_") and callable(v)}


def run(names=None):
    failed = []
    for name, fn in tests().items():
        if names and name not in names:
            continue
        try:
            fn()
            print(f"ok   {name}")
        except Exception as e:
            failed.append(name)
            print(f"FAIL {name}: {type(e).__name__}: {e}")
    return failed


def mutate():
    """Every mutation must break its test. A mutation the suite survives is reported LOUDLY: it
    means that behaviour is untested, which is worse than a failing test because it looks fine."""
    bad = 0
    for label, patch, target in MUTATIONS:
        assert target in tests(), f"mutation names a test that does not exist: {target}"
        with patch():
            try:
                tests()[target]()
                print(f"NOT-BITING [{label}] -> {target} still passed")
                bad += 1
            except Exception as e:
                print(f"BITES      [{label}] -> {target}: {type(e).__name__}")
    print(f"\n{len(MUTATIONS) - bad}/{len(MUTATIONS)} mutations confirmed to bite")
    return bad


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--mutations", action="store_true")
    a = ap.parse_args()
    failed = run()
    print(f"\n{len(tests()) - len(failed)}/{len(tests())} passed")
    bad = mutate() if a.mutations else 0
    sys.exit(1 if failed or bad else 0)
