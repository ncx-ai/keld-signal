#!/usr/bin/env python3
"""Tests for scripts/session_prior.py — standalone, no pytest.

    PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/test_session_prior.py

Two halves, and the second is the point. The `test_*` functions assert the behaviours the study's
numbers rest on. `MUTATIONS` then replaces one behaviour at a time with a plausible WRONG
implementation — the fallback the design forbids, an inclusive causal cut, a departure taken
against the prior's dominant value instead of the window's — and requires the named test to fail
with an AssertionError. A test that passes under its own mutation is not testing anything, and a
mutation that "bites" by raising RecursionError or TypeError is testing the patch rather than the
path, so the harness demands AssertionError specifically.
"""
import json
import os
import sys
import tempfile
from datetime import datetime, timedelta

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
sys.path.insert(0, os.path.join(os.path.dirname(HERE), "sidecar"))

import session_prior as sp        # noqa: E402


def _t(minute):
    return {"type": "user", "timestamp": f"2026-08-10T09:{minute:02d}:00Z",
            "cwd": "/w/x", "message": {"role": "user", "content": "hi"}}


# ------------------------------------------------------------------ the causal cut

def test_causal_turns_is_half_open_on_the_right():
    """A turn AT the cutoff instant is not in the prior. For the `before` variant that turn is
    inside the window being characterised, and admitting it puts the window in its own frame."""
    turns = [_t(0), _t(30), _t(59)]
    cut = sp._epoch("2026-08-10T09:30:00Z")
    got = [o["timestamp"] for o in sp.causal_turns(turns, cut)]
    assert got == ["2026-08-10T09:00:00Z"], f"cutoff turn leaked into the prior: {got}"
    assert sp.causal_turns(turns, sp._epoch("2026-08-10T09:00:00Z")) == [], \
        "a prior at the session's first instant must be empty"


# ------------------------------------------------------------------ the prior itself

def _rl(level, pairs):
    return {level: sorted(pairs, key=lambda kv: (-kv[1], kv[0]))}


def test_prior_never_fills_an_unattributed_level():
    """`thin` and `no_majority` priors report the REASON and keep share/evidence, and carry NO
    value. That is `contrast, never fallback` at the prior end: a prior with no dominant value
    must not hand one out."""
    thin = sp.prior_at(_rl("lang", [("Go", 2), ("Python", 1)]), "language")
    assert thin["reason"] == "thin", thin["reason"]
    assert thin["value"] is None, f"a thin prior supplied a value: {thin['value']!r}"
    assert thin["evidence"] == 3 and thin["share"] > 0, "share/evidence must survive"
    assert [t["value"] for t in thin["top"]] == ["Go", "Python"], thin["top"]

    mixed = sp.prior_at(_rl("lang", [("Go", 5), ("Python", 4), ("Rust", 3)]), "language")
    assert mixed["reason"] == "no_majority", mixed["reason"]
    assert mixed["value"] is None, "a no_majority prior supplied a value"

    ok = sp.prior_at(_rl("lang", [("Go", 9), ("Python", 1)]), "language")
    assert ok["reason"] == "attributed" and ok["value"] == "Go", ok
    assert sp.prior_at({}, "language")["reason"] == "absent", "an empty level is `absent`"


# ------------------------------------------------------------------ the contrast

WIN = {"value": "Python", "share": 0.576, "evidence": 12, "reason": "attributed"}
UNATTRIBUTED = {"value": None, "share": 0.4, "evidence": 12, "reason": "no_majority"}


def test_contrast_is_all_none_when_the_window_has_no_value():
    """The rule the whole design turns on: an unattributed window stays unattributed. No agrees,
    no departure, no novel — nothing for a reader to mistake for an answer."""
    prior = sp.prior_at(_rl("lang", [("TypeScript", 90), ("Go", 10)]), "language")
    c = sp.contrast(UNATTRIBUTED, prior, {"TypeScript": 90, "Go": 10})
    for k in ("agrees", "departure", "novel"):
        assert c[k] is None, f"{k} was supplied for an unattributed window: {c[k]!r}"


