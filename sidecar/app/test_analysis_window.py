import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis import SCHEMA
from app.analysis.window import rollup, dominant
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


def test_an_absent_level_produces_no_key_rather_than_an_empty_one():
    assert payload(rollup(R))["workstreams"]["workflow"] is None


def test_payload_carries_its_schema_version():
    """These values land in financial reports; a silent shape change is the reproducibility
    failure the earlier handoff called out, so the payload is versioned from the first release."""
    assert payload(rollup(R))["schema"] == SCHEMA == 1


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
