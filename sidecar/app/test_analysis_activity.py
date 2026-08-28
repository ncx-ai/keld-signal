import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis.activity import (ACTIVITIES, ACTIVITY_FOR_ACTION, PRECEDENCE, REASONS,
                                   UNMAPPED_ACTIONS, activity)
from app.analysis.vocab import EXE_ACTION, TOOL_ACTION
from app.analysis.window import MIN_EVIDENCE, rollup


def _rows(*specs):
    """(level, ref, n) triples -> event rows in `levels.events_for_turns` shape."""
    return [(0.0, "s", "repo", "br", False, "ref", lv, ref, float(n)) for lv, ref, n in specs]


def _act(*pairs):
    return _rows(*[("action", a, n) for a, n in pairs])


# --- the Amendment 1 trap: precedence, never dominance --------------------------------------
#
# ~/keld/refseries-context/prose-activity/PREREGISTRATION.md Amendment 1 measured a rollup that
# took the DOMINANT action class: 827/1014 windows came out `researching`, a 95.4% majority
# baseline, because an hour of authoring issues ~50 Reads and ~5 Edits. These classes are a
# hierarchy of demand, not a partition by volume.

def test_authoring_wins_a_window_that_reads_ten_times_as_much():
    """The exact trap. 50 reads and 5 creates is an hour of authoring, not an hour of reading."""
    a = activity(rollup(_act(("read", 50), ("create", 5))))
    assert a.value == "generate", a


def test_editing_wins_a_window_that_reads_ten_times_as_much():
    a = activity(rollup(_act(("read", 50), ("edit", 6))))
    assert a.value == "transform", a


def test_the_dominant_class_does_not_win_by_being_dominant():
    """`retrieve` holds 88% of the evidence here and still loses. If this ever reports
    `retrieve`, dominance has crept back in and the 95.4% baseline comes with it."""
    a = activity(rollup(_act(("read", 40), ("search", 30), ("edit", 10))))
    assert a.value == "transform", a
    assert a.evidence == 10 and a.total == 80, a


# --- the precedence order is a total order over the five count-based classes -----------------

def test_precedence_is_a_total_order_and_the_earlier_class_always_wins():
    """Every adjacent pair, with the LOWER-precedence class holding strictly more evidence.
    A pairwise check, so a reordering of any single step in PRECEDENCE fails here."""
    for hi, lo in zip(PRECEDENCE, PRECEDENCE[1:]):
        hi_act = next(k for k, v in ACTIVITY_FOR_ACTION.items() if v == hi)
        lo_act = next(k for k, v in ACTIVITY_FOR_ACTION.items() if v == lo)
        a = activity(rollup(_act((hi_act, MIN_EVIDENCE), (lo_act, 500))))
        assert a.value == hi, (hi, lo, a)


def test_the_published_order_is_the_one_that_was_pre_registered():
    """Pinned literally, not derived: the order is the whole claim and a silent reshuffle of it
    is a different experiment. Derived from ACT SPECIFICITY (see the module docstring), not from
    the cost tier — `run code` is the most ambiguous act in the vocabulary and must not outrank
    the acts with exactly one reading."""
    assert PRECEDENCE == ("generate", "transform", "review", "analyze", "retrieve"), PRECEDENCE


# --- the evidence floor is the package's own derived constant, and it is uniform -------------

def test_a_class_below_the_floor_yields_to_a_lower_class_above_it():
    """Four creates are incidental; sixty reads are what the window did. The floor is
    window.MIN_EVIDENCE, reused rather than re-chosen."""
    a = activity(rollup(_act(("create", MIN_EVIDENCE - 1), ("read", 60))))
    assert a.value == "retrieve", a


def test_the_floor_is_inclusive_at_min_evidence():
    a = activity(rollup(_act(("create", MIN_EVIDENCE), ("read", 60))))
    assert a.value == "generate", a


def test_a_window_where_no_class_reaches_the_floor_is_thin_not_guessed():
    a = activity(rollup(_act(("read", 2), ("edit", 2))))
    assert a.value is None and a.reason == "thin", a
    assert a.total == 4, a


def test_the_floor_is_uniform_across_classes():
    """A per-class floor is exactly where tuning would enter: `create` is rarer than `read`, so
    a lower floor for `generate` would buy coverage by making the class easier to reach. The
    floor is one number for all five."""
    for cls in PRECEDENCE:
        act = next(k for k, v in ACTIVITY_FOR_ACTION.items() if v == cls)
        assert activity(rollup(_act((act, MIN_EVIDENCE - 1)))).reason == "thin", cls
        assert activity(rollup(_act((act, MIN_EVIDENCE)))).value == cls, cls


