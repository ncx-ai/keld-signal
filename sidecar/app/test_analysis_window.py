import re
import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis import SCHEMA
from app.analysis.window import (MIN_EVIDENCE, attribution, dominant,
                                 min_evidence_for, rollup)
from app.analysis.workstreams import payload

R = [(0, "s", "r", "b", False, "ref", "artifact", "code", 9.0),
     (0, "s", "r", "b", False, "ref", "artifact", "prose", 1.0),
     (0, "s", "r", "b", False, "ref", "lang", "Go", 5.0),
     (0, "s", "r", "b", False, "ref", "lang", "Python", 5.0),
     (0, "s", "r", "b", False, "ref", "service", "127.0.0.1", 20.0),
     (0, "s", "r", "b", False, "ref", "service", "github.com", 2.0)]


def test_a_dominant_value_is_reported_with_its_share():
    assert dominant(rollup(R), "artifact")[:2] == ("code", 0.9)


def test_a_tie_is_unattributed_rather_than_an_arbitrary_pick():
    """Multi-label double-counts spend, and a silently chosen winner is the
    plausible-wrong-number failure this work hit roughly twenty times."""
    v, share, _ = dominant(rollup(R), "lang")
    assert v is None and share == 0.5


# --- minimum evidence -----------------------------------------------------------------------
#
# The share floor alone says nothing about how much was counted. One tool call gives share=1.0,
# and AnalyzeLabeled drops `evidence` on the way to the published enrichment, so a report cannot
# tell one observation from five hundred. Measured on the 572-window sample in
# ~/keld/refseries-context/workstreams.ndjson: 330 dimension slots publish share=1.0 off fewer
# than five observations, 129 of them off a single one.

def _n(level, ref, n):
    return (0, "s", "r", "b", False, "ref", level, ref, float(n))


def test_a_single_observation_is_not_a_dominant_value():
    """share=1.0 over one observation is the plausible-wrong-number failure this project keeps
    hitting: a fresh session's first prompt in /tmp would publish `project` at confidence 1.0."""
    v, share, total = dominant(rollup([_n("workspace", "scratch", 1)]), "workspace")
    assert v is None, v
    assert (share, total) == (1.0, 1), (share, total)


def test_an_underevidenced_window_still_reports_its_share_and_total():
    """Unattributed must stay VISIBLE — same reasoning the tie branch already follows. Only the
    value is withheld; the measurement is not."""
    rl = rollup([_n("lang", "Go", 3), _n("lang", "Python", 1)])
    v, share, total = dominant(rl, "lang")
    assert v is None
    assert (share, total) == (0.75, 4)


def test_the_floor_admits_a_window_at_exactly_min_evidence():
    """The bound is inclusive: MIN_EVIDENCE is the first count at which the 0.5 share floor is a
    statement about the population rather than about the sample."""
    rl = rollup([_n("workspace", "widget-app", MIN_EVIDENCE)])
    assert dominant(rl, "workspace")[0] == "widget-app"


def test_min_evidence_does_not_replace_the_share_floor():
    """Two independent conditions. Plenty of evidence, no majority -> still unattributed."""
    rl = rollup([_n("lang", "Go", 40), _n("lang", "Python", 35), _n("lang", "Rust", 30)])
    v, share, total = dominant(rl, "lang")
    assert v is None and total == 105 and share < 0.5


def test_min_evidence_is_overridable_per_call():
    rl = rollup([_n("workspace", "widget-app", 2)])
    assert dominant(rl, "workspace")[0] is None
    assert dominant(rl, "workspace", min_evidence=2)[0] == "widget-app"


def test_the_payload_reports_an_underevidenced_dimension_as_unattributed():
    """The property that actually reaches Atlas: workstreams.payload must not hand a
    one-observation dimension to publish as a real answer."""
    rl = rollup([_n("workspace", "scratch", 1), _n("branch", "main", 1)])
    ws = payload(rl)["workstreams"]
    assert ws["project"] is None and ws["branch"] is None, ws