def test_departure_is_measured_against_the_windows_own_value():
    """`departure` = window share − the session's share OF THE WINDOW'S VALUE. Taking it against
    the prior's DOMINANT share instead compares two different values and would report a
    TypeScript-dominant session as 'barely departing' from a Python window."""
    counts = {"TypeScript": 90, "Python": 10}
    prior = sp.prior_at(_rl("lang", list(counts.items())), "language")
    c = sp.contrast(WIN, prior, counts)
    assert abs(c["prior_share_of_window_value"] - 0.10) < 1e-9, c
    assert abs(c["departure"] - (0.576 - 0.10)) < 1e-4, \
        f"departure {c['departure']} is not window share minus the session's Python share"
    assert c["departure"] > 0.4, "the excursion must read as a large positive departure"


def test_agrees_is_undefined_rather_than_false_when_the_prior_is_unattributed():
    """A `no_majority` prior has no value to agree with. Scoring it as disagreement would count
    the session's own ambiguity as the window departing from it — and the spec says a
    `no_majority` prior is itself informative, not a missing one."""
    counts = {"TypeScript": 5, "Python": 5, "Go": 4}
    prior = sp.prior_at(_rl("lang", list(counts.items())), "language")
    assert prior["reason"] in ("tie", "no_majority"), prior["reason"]
    c = sp.contrast(WIN, prior, counts)
    assert c["agrees"] is None, f"agrees was decided against an unattributed prior: {c['agrees']}"
    assert c["departure"] is not None, "departure is still defined — the counts exist"

    ok_counts = {"Python": 90, "Go": 10}
    ok = sp.contrast(WIN, sp.prior_at(_rl("lang", list(ok_counts.items())), "language"),
                     ok_counts)
    assert ok["agrees"] is True, ok
    ts_counts = {"TypeScript": 90, "Python": 10}
    no = sp.contrast(WIN, sp.prior_at(_rl("lang", list(ts_counts.items())), "language"),
                     ts_counts)
    assert no["agrees"] is False, no


def test_novel_requires_absence_and_an_empty_prior_is_not_yield():
    """`novel` is the value having no presence in the session at all. Against an EMPTY prior
    every value is trivially novel, which is not yield — it is a session with no history yet, and
    it is reported as `prior_empty` so it cannot inflate bar 2."""
    counts = {"TypeScript": 90, "Go": 10}
    prior = sp.prior_at(_rl("lang", list(counts.items())), "language")
    c = sp.contrast(WIN, prior, counts)
    assert c["novel"] is True, "Python is absent from the session and must read as novel"
    assert c["prior_empty"] is False, c

    present = {"TypeScript": 90, "Python": 10}
    c2 = sp.contrast(WIN, sp.prior_at(_rl("lang", list(present.items())), "language"), present)
    assert c2["novel"] is False, "a value the session has seen is not novel"

    empty = sp.contrast(WIN, sp.prior_at({}, "language"), {})
    assert empty["prior_empty"] is True, empty
    assert empty["novel"] is None, "novelty against an empty prior must not be counted as yield"


# ------------------------------------------------------------------ aggregation

def _rec(wid, volume, w, prior_counts, variant="before"):
    prior = sp.prior_at(_rl("lang", list(prior_counts.items())), "language")
    rec = {"wid": wid, "file": f"/c/{wid}.jsonl", "start": wid, "volume": volume,
           "language": {"window": w}}
    rec["language"][variant] = {"prior": prior, "contrast": sp.contrast(w, prior, prior_counts)}
    return rec


def test_agreement_denominator_is_windows_where_both_sides_are_attributed():
    """Windows whose prior is unattributed are OUT of the denominator, not counted as
    disagreements. Folding them in would make agreement a function of prior coverage — bar 3's
    question — rather than of the contrast."""
    recs = [
        _rec("a", 100, {"value": "Go", "share": 0.9, "evidence": 10, "reason": "attributed"},
             {"Go": 90, "Python": 10}),                       # agrees
        _rec("b", 100, WIN, {"TypeScript": 90, "Go": 10}),     # disagrees
        _rec("c", 100, WIN, {"Go": 5, "Python": 5, "Rust": 4}),  # prior unattributed -> out
        _rec("d", 100, UNATTRIBUTED, {"Go": 90}),              # window unattributed -> out
    ]
    a = sp.agreement(recs, "language", "before")
    assert a["n_both_attributed"] == 2, f"denominator was {a['n_both_attributed']}, want 2"
    assert a["agree"] == 1 and a["disagree"] == 1, a
    assert abs(a["agreement"] - 0.5) < 1e-9, a


