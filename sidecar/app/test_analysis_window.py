import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis import SCHEMA
from app.analysis.window import MIN_EVIDENCE, rollup, dominant
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
    assert payload(rollup(R))["schema"] == SCHEMA == 2


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
