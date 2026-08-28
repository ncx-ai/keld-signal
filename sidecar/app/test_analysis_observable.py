"""Tests for `app.analysis.observable` — the two binary observable facets.

Written alongside the mapping and committed BEFORE the measurement that scores it. Each behaviour
in the module has a test that BITES: `mutations()` at the bottom injects a wrong version of every
rule and asserts the suite catches it, so "20 passed" is evidence rather than decoration.
"""
import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis.observable import (AUTHORING, REASONS, VERIFYING, VARIANTS, WRITING, authoring,
                                     authoring_collapse, authoring_narrow, authoring_sustained,
                                     counts, judge, verification)
from app.analysis.activity import ACTIVITY_FOR_ACTION, UNMAPPED_ACTIONS
from app.analysis.vocab import EXE_ACTION, TOOL_ACTION
from app.analysis.window import MIN_EVIDENCE, rollup


def _rows(*specs):
    """(level, ref, n) triples -> event rows in `levels.events_for_turns` shape."""
    return [(0.0, "s", "repo", "br", False, "ref", lv, ref, float(n)) for lv, ref, n in specs]


def _act(*pairs):
    return rollup(_rows(*[("action", a, n) for a, n in pairs]))


# --- the presence rule: one act is enough to say YES ----------------------------------------

def test_one_write_among_fifty_reads_is_still_authoring():
    """The whole point of a presence claim. A `Write` wrote a file and no amount of further
    reading unwrites it — this is where the facet differs from the refuted six-way, whose floor
    of 5 per class would have called this window `retrieve`."""
    a = authoring(_act(("read", 50), ("create", 1)))
    assert a.value is True, a
    assert (a.evidence, a.total, a.reason) == (1, 51, "attributed"), a


def test_one_test_run_among_fifty_reads_is_still_verification():
    v = verification(_act(("read", 50), ("test", 1)))
    assert v.value is True and v.evidence == 1, v


def test_every_positive_act_on_its_own_reaches_true():
    """No member of either set may be dead. A silently unreachable act is a mapping to a
    narrower set than the one documented."""
    for a in AUTHORING:
        assert authoring(_act((a, 1))).value is True, a
    for a in VERIFYING:
        assert verification(_act((a, 1))).value is True, a
    for a in WRITING:
        assert authoring_narrow(_act((a, 1))).value is True, a


# --- the absence claim needs the floor; the presence claim does not -------------------------

def test_no_positive_act_and_enough_evidence_is_a_confident_no():
    a = authoring(_act(("read", MIN_EVIDENCE)))
    assert a.value is False and a.reason == "attributed", a
    assert verification(_act(("read", 9), ("search", 4))).value is False


def test_no_positive_act_and_too_little_evidence_abstains_as_thin():
    """Four reads and nothing else does not establish that nothing was written — it establishes
    that the window was barely observed. `thin`, not False."""
    for n in range(1, MIN_EVIDENCE):
        a = authoring(_act(("read", n)))
        assert a.value is None and a.reason == "thin", (n, a)
        assert verification(_act(("read", n))).value is None


def test_the_floor_is_on_the_total_not_on_the_positive_class():
    """The asymmetry is the design. Unmapped acts still count toward the total, because they are
    observations of the window: `commit` five times shows the window was active and wrote
    nothing."""
    a = authoring(_act(("commit", MIN_EVIDENCE)))
    assert a.value is False and a.total == MIN_EVIDENCE, a


def test_no_action_evidence_at_all_is_absent_not_thin():
    """`absent` is not `thin`, for `window.REASONS`' reason: no number is not a small number.
    A window with only `tool` or `model` rows never had a physical act recognised."""
    for rl in ({}, rollup([]), rollup(_rows(("tool", "TodoWrite", 3))),
               rollup(_rows(("model", "m", 2)))):
        for name in ("authoring", "verification", "authoring_narrow", "authoring_sustained"):
            j = VARIANTS[name](rl)
            assert j.value is None and j.reason == "absent", (rl, name, j)
            assert (j.evidence, j.total) == (0, 0), (name, j)