def test_an_absent_level_produces_no_key_rather_than_an_empty_one():
    assert payload(rollup(R))["workstreams"]["workflow"] is None


def test_payload_carries_its_schema_version():
    """These values land in financial reports; a silent shape change is the reproducibility
    failure the earlier handoff called out, so the payload is versioned from the first release."""
    assert payload(rollup(R))["schema"] == SCHEMA == 7


# --- the floor generalised to an arbitrary slice length ---------------------------------------
#
# `/analyze` is gaining a short recent SLICE read against a longer baseline, so the floor has to
# hold at 5 minutes as well as 60. The derivation (see MIN_EVIDENCE) mentions no duration at all
# — it is a statement about the NUMBER OF OBSERVATIONS — so these pin that the generalisation is
# the same constant, that it is computed from the share floor rather than typed, and that the
# 60-minute behaviour is byte-for-byte what it was.

def test_the_floor_is_computed_from_the_share_floor_not_typed():
    """MIN_EVIDENCE is the OUTPUT of its own derivation, so the two cannot drift apart.

    This inspects the SOURCE, deliberately, and it is the only test here that does. Asserting
    `MIN_EVIDENCE == min_evidence_for(0.5, 0.05)` passes just as happily against a typed `= 5`,
    which is the vacuous version of this test — confirmed by mutation. What must hold is that the
    constant cannot be edited independently of the argument that produced it: change alpha or the
    formula and the constant follows, or the comment above it becomes a lie about the shipped
    number. Only the assignment expresses that, so only the assignment can be checked.
    """
    import inspect
    import app.analysis.window as w
    src = inspect.getsource(w)
    assert re.search(r"^MIN_EVIDENCE = min_evidence_for\(0\.5, 0\.05\)", src, re.M), \
        "MIN_EVIDENCE must be computed by min_evidence_for, not typed"
    assert MIN_EVIDENCE == min_evidence_for(0.5, 0.05) == 5


def test_the_floor_tracks_the_share_floor_and_the_significance_level():
    """The only two inputs. A stricter share floor needs MORE observations to be distinguishable
    from its own null, and a stricter alpha needs more too — neither is a duration."""
    assert min_evidence_for(0.5, 0.01) == 7          # 0.5**7 = 0.0078 <= 0.01, 0.5**6 = 0.0156
    assert min_evidence_for(0.75, 0.05) == 11        # 0.75**11 = 0.042 <= 0.05
    assert min_evidence_for(0.5, 0.5) == 1           # the floor itself is already significant
    assert min_evidence_for(0.5, 0.06) == 5          # 0.5**5 = 0.031; n=4 is 0.0625 > 0.06


def test_the_derived_floor_is_the_first_n_that_clears_alpha():
    """The property, not the arithmetic: n clears it and n-1 does not."""
    for floor in (0.5, 0.6, 0.75, 0.9):
        for alpha in (0.1, 0.05, 0.01):
            n = min_evidence_for(floor, alpha)
            assert floor ** n <= alpha, (floor, alpha, n)
            assert n == 1 or floor ** (n - 1) > alpha, (floor, alpha, n)


def test_the_floor_does_not_take_a_duration():
    """The one thing a caller must not be able to do is scale the floor by slice length. A
    duration-scaled floor makes the significance of a published attribution a function of the
    slice, while `share` and `value` look identical either way — and `evidence` is dropped before
    publish, so no reader could tell. That is the defect MIN_EVIDENCE exists to prevent,
    reintroduced through the back door."""
    import inspect
    params = set(inspect.signature(min_evidence_for).parameters)
    assert params == {"floor", "alpha"}, params