def test_novelty_denominator_excludes_windows_with_no_prior_evidence():
    """A session's first window has an empty prior; counting it as novel would report the
    absence of a session as the discovery of one."""
    recs = [
        _rec("a", 100, WIN, {"TypeScript": 90, "Go": 10}),   # novel
        _rec("b", 100, WIN, {"Python": 50, "Go": 50}),       # not novel
        _rec("c", 100, WIN, {}),                             # empty prior -> out
    ]
    n = sp.novelty(recs, "language", "before")
    assert n["n_window_attributed"] == 3, n
    assert n["n_with_prior_evidence"] == 2, f"denominator was {n['n_with_prior_evidence']}, want 2"
    assert n["prior_empty"] == 1 and n["novel"] == 1, n
    assert abs(n["novelty"] - 0.5) < 1e-9, n
    assert n["examples"] and n["examples"][0]["wid"] == "a", n["examples"]


def test_pearson():
    assert abs(sp.pearson([1, 2, 3, 4], [2, 4, 6, 8]) - 1.0) < 1e-9, "perfect +1 not recovered"
    assert abs(sp.pearson([1, 2, 3, 4], [8, 6, 4, 2]) + 1.0) < 1e-9, \
        "perfect -1 not recovered — an uncentred formula reports +1 on all-positive inputs"
    assert abs(sp.pearson([1, 2, 3, 4], [5, 5, 5, 5])) < 1e-9, "constant y must give 0, not NaN"
    r = sp.pearson([0, 0, 1, 1], [1.0, 2.0, 3.0, 4.0])
    assert 0.8 < r < 0.9, r      # point-biserial on a 0/1 x
    assert sp.pearson([1], [2]) == 0.0, "a single point has no correlation"


# ------------------------------------------------------------------ frame identity

def test_frame_assertions_reject_a_merged_frame():
    """The 8-char prefix collides for 445 of 500 transcripts and a study keyed on it reported 550
    windows where the truth was 1,022, raising nothing. The frame must reject that shape."""
    good = [{"wid": "aaaaaaaa#t0000-A", "file": "/c/agent-a1.jsonl", "prefix": "agent-a1",
             "start": "A", "prior_turns": {"before": 0}},
            {"wid": "aaaaaaaa#t0001-A", "file": "/c/agent-a2.jsonl", "prefix": "agent-a1",
             "start": "A", "prior_turns": {"before": 3}}]
    files = ["/c/agent-a1.jsonl", "/c/agent-a2.jsonl"]
    per_file = {f: 1 for f in files}
    old = sp.EXPECTED_WINDOWS
    try:
        sp.EXPECTED_WINDOWS = 2
        sp.assert_frame(good, per_file, files)          # per-file identity: 2 windows
        merged = [dict(good[0]), dict(good[1])]
        merged[1]["wid"] = merged[0]["wid"]             # what prefix keying produces
        try:
            sp.assert_frame(merged, per_file, files)
        except AssertionError:
            pass
        else:
            raise AssertionError("a frame with colliding wids was accepted")
        dup = [dict(good[0]), dict(good[1])]
        dup[1]["file"] = dup[0]["file"]
        try:
            sp.assert_frame(dup, per_file, files)
        except AssertionError:
            pass
        else:
            raise AssertionError("a frame with a duplicate (file, start) was accepted")
    finally:
        sp.EXPECTED_WINDOWS = old


# ------------------------------------------------------------------ end to end

def _turn(dt, path, lang_file):
    return {"type": "assistant", "timestamp": dt.strftime("%Y-%m-%dT%H:%M:%SZ"),
            "cwd": "/w/proj", "gitBranch": "main", "requestId": f"req-{dt:%H%M%S}",
            "message": {"role": "assistant", "model": "acme-llm-7b",
                        "content": [{"type": "tool_use", "id": f"t{dt:%H%M%S}", "name": "Edit",
                                     "input": {"file_path": f"{path}/{lang_file}",
                                               "old_string": "a", "new_string": "b"}}]}}


def _synthetic(tmp):
    """A three-hour session: Go until minute 145, then Python from minute 150. With
    span=60/stride=50 that is four windows starting at 0/50/100/150, and the last one's language
    is absent from everything strictly before it."""
    d = os.path.join(tmp, "projects", "-w-proj")
    os.makedirs(d, exist_ok=True)
    p = os.path.join(d, "aaaaaaaa-1111-2222-3333-444444444444.jsonl")
    t0 = datetime(2026, 8, 10, 9, 0, 0)
    lines = []
    for i in range(30):                      # 0..145 min, every 5 min: Go
        lines.append(_turn(t0 + timedelta(minutes=5 * i), "/w/proj", f"pkg/svc{i}.go"))
    for i in range(30, 36):                  # 150..175 min: Python only
        lines.append(_turn(t0 + timedelta(minutes=5 * i), "/w/proj", f"tools/run{i}.py"))
    with open(p, "w") as fh:
        for o in lines:
            fh.write(json.dumps(o) + "\n")
    return p, lines