def test_the_collapse_inherits_converse_and_that_is_a_DIFFERENCE_not_a_bug():
    """`activity` reads "no action, no tool, but some other evidence" as `converse` — an
    attributed value, not an abstention. Collapsed that is False (a conversation authored
    nothing), so the collapse ANSWERS a window the primary rule calls `absent`. Pinned because it
    is a real structural difference between the two and will show up in their coverages."""
    talk = rollup(_rows(("model", "m", 2)))
    assert authoring(talk).reason == "absent"
    j = authoring_collapse(talk)
    assert j.value is False and j.reason == "attributed", j
    # with a `tool` row present, `activity` refuses `converse` and both sides abstain
    tooled = rollup(_rows(("tool", "TodoWrite", 3)))
    assert authoring(tooled).reason == "absent" and authoring_collapse(tooled).value is None


# --- structural invariants ------------------------------------------------------------------

def test_the_verdict_is_none_exactly_when_it_is_not_attributed():
    """The invariant `window.attribution` and `activity.activity` both hold: an unattributed
    window stays visible rather than being handed a plausible value."""
    cases = [{}, _act(("read", 1)), _act(("read", 60)), _act(("commit", 9)),
             _act(("read", 30), ("edit", 2)), _act(("create", 8)), _act(("test", 1))]
    for rl in cases:
        for name, fn in VARIANTS.items():
            j = fn(rl)
            assert (j.value is not None) == (j.reason == "attributed"), (name, rl, j)
            assert j.value is None or j.value in (True, False), (name, j)


def test_every_reason_the_primary_rule_can_emit_is_declared():
    seen = set()
    for rl in ({}, _act(("read", 1)), _act(("read", 9)), _act(("create", 1))):
        seen.add(authoring(rl).reason)
        seen.add(verification(rl).reason)
    assert seen == set(REASONS), seen


def test_counts_totals_every_action_including_unmapped_ones():
    n_pos, n_act = counts(_act(("edit", 3), ("read", 7), ("commit", 2)), AUTHORING)
    assert (n_pos, n_act) == (3, 12), (n_pos, n_act)


def test_the_action_sets_are_real_actions_the_vocabulary_can_emit():
    """A positive set naming an act `vocab.action_for` never returns is a set that can never
    fire. This is the guard that would catch a typo like `run_code`."""
    emitted = ({v for v in TOOL_ACTION.values() if v} | set(EXE_ACTION)
               | {"commit", "sync with remote", "test", "build", "install"})
    for name, s in (("AUTHORING", AUTHORING), ("WRITING", WRITING), ("VERIFYING", VERIFYING)):
        assert set(s) <= emitted, (name, set(s) - emitted)


def test_authoring_and_verification_are_disjoint_and_neither_claims_bookkeeping():
    """They must be able to disagree, or one is a restatement of the other. And neither may read
    `version control` (it lumps `git status` in with `git diff`) or `commit`."""
    assert not set(AUTHORING) & set(VERIFYING)
    assert not ({"version control", "commit", "sync with remote"} & (set(AUTHORING)
                                                                    | set(VERIFYING)))
    both = _act(("edit", 3), ("test", 2))
    assert authoring(both).value is True and verification(both).value is True
    only_a = _act(("edit", 3), ("read", 4))
    assert authoring(only_a).value is True and verification(only_a).value is False


def test_writing_is_the_narrow_subset_and_transform_is_what_separates_them():
    assert set(WRITING) < set(AUTHORING)
    assert set(AUTHORING) - set(WRITING) == {"transform", "convert a document"}
    pipeline = _act(("search", 6), ("transform", 3))
    assert authoring(pipeline).value is True, "the primary counts a sed pipeline"
    assert authoring_narrow(pipeline).value is False, "the sensitivity variant does not"


def test_the_primary_authoring_set_is_the_refuted_collapse_verbatim():
    """The hypothesis under test is the collapse AS FORMED — `generate` + `transform` of the
    refuted six-way. If those classes' membership changes, this facet is testing something else
    and the 0.756 is no longer the number being replicated."""
    collapsed = {a for a, c in ACTIVITY_FOR_ACTION.items() if c in ("generate", "transform")}
    assert collapsed == set(AUTHORING), collapsed ^ set(AUTHORING)
    assert not set(AUTHORING) & UNMAPPED_ACTIONS


