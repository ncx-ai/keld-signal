#!/usr/bin/env python3
"""Tests for the activity-rerun harness (`scripts/activity_rerun.py`).

Standalone script with a `__main__` runner, never pytest — the repo convention (AGENTS.md).

Every load-bearing behaviour of the harness gets a test that BITES: the overlap exclusion that
makes the labels fresh, the wid parsing for both prior wid shapes, the rule adjudication at each
threshold, the rank/eta statistics, the label reader that the observable study broke, and the
blind. Run with the study interpreter (this needs `app.analysis`, which needs bashlex/wordfreq
only for levels — the pieces exercised here are pure Python):

    PYTHONPATH=sidecar python3 scripts/test_activity_rerun.py
"""
import math, os, sys, tempfile

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import activity_rerun as H


def _res(**kw):
    """A results dict with every rule PASSING, so each test can break exactly one thing."""
    base = {"lift": 0.25, "coverage": 0.90, "top_predicted_share": 0.30,
            "r_predrank_vs_log_volume": 0.10}
    base.update(kw)
    return base


def _tbl(**over):
    tbl = {c: {"support": 20, "predicted": 20, "tp": 15, "precision": 0.75, "recall": 0.75}
           for c in H.ACTIVITIES}
    for c, d in over.items():
        tbl[c] = {**tbl[c], **d}
    return tbl


# ---------------------------------------------------------------- the wid shapes and the overlap

def test_both_prior_wid_shapes_reduce_to_the_same_ambiguous_key():
    """`prefix-STAMP` (activity) and `prefix#tNNNN-STAMP` (observable) must both parse, and both
    must reduce to `prefix-STAMP` — the conservative key, because a prefix covers up to 37 files
    so an activity wid cannot identify one window."""
    with tempfile.TemporaryDirectory() as d:
        a = os.path.join(d, "a.txt")
        open(a, "w").write("# comment\nagent-a6-20260803T0059 generate\n"
                           "fff01ac3#t0298-20260720T2029 y n\n\n")
        keys, per = H.prior_keys([a])
        assert keys == {"agent-a6-20260803T0059", "fff01ac3-20260720T2029"}, keys
        assert per["a.txt"] == 2, per


def test_an_unparseable_wid_fails_loudly_rather_than_silently_admitting_a_window():
    with tempfile.TemporaryDirectory() as d:
        a = os.path.join(d, "a.txt")
        open(a, "w").write("not-a-wid-at-all generate\n")
        try:
            H.prior_keys([a])
        except AssertionError:
            return
        raise AssertionError("an unparseable wid was accepted — the exclusion would silently leak")


def test_sample_refuses_a_frame_whose_fresh_pool_overlaps_a_prior_sample():
    """The load-bearing assertion. Hand `sample` a frame in which EVERY eligible window was
    already labelled: it must not be able to produce a sample at all."""
    with tempfile.TemporaryDirectory() as d:
        prior = os.path.join(d, "facets")
        os.mkdir(prior)
        rows = []
        for i in range(200):
            rows.append({"wid": f"pre{i:05d}#t{i:04d}-20260101T{i % 24:02d}00",
                         "key": f"pre{i:05d}-20260101T{i % 24:02d}00",
                         "n_prompts": 1, "n_prose": 1, "file": f"/f{i}"})
        import json
        frame = os.path.join(d, "frame.ndjson")
        open(frame, "w").write("".join(json.dumps(r) + "\n" for r in rows))
        open(os.path.join(prior, H.PRIOR_LABELS[0]), "w").write(
            "".join(f"{r['key']} generate\n" for r in rows))
        open(os.path.join(prior, H.PRIOR_LABELS[1]), "w").write("")
        try:
            H.sample(frame, os.path.join(d, "out.ndjson"), prior)
        except AssertionError as e:
            assert "150" in str(e) or "OVERLAP" in str(e), e
            return
        raise AssertionError("sample drew from a fully-prior-labelled frame")