def test_end_to_end_the_before_prior_excludes_the_window_and_sofar_does_not():
    """The whole reason `before` is primary. On a session that turns from Go to Python at hour 3:

      * the session's FIRST window has an empty `before` prior — reported, never filled;
      * the final window's `before` prior holds only the earlier Go evidence, so Python reads as
        NOVEL with a large positive departure;
      * the same window's `sofar` prior contains its own evidence, so `novel` is False. That is
        the structural degeneracy of the spec's literal reading, pinned as a test rather than
        asserted in prose.
    """
    from app.analysis.workspace import scan_workspace
    with tempfile.TemporaryDirectory() as tmp:
        path, turns = _synthetic(tmp)
        ws = sp.windows_of(path, 0, turns, scan_workspace(path))
        assert len(ws) == 4, f"{len(ws)} windows, want 4 (span 60 / stride 50 over 175 min)"
        assert ws[0]["prior_turns"]["before"] == 0, ws[0]["prior_turns"]
        assert ws[0]["language"]["before"]["prior"]["reason"] == "absent", \
            "the session's first window must have an EMPTY prior, not an inherited one"
        assert ws[0]["language"]["before"]["contrast"]["prior_empty"] is True

        last = ws[-1]
        assert last["language"]["window"]["value"] == "Python", last["language"]["window"]
        b = last["language"]["before"]
        assert b["prior"]["value"] == "Go", f"the `before` prior is not Go: {b['prior']}"
        assert "Python" not in [t["value"] for t in b["prior"]["top"]], \
            "the `before` prior contains the window's own Python evidence — the cut leaked"
        assert b["contrast"]["novel"] is True, b["contrast"]
        assert b["contrast"]["agrees"] is False, b["contrast"]
        assert b["contrast"]["departure"] > 0.9, b["contrast"]

        s = last["language"]["sofar"]
        assert "Python" in [t["value"] for t in s["prior"]["top"]], \
            "the `sofar` prior must contain the window's own evidence (that is the point)"
        assert s["contrast"]["novel"] is False, \
            "`sofar` cannot produce novelty — bar 2 is structurally zero under it"
        assert s["contrast"]["departure"] < b["contrast"]["departure"], \
            "self-inclusion must shrink the departure"


# ------------------------------------------------------------------ mutation harness

def _mut_causal_inclusive(turns, cutoff):
    return [o for o in turns if sp._epoch(o["timestamp"]) <= cutoff]


def _mut_prior_falls_back(rl, dim):
    """The rejected alternative, implemented: hand out the top value even when unattributed."""
    p = _REAL["prior_at"](rl, dim)
    if p["value"] is None and p["top"]:
        p["value"] = p["top"][0]["value"]
    return p


def _mut_contrast_fills_an_unattributed_window(w, prior, counts):
    ww = w if w["value"] is not None else dict(w, value=prior["value"], share=prior["share"])
    return _REAL["contrast"](ww, prior, counts)


def _mut_departure_vs_prior_dominant(w, prior, counts):
    c = _REAL["contrast"](w, prior, counts)
    if c["departure"] is not None:
        c["departure"] = round(w["share"] - prior["share"], 4)
    return c


def _mut_agrees_false_when_prior_unattributed(w, prior, counts):
    c = _REAL["contrast"](w, prior, counts)
    if c["novel"] is not None and c["agrees"] is None:
        c["agrees"] = False
    return c


def _mut_novel_on_empty_prior(w, prior, counts):
    if w["value"] is not None and not sum(counts.values()):
        return {"agrees": None, "departure": None, "novel": True,
                "prior_share_of_window_value": 0.0, "prior_empty": True}
    return _REAL["contrast"](w, prior, counts)


def _mut_agreement_counts_unattributed_priors(recs, dim, variant):
    a = _REAL["agreement"](recs, dim, variant)
    a["n_both_attributed"] = a["window_attributed"]
    a["disagree"] = a["window_attributed"] - a["agree"]
    a["agreement"] = (round(a["agree"] / a["window_attributed"], 4)
                      if a["window_attributed"] else None)
    return a