def test_the_verifying_set_is_the_routing_class_set_verbatim():
    """Membership is pinned, not merely iterated. Dropping `run code` would leave every other
    test green — `test_every_positive_act_on_its_own_reaches_true` iterates the SET, so a removal
    is invisible to it (measured: mutation M9 escaped until this test existed). The set is the
    routing-class study's, unchanged, because that is where the r = +0.114 independence and the
    0.622 base rate were measured; a different set has neither number behind it."""
    assert VERIFYING == ("test", "build", "run code"), VERIFYING
    assert set(VERIFYING) <= set(ACTIVITY_FOR_ACTION), set(VERIFYING) - set(ACTIVITY_FOR_ACTION)


# --- the variants are STRUCTURALLY different, not cosmetically ------------------------------

def test_sustained_needs_the_floor_on_the_positive_class_and_says_so():
    for n in range(1, MIN_EVIDENCE):
        j = authoring_sustained(_act(("read", 40), ("edit", n)))
        assert j.value is None and j.reason == "ambiguous", (n, j)
    assert authoring_sustained(_act(("read", 40), ("edit", MIN_EVIDENCE))).value is True
    assert authoring_sustained(_act(("read", 40))).value is False


def test_sustained_pools_the_positive_set_where_the_collapse_does_not():
    """4 creates + 4 edits is 8 authoring acts. The presence rule and the sustained rule both
    reach True; the collapse's per-CLASS floor reaches neither `generate` nor `transform` and
    falls through to what the reads say. That divergence is the reason both are reported."""
    rl = _act(("create", 4), ("edit", 4), ("read", 30))
    assert authoring(rl).value is True
    assert authoring_sustained(rl).value is True
    assert authoring_collapse(rl).value is False, authoring_collapse(rl)


def test_the_collapse_maps_the_six_classes_to_the_two_sides_it_claims():
    assert authoring_collapse(_act(("create", 9))).value is True          # generate
    assert authoring_collapse(_act(("edit", 9))).value is True            # transform
    assert authoring_collapse(_act(("test", 9))).value is False           # review
    assert authoring_collapse(_act(("run code", 9))).value is False       # analyze
    assert authoring_collapse(_act(("read", 9))).value is False           # retrieve


def test_the_collapse_carries_its_abstentions_through_rather_than_guessing():
    thin = authoring_collapse(_act(("read", 2), ("edit", 2)))
    assert thin.value is None and thin.reason == "thin", thin
    unmapped = authoring_collapse(_act(("commit", 9)))
    assert unmapped.value is None and unmapped.reason == "unmapped", unmapped


def test_min_evidence_is_the_derived_constant_not_a_new_number():
    from app.analysis.window import min_evidence_for
    assert MIN_EVIDENCE == min_evidence_for(0.5, 0.05) == 5


# --- mutation audit: every rule above must BITE ---------------------------------------------