def test_sample_excludes_only_the_prior_windows_and_keeps_the_rest():
    import json
    with tempfile.TemporaryDirectory() as d:
        prior = os.path.join(d, "facets")
        os.mkdir(prior)
        rows = [{"wid": f"w{i:05d}#t{i:04d}-20260101T0100", "key": f"w{i:05d}-20260101T0100",
                 "n_prompts": 1, "n_prose": 1, "file": f"/f{i}"} for i in range(400)]
        frame = os.path.join(d, "frame.ndjson")
        open(frame, "w").write("".join(json.dumps(r) + "\n" for r in rows))
        banned = {r["key"] for r in rows[:200]}
        open(os.path.join(prior, H.PRIOR_LABELS[0]), "w").write(
            "".join(f"{k} generate\n" for k in sorted(banned)))
        open(os.path.join(prior, H.PRIOR_LABELS[1]), "w").write("")
        out = os.path.join(d, "out.ndjson")
        H.sample(frame, out, prior)
        got = [json.loads(l) for l in open(out)]
        assert len(got) == H.N_SAMPLE, len(got)
        assert not [r for r in got if r["key"] in banned], "a prior window reached the sample"


def test_ineligible_windows_are_excluded_not_silently_labelled():
    import json
    with tempfile.TemporaryDirectory() as d:
        prior = os.path.join(d, "facets")
        os.mkdir(prior)
        for f in H.PRIOR_LABELS:
            open(os.path.join(prior, f), "w").write("")
        rows = [{"wid": f"w{i:05d}#t{i:04d}-20260101T0100", "key": f"w{i:05d}-20260101T0100",
                 "n_prompts": 1 if i < 160 else 0, "n_prose": 1, "file": f"/f{i}"}
                for i in range(400)]
        frame = os.path.join(d, "frame.ndjson")
        open(frame, "w").write("".join(json.dumps(r) + "\n" for r in rows))
        out = os.path.join(d, "out.ndjson")
        H.sample(frame, out, prior)
        got = [json.loads(l) for l in open(out)]
        assert all(r["n_prompts"] >= 1 for r in got), "a prompt-less window reached the sample"


# ------------------------------------------------------------------------------- the label reader

def test_a_wid_containing_a_hash_survives_the_label_reader():
    """The observable study's bug: `line.split("#")[0]` truncates every wid at its own `#`."""
    with tempfile.TemporaryDirectory() as d:
        p = os.path.join(d, "l.txt")
        open(p, "w").write("# a comment\nfff01ac3#t0298-20260720T2029 generate\n")
        got = H.read_labels(p)
        assert got == {"fff01ac3#t0298-20260720T2029": "generate"}, got


def test_a_label_outside_the_production_vocabulary_fails_loudly():
    with tempfile.TemporaryDirectory() as d:
        p = os.path.join(d, "l.txt")
        open(p, "w").write("a#t0001-20260101T0100 authoring\n")
        try:
            H.read_labels(p)
        except AssertionError:
            return
        raise AssertionError("a non-vocabulary label was accepted")


def test_a_duplicate_wid_fails_loudly():
    with tempfile.TemporaryDirectory() as d:
        p = os.path.join(d, "l.txt")
        open(p, "w").write("a#t0001-20260101T0100 generate\na#t0001-20260101T0100 review\n")
        try:
            H.read_labels(p)
        except AssertionError:
            return
        raise AssertionError("a duplicate label was accepted — one window would be scored twice")


# ------------------------------------------------------------------------------ rule adjudication

def test_all_five_rules_pass_on_a_clean_result():
    r = H.adjudicate(_res(), _tbl(), H.ACTIVITIES)
    assert r["passes_all_rules"], r["rules"]


def test_rule_1_fails_just_below_the_registered_lift():
    assert not H.adjudicate(_res(lift=0.099), _tbl(), H.ACTIVITIES)["rules"][
        "1_beats_constant_by_10pts"]
    assert H.adjudicate(_res(lift=0.10), _tbl(), H.ACTIVITIES)["rules"][
        "1_beats_constant_by_10pts"], "the floor must be inclusive at the registered value"


def test_rule_2_fails_on_a_dominant_predicted_label():
    assert not H.adjudicate(_res(top_predicted_share=0.71), _tbl(), H.ACTIVITIES)["rules"][
        "2_not_degenerate"]