def _mut_novelty_includes_empty_priors(recs, dim, variant):
    n = _REAL["novelty"](recs, dim, variant)
    n["n_with_prior_evidence"] = n["n_window_attributed"]
    n["novel"] += n["prior_empty"]
    n["novelty"] = (round(n["novel"] / n["n_window_attributed"], 4)
                    if n["n_window_attributed"] else None)
    return n


def _mut_assert_frame_no_wid_check(recs, per_file, files):
    assert len(recs) == sum(per_file.values())
    assert len(recs) == sp.EXPECTED_WINDOWS


def _mut_pearson_uncentred(xs, ys):
    import math
    sxy = sum(a * b for a, b in zip(xs, ys))
    sxx, syy = sum(a * a for a in xs), sum(b * b for b in ys)
    return sxy / math.sqrt(sxx * syy) if sxx and syy else 0.0


_REAL = {}

MUTATIONS = [
    ("causal_turns admits the boundary turn (<= not <)",
     "causal_turns", _mut_causal_inclusive,
     ["test_causal_turns_is_half_open_on_the_right",
      "test_end_to_end_the_before_prior_excludes_the_window_and_sofar_does_not"]),
    ("prior_at hands out the top value when unattributed (the rejected fallback)",
     "prior_at", _mut_prior_falls_back, ["test_prior_never_fills_an_unattributed_level"]),
    ("contrast fills an unattributed window from the prior",
     "contrast", _mut_contrast_fills_an_unattributed_window,
     ["test_contrast_is_all_none_when_the_window_has_no_value"]),
    ("departure taken against the prior's DOMINANT share",
     "contrast", _mut_departure_vs_prior_dominant,
     ["test_departure_is_measured_against_the_windows_own_value"]),
    ("agrees scored False rather than undefined against an unattributed prior",
     "contrast", _mut_agrees_false_when_prior_unattributed,
     ["test_agrees_is_undefined_rather_than_false_when_the_prior_is_unattributed"]),
    ("novel fires against an empty prior",
     "contrast", _mut_novel_on_empty_prior,
     ["test_novel_requires_absence_and_an_empty_prior_is_not_yield"]),
    ("agreement counts unattributed priors as disagreements",
     "agreement", _mut_agreement_counts_unattributed_priors,
     ["test_agreement_denominator_is_windows_where_both_sides_are_attributed"]),
    ("novelty counts empty priors as novel",
     "novelty", _mut_novelty_includes_empty_priors,
     ["test_novelty_denominator_excludes_windows_with_no_prior_evidence"]),
    ("assert_frame drops the wid-uniqueness check",
     "assert_frame", _mut_assert_frame_no_wid_check,
     ["test_frame_assertions_reject_a_merged_frame"]),
    ("pearson without mean-centring",
     "pearson", _mut_pearson_uncentred, ["test_pearson"]),
]

TESTS = [v for k, v in sorted(globals().items()) if k.startswith("test_")]


def _run_mutations():
    ok = True
    for label, attr, mutant, biters in MUTATIONS:
        real = getattr(sp, attr)
        _REAL[attr] = real
        setattr(sp, attr, mutant)
        try:
            for name in biters:
                fn = globals()[name]
                try:
                    fn()
                except AssertionError as e:
                    print(f"  BITES  {label}\n           -> {name}: {str(e)[:96]}")
                except Exception as e:                        # noqa: BLE001
                    ok = False
                    print(f"  WRONG  {label}\n           -> {name} raised "
                          f"{type(e).__name__} ({str(e)[:70]}), not AssertionError — the test "
                          f"died in the patch instead of exercising the path")
                else:
                    ok = False
                    print(f"  SURVIVES  {label}\n           -> {name} still passes; it is not "
                          f"testing this behaviour")
        finally:
            setattr(sp, attr, real)
            _REAL.pop(attr, None)
    return ok


if __name__ == "__main__":
    print(f"== {len(TESTS)} tests ==")
    for fn in TESTS:
        fn()
        print(f"  ok  {fn.__name__}")
    print(f"\n== {len(MUTATIONS)} mutations ==")
    good = _run_mutations()
    print("\nALL TESTS PASS; every mutation bites" if good else "\nMUTATION CHECK FAILED")
    sys.exit(0 if good else 1)