def mutations():
    """Inject a wrong mapping; assert the suite fails. A test that passes against a broken
    mapping is not evidence, and this series has ~20 defects that surfaced as plausible numbers
    no aggregate caught."""
    import app.analysis.observable as ob
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]

    def run():
        for fn in fns:
            fn()

    def bites(name, patch, undo):
        patch()
        try:
            run()
        except (AssertionError, Exception):
            print(f"CAUGHT  {name}")
            return True
        finally:
            undo()
        print(f"MISSED  {name}")
        return False

    orig = dict(AUTHORING=ob.AUTHORING, WRITING=ob.WRITING, VERIFYING=ob.VERIFYING,
                judge=ob.judge, authoring=ob.authoring, verification=ob.verification,
                authoring_sustained=ob.authoring_sustained,
                authoring_collapse=ob.authoring_collapse, counts=ob.counts)

    def restore():
        for k, v in orig.items():
            setattr(ob, k, v)
        for k in ("authoring", "verification", "authoring_narrow", "authoring_sustained",
                  "authoring_collapse"):
            ob.VARIANTS[k] = getattr(ob, k)
        globals().update(authoring=ob.authoring, verification=ob.verification,
                         authoring_sustained=ob.authoring_sustained,
                         authoring_collapse=ob.authoring_collapse, counts=ob.counts,
                         AUTHORING=ob.AUTHORING, WRITING=ob.WRITING, VERIFYING=ob.VERIFYING)

    def swap(**kw):
        def do():
            for k, v in kw.items():
                setattr(ob, k, v)
                if k in ob.VARIANTS:
                    ob.VARIANTS[k] = v
                globals()[k] = v
        return do

    def floor_on_positive(rl, positive, min_evidence=MIN_EVIDENCE):
        """M1 — the presence rule turned into a share rule. Kills the whole design."""
        n_pos, n_act = ob.counts(rl, positive)
        if not (rl.get("action") or []):
            return ob.Judgement(None, 0, 0, "absent")
        if n_pos >= min_evidence:
            return ob.Judgement(True, n_pos, n_act, "attributed")
        return ob.Judgement(False, n_pos, n_act, "attributed")

    def no_floor_at_all(rl, positive, min_evidence=MIN_EVIDENCE):
        """M2 — a bare negative with no evidence floor: `thin` becomes False."""
        n_pos, n_act = ob.counts(rl, positive)
        if not (rl.get("action") or []):
            return ob.Judgement(None, 0, 0, "absent")
        return ob.Judgement(n_pos >= 1, n_pos, n_act, "attributed")

    def absent_as_thin(rl, positive, min_evidence=MIN_EVIDENCE):
        """M3 — collapsing the two abstention reasons into one."""
        n_pos, n_act = ob.counts(rl, positive)
        if n_pos >= 1:
            return ob.Judgement(True, n_pos, n_act, "attributed")
        if n_act >= min_evidence:
            return ob.Judgement(False, 0, n_act, "attributed")
        return ob.Judgement(None, 0, n_act, "thin")

    def positive_only_total(rl, positive):
        """M4 — the total stops counting unmapped acts, so `commit`x5 abstains."""
        items = rl.get("action") or []
        pos = frozenset(positive)
        n = int(sum(x for ref, x in items if ref in pos))
        return n, n

    def bind(fn, positive):
        return lambda rl, min_evidence=MIN_EVIDENCE: fn(rl, positive, min_evidence)

    caught = 0
    total = 0
    cases = [
        ("M1 floor on the positive class instead of the total",
         swap(judge=floor_on_positive, authoring=bind(floor_on_positive, ob.AUTHORING),
              verification=bind(floor_on_positive, ob.VERIFYING))),
        ("M2 no evidence floor on the negative (thin -> False)",
         swap(judge=no_floor_at_all, authoring=bind(no_floor_at_all, ob.AUTHORING),
              verification=bind(no_floor_at_all, ob.VERIFYING))),
        ("M3 absent collapsed into thin",
         swap(judge=absent_as_thin, authoring=bind(absent_as_thin, ob.AUTHORING),
              verification=bind(absent_as_thin, ob.VERIFYING))),
        ("M4 total excludes unmapped acts", swap(counts=positive_only_total)),
        ("M5 AUTHORING drops `transform` (no longer the collapse under test)",
         swap(AUTHORING=("create", "edit", "publish"))),
        ("M6 AUTHORING drops `publish`", swap(AUTHORING=("create", "edit", "transform",
                                                         "convert a document"))),
        ("M7 WRITING equals AUTHORING (the sensitivity variant stops varying)",
         swap(WRITING=ob.AUTHORING)),
        ("M8 VERIFYING gains `version control` (git status becomes verification)",
         swap(VERIFYING=("test", "build", "run code", "version control"))),
        ("M9 VERIFYING loses `run code`", swap(VERIFYING=("test", "build"))),
        ("M10 VERIFYING overlaps AUTHORING", swap(VERIFYING=("test", "build", "run code",
                                                             "edit"))),
        ("M11 an act name typo makes a member unreachable",
         swap(VERIFYING=("test", "build", "run_code"))),
        ("M12 sustained loses its ambiguous band",
         swap(authoring_sustained=bind(no_floor_at_all, ob.AUTHORING))),
        ("M13 the collapse becomes the presence rule",
         swap(authoring_collapse=bind(no_floor_at_all, ob.AUTHORING))),
        ("M14 the collapse invents a value where activity abstained",
         swap(authoring_collapse=lambda rl, min_evidence=MIN_EVIDENCE: ob.Judgement(
             True, 0, 0, "attributed"))),
    ]
    for name, patch in cases:
        total += 1
        caught += bites(name, patch, restore)
    print(f"\nmutation audit: {caught} of {total} caught")
    return caught == total


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed\n")
    if "--mutate" in sys.argv or True:
        ok = mutations()
        print("MUTATION AUDIT " + ("OK" if ok else "INCOMPLETE"))
        sys.exit(0 if ok else 1)