def test_rule_2_fails_on_the_transform_signature_even_when_nothing_dominates():
    """A label predicted more than ten times and right zero times — the exact shape that
    condemned attempt one's `transform` and, before it, `speech_act`'s `statement`."""
    r = H.adjudicate(_res(), _tbl(transform={"support": 0, "predicted": 36, "tp": 0,
                                             "precision": 0.0}), H.ACTIVITIES)
    assert not r["rules"]["2_not_degenerate"]
    assert r["rule_detail"]["2_labels_predicted_over_10x_with_zero_correct"] == ["transform"]


def test_rule_2_tolerates_ten_zero_correct_predictions_but_not_eleven():
    at = H.adjudicate(_res(), _tbl(transform={"predicted": 10, "tp": 0, "precision": 0.0}),
                      H.ACTIVITIES)
    over = H.adjudicate(_res(), _tbl(transform={"predicted": 11, "tp": 0, "precision": 0.0}),
                        H.ACTIVITIES)
    assert at["rules"]["2_not_degenerate"], "the registered threshold is >10, not >=10"
    assert not over["rules"]["2_not_degenerate"]


def test_rule_3_fails_just_below_the_registered_coverage():
    assert not H.adjudicate(_res(coverage=0.599), _tbl(), H.ACTIVITIES)["rules"][
        "3_coverage_at_least_60pct"]
    assert H.adjudicate(_res(coverage=0.60), _tbl(), H.ACTIVITIES)["rules"][
        "3_coverage_at_least_60pct"]


def test_rule_4_judges_only_classes_whose_true_support_reaches_the_floor():
    """A class with support 2 cannot be judged on precision — that is noise, not a measurement.
    A class at support 5 IS judged."""
    r = H.adjudicate(_res(), _tbl(converse={"support": 2, "predicted": 1, "tp": 0,
                                            "precision": 0.0}), H.ACTIVITIES)
    assert r["rules"]["4_per_class_precision_floor"], r["rule_detail"]
    assert r["rule_detail"]["4_classes_unjudged_support_under_5"] == ["converse"]
    r = H.adjudicate(_res(), _tbl(converse={"support": 5, "predicted": 1, "tp": 0,
                                            "precision": 0.0}), H.ACTIVITIES)
    assert not r["rules"]["4_per_class_precision_floor"]
    assert r["rule_detail"]["4_classes_below_precision_floor"] == ["converse"]


def test_rule_4_fails_a_judged_class_the_mapping_never_predicts():
    """Undefined precision is a FAIL: never predicting a real class is not a pass."""
    r = H.adjudicate(_res(), _tbl(review={"support": 30, "predicted": 0, "tp": 0,
                                          "precision": None}), H.ACTIVITIES)
    assert not r["rules"]["4_per_class_precision_floor"]
    assert "review" in r["rule_detail"]["4_classes_below_precision_floor"]


def test_rule_4_floor_is_inclusive_at_the_registered_precision():
    assert H.adjudicate(_res(), _tbl(review={"precision": 0.40}), H.ACTIVITIES)["rules"][
        "4_per_class_precision_floor"]
    assert not H.adjudicate(_res(), _tbl(review={"precision": 0.39}), H.ACTIVITIES)["rules"][
        "4_per_class_precision_floor"]


def test_rule_5_fails_on_a_size_bucket_in_either_direction():
    """`authoring` failed this at r = +0.737; a precedence mapping fails it NEGATIVE (more volume
    -> an earlier, more specific class). Both must trip it."""
    assert not H.adjudicate(_res(r_predrank_vs_log_volume=-0.6), _tbl(),
                            H.ACTIVITIES)["rules"]["5_not_a_size_bucket"]
    assert not H.adjudicate(_res(r_predrank_vs_log_volume=0.737), _tbl(),
                            H.ACTIVITIES)["rules"]["5_not_a_size_bucket"]
    assert H.adjudicate(_res(r_predrank_vs_log_volume=0.49), _tbl(),
                        H.ACTIVITIES)["rules"]["5_not_a_size_bucket"]