# --- converse is STRUCTURAL: no tools were used at all ---------------------------------------

def test_a_window_with_no_tool_call_at_all_is_converse():
    """The one class with no action evidence behind it. It is a fact about the window's SHAPE
    (nothing was reached for), not a count that won a comparison."""
    a = activity(rollup(_rows(("model", "claude-opus-5", 8), ("term", "Postgres", 3))))
    assert a.value == "converse" and a.reason == "attributed", a
    assert a.evidence == 0 and a.total == 0, a


def test_a_window_that_used_tools_with_no_recognised_action_is_absent_not_converse():
    """TodoWrite has no TOOL_ACTION entry, so the action level is empty while tools were plainly
    used. Reading that as `converse` would report a silent hour of talking."""
    a = activity(rollup(_rows(("tool", "TodoWrite", 9), ("model", "claude-opus-5", 4))))
    assert a.value is None and a.reason == "absent", a


def test_an_empty_window_is_absent_not_converse():
    """No evidence of any kind is not a finding about conversation."""
    a = activity(rollup([]))
    assert a.value is None and a.reason == "absent", a


# --- unmapped actions are inert, and coverage is reported separately from the verdict --------

def test_an_action_with_no_honest_reading_never_wins():
    """`commit`, `install`, `delegate`, `manage files` are bookkeeping, not one of the six.
    Five hundred of them still does not name the window's activity."""
    for act in sorted(UNMAPPED_ACTIONS):
        a = activity(rollup(_act((act, 500))))
        assert a.value is None and a.reason == "unmapped", (act, a)


def test_abundant_unreadable_evidence_is_not_reported_as_thin_evidence():
    """Five hundred commits and two edits are two DIFFERENT failures and this package names
    different facts differently (window.REASONS: `absent` is not `no_majority`). `thin` says
    almost nothing was counted; `unmapped` says plenty was counted and none of it speaks to
    activity. A reader who cannot tell them apart cannot tell whether a longer window would
    help — for `thin` it would, for `unmapped` no window length ever will."""
    assert activity(rollup(_act(("commit", 500)))).reason == "unmapped"
    assert activity(rollup(_act(("edit", 2)))).reason == "thin"
    assert activity(rollup(_act(("commit", 500), ("edit", 2)))).reason == "thin"


def test_an_unmapped_action_does_not_dilute_a_class_below_the_floor():
    a = activity(rollup(_act(("commit", 400), ("edit", MIN_EVIDENCE))))
    assert a.value == "transform", a


def test_total_counts_every_action_including_the_unmapped_ones():
    """Coverage must be measurable from the return value: `evidence`/`total` is the share of the
    window's physical acts the verdict actually rests on."""
    a = activity(rollup(_act(("edit", 10), ("commit", 30), ("read", 60))))
    assert (a.value, a.evidence, a.total) == ("transform", 10, 100), a


# --- the closed vocabularies must stay closed ------------------------------------------------

def test_every_action_the_vocabulary_can_emit_is_decided_one_way_or_the_other():
    """The guard against a new `action_for` value becoming silently invisible: a physical act
    added to vocab.py must be mapped or explicitly declared unmapped, never merely absent."""
    emitted = {a for a in TOOL_ACTION.values() if a} | set(EXE_ACTION)
    emitted |= {"commit", "sync with remote", "test", "build", "install"}   # action_for's verbs
    undecided = emitted - set(ACTIVITY_FOR_ACTION) - UNMAPPED_ACTIONS
    assert not undecided, undecided


def test_mapped_and_unmapped_are_disjoint():
    assert not (set(ACTIVITY_FOR_ACTION) & UNMAPPED_ACTIONS)


def test_every_production_label_is_reachable_and_no_other_value_is_emitted():
    """ACTIVITIES is the production `Activities` vocabulary in internal/agent/enrich/labels.go.
    A mapping that can never emit one of the six is a mapping to a different taxonomy."""
    assert set(ACTIVITY_FOR_ACTION.values()) | {"converse"} == set(ACTIVITIES)
    assert set(PRECEDENCE) | {"converse"} == set(ACTIVITIES)


def test_the_verdict_is_none_exactly_when_it_is_not_attributed():
    """Same invariant window.attribution holds, and for the same reason: an unattributed window
    must stay visible rather than being handed a plausible value."""
    cases = [[], _act(("read", 1)), _act(("read", 60)), _act(("commit", 9)),
             _rows(("model", "m", 2)), _rows(("tool", "TodoWrite", 3))]
    for rows in cases:
        a = activity(rollup(rows))
        assert (a.value is not None) == (a.reason == "attributed"), (rows, a)
        assert a.reason in REASONS, a
        assert a.value is None or a.value in ACTIVITIES, a


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