def test_sixty_minute_dominance_is_unchanged():
    """The regression pin for what production publishes TODAY. Every one of these was the answer
    before the slice work began and must still be, field for field."""
    assert dominant(rollup(R), "artifact") == ("code", 0.9, 10)
    assert dominant(rollup(R), "lang") == (None, 0.5, 10)
    assert dominant(rollup(R), "workspace") == (None, 0.0, 0)
    assert dominant(rollup([_n("workspace", "scratch", 1)]), "workspace") == (None, 1.0, 1)
    assert dominant(rollup([_n("workspace", "w", 5)]), "workspace") == ("w", 1.0, 5)
    assert dominant(rollup([_n("lang", "Go", 3), _n("lang", "Py", 1)]), "lang") == (None, 0.75, 4)


# --- WHY a slot is unattributed --------------------------------------------------------------
#
# Measured over 20,000 windows of the frozen corpus (see scripts/evidence_floor.py): at a
# 5-minute slice, `tooling` is unattributed 97.3% of the time and 77.8 points of that is NO
# EVIDENCE AT ALL at that level, not a floor rejection. A dynamics comparison that reads "absent"
# as "the dominant value changed" would report a context switch out of an empty level. So the
# reason is a first-class output, and the four reasons are four different facts.

def test_every_way_a_slot_fails_is_named():
    a = attribution(rollup(R), "artifact")
    assert (a.reason, a.value, a.share, a.evidence) == ("attributed", "code", 0.9, 10)
    assert attribution(rollup(R), "workspace").reason == "absent"
    assert attribution(rollup(R), "lang").reason == "tie"
    assert attribution(rollup([_n("workspace", "scratch", 1)]), "workspace").reason == "thin"
    assert attribution(rollup([_n("lang", "Go", 40), _n("lang", "Py", 35),
                               _n("lang", "Rs", 30)]), "lang").reason == "no_majority"


def test_an_absent_level_is_not_a_thin_one():
    """`bin` is sparse and a level can simply never have fired. Reporting that as `thin` would
    invite a caller to widen the slice to fix something no slice length can fix — measured:
    `workflow`'s `absent` share only falls 77.5% -> 55.3% from 5 to 60 minutes, while its `thin`
    share is under 3% at every length."""
    absent = attribution(rollup([]), "workspace")
    assert (absent.reason, absent.evidence) == ("absent", 0)
    thin = attribution(rollup([_n("workspace", "w", 4)]), "workspace")
    assert (thin.reason, thin.evidence) == ("thin", 4)


def test_too_little_evidence_outranks_a_tie():
    """One observation each is not a genuinely split window; it is two observations. Naming it
    `tie` would say the work was divided when the truth is that nothing was counted yet."""
    rl = rollup([_n("lang", "Go", 1), _n("lang", "Py", 1)])
    assert attribution(rl, "lang").reason == "thin"


def test_dominant_and_attribution_cannot_disagree():
    """`dominant` is a projection of `attribution`, so a value is returned exactly when the
    reason is `attributed` — there is no fourth path by which one could say yes and the other
    no."""
    cases = [[], [_n("workspace", "w", 1)], [_n("workspace", "w", 5)],
             [_n("lang", "Go", 3), _n("lang", "Py", 3)],
             [_n("lang", "Go", 40), _n("lang", "Py", 35), _n("lang", "Rs", 30)],
             [_n("artifact", "code", 9), _n("artifact", "prose", 1)]]
    for rows in cases:
        for level in ("workspace", "lang", "artifact"):
            rl = rollup(rows)
            v, share, tot = dominant(rl, level)
            a = attribution(rl, level)
            assert (v, share, tot) == (a.value, a.share, a.evidence), (rows, level)
            assert (v is not None) == (a.reason == "attributed"), (rows, level)


def test_loopback_is_not_an_external_system():
    """127.0.0.1 and localhost are 85% of the raw service level and would otherwise be the top
    'system this org depends on'."""
    ext = payload(rollup(R))["inventory"]["external_systems"]
    assert [e["value"] for e in ext] == ["github.com"], ext


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