def test_rule_5_fails_when_the_correlation_could_not_be_computed():
    assert not H.adjudicate(_res(r_predrank_vs_log_volume=None), _tbl(),
                            H.ACTIVITIES)["rules"]["5_not_a_size_bucket"]


# ------------------------------------------------------------------------------- the statistics

def test_the_rank_covers_every_production_label_exactly_once():
    assert set(H.RANK) == set(H.ACTIVITIES), H.RANK
    assert sorted(H.RANK.values()) == list(range(len(H.ACTIVITIES)))
    assert H.RANK["generate"] == 0, "the rank must be the mapping's own precedence order"
    assert H.RANK["converse"] == len(H.ACTIVITIES) - 1, (
        "converse is the no-evidence end of the axis and must rank last")


def test_pearson_recovers_a_known_correlation_and_refuses_a_constant():
    assert abs(H.pearson([1, 2, 3, 4], [2, 4, 6, 8]) - 1.0) < 1e-9
    assert abs(H.pearson([1, 2, 3, 4], [8, 6, 4, 2]) + 1.0) < 1e-9
    assert H.pearson([1, 1, 1], [1, 2, 3]) is None


def test_eta_equals_absolute_r_on_a_binary_grouping():
    """The claim that makes eta the honest companion to rule 5: for two groups it IS |r|, so it
    generalises the observable study's rule rather than replacing it with something else."""
    g = [0, 0, 0, 1, 1, 1, 0, 1]
    v = [1.0, 2.0, 1.5, 5.0, 6.0, 4.5, 2.5, 5.5]
    assert abs(H.eta(g, v) - abs(H.pearson([float(x) for x in g], v))) < 1e-9


def test_eta_is_one_when_the_class_determines_volume_and_zero_when_it_does_not():
    assert abs(H.eta(["a", "a", "b", "b"], [1.0, 1.0, 9.0, 9.0]) - 1.0) < 1e-9
    assert abs(H.eta(["a", "b", "a", "b"], [1.0, 1.0, 9.0, 9.0])) < 1e-9


# ------------------------------------------------------------------------------- per-class table

def test_per_class_counts_support_predictions_and_hits_independently():
    answered = ["w1", "w2", "w3", "w4"]
    truth = {"w1": "generate", "w2": "generate", "w3": "review", "w4": "retrieve"}
    pred = {"w1": "generate", "w2": "transform", "w3": "transform", "w4": "retrieve"}
    tbl = H.per_class(answered, truth, pred, H.ACTIVITIES)
    assert tbl["generate"] == {"support": 2, "predicted": 1, "tp": 1,
                               "recall": 0.5, "precision": 1.0}, tbl["generate"]
    assert tbl["transform"]["support"] == 0 and tbl["transform"]["predicted"] == 2
    assert tbl["transform"]["precision"] == 0.0 and tbl["transform"]["recall"] is None
    assert tbl["review"]["recall"] == 0.0 and tbl["review"]["precision"] is None


# ------------------------------------------------------------------------------------- the blind

def test_the_blind_mechanism_drops_every_non_prose_block():
    H.assert_blind()


def test_assert_blind_rejects_ANY_extra_content_not_only_the_listed_words():
    """The banned-substring loop alone is not enough: a future `text_of` could leak content that
    is not on any list. `assert_blind` therefore pins EXACT equality with the prose, and this test
    proves that half bites — the mutation audit found the equality assertion removable without a
    single test failing, which is exactly the shape of a blind that erodes quietly."""
    real = H.text_of
    try:
        # Deliberately contains NO word from the banned list: the banned loop cannot catch it,
        # so only the exact-equality half can.
        H.text_of = lambda content: "PROSE. plus something the list never anticipated"
        try:
            H.assert_blind()
        except AssertionError:
            pass
        else:
            raise AssertionError("assert_blind accepted extra content beyond the prose")
        # And a leak whose words ARE listed must still be rejected, so neither half is redundant.
        H.text_of = lambda content: "PROSE. SECRET"
        try:
            H.assert_blind()
        except AssertionError:
            return
        raise AssertionError("assert_blind accepted a listed banned word")
    finally:
        H.text_of = real


def test_the_view_never_renders_a_level():
    """`dump` must emit no action, no count, no volume and no tool name. Feed it a record whose
    feature fields carry unmistakable markers and assert none reaches the file."""
    import json
    with tempfile.TemporaryDirectory() as d:
        samp = os.path.join(d, "s.ndjson")
        open(samp, "w").write(json.dumps({
            "wid": "aa#t0001-20260101T0100", "n_prompts": 1, "n_prose": 1,
            "actions": {"MARKER_ACTION": 4242}, "volume": 999777, "n_actions": 4242,
            "levels": ["action", "MARKER_LEVEL"], "tools": True, "nonempty": True,
            "prompts": ["do the thing"], "prose": ["did the thing"]}) + "\n")
        out = os.path.join(d, "v.txt")
        H.dump(samp, out)
        text = open(out).read()
        for banned in ("MARKER_ACTION", "4242", "999777", "MARKER_LEVEL"):
            assert banned not in text, f"the view leaks {banned!r}"
        assert "do the thing" in text and "did the thing" in text


def test_the_view_refuses_a_serialized_tool_block():
    import json
    with tempfile.TemporaryDirectory() as d:
        samp = os.path.join(d, "s.ndjson")
        open(samp, "w").write(json.dumps({
            "wid": "aa#t0001-20260101T0100", "n_prompts": 1, "n_prose": 1,
            "prompts": ['{"type": "tool_use", "name": "Read"}'], "prose": ["x"]}) + "\n")
        try:
            H.dump(samp, os.path.join(d, "v.txt"))
        except AssertionError:
            return
        raise AssertionError("a serialized tool block reached the view")


# ------------------------------------------------------------------- text bounding (repo rule)

def test_text_is_cut_at_a_sentence_boundary_and_the_drop_is_visible():
    t = "One sentence here. Two sentence here. Three sentence here."
    got = H.bound(t, 25)
    assert got.startswith("One sentence here."), got
    assert "omitted" in got, "a silent drop is the same defect one level up"
    assert not got.replace(" [... 39 chars omitted]", "").endswith("Two"), got


def test_a_single_oversized_sentence_is_rendered_WHOLE_and_never_cut_mid_clause():
    """The repo rule is "never cut mid-sentence", and it OUTRANKS the budget: a sentence longer
    than the whole budget is emitted in full rather than truncated. Cost is a view that runs
    over budget; the labeller sees more text, never less, so nothing is hidden.

    Consequence, pinned here because it is a real defect rather than a design: `bound`'s
    `if not keep` branch ("1 sentence of N chars omitted") is UNREACHABLE — the first sentence is
    always appended, since `keep` is empty on the first pass. That dead branch is inherited
    byte-identically from `activity_det.py` and `observable_facets.py`, so it has been dead in
    every study in this series. It is REPORTED, not silently patched: changing `bound` would
    change the rendered views and stop this study reusing attempt one's labelling method verbatim.
    """
    body = "a" * 300 + "."
    got = H.bound(body, 50)
    assert got == body, "the clause was cut, or a notice was invented"
    assert len(got) > 50, "the budget was respected at the cost of cutting mid-clause"
    # And the notice branch really is unreachable for ANY single-sentence input.
    for limit in (1, 10, 299):
        assert "no boundary inside the budget" not in H.bound(body, limit)


def test_a_long_code_fence_becomes_a_visible_placeholder():
    got = H.elide_code("before\n```go\n" + "\n".join(f"line{i}" for i in range(9)) + "\n```\nafter")
    assert "code omitted: 9 lines go" in got, got
    assert "line4" not in got
    assert "before" in got and "after" in got


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_") and callable(v)]
    failed = 0
    for fn in fns:
        try:
            fn()
            print(f"PASS {fn.__name__}")
        except AssertionError as e:
            failed += 1
            print(f"FAIL {fn.__name__}: {e}")
    print(f"\n{len(fns) - failed}/{len(fns)} passed")
    sys.exit(1 if failed else 0)
