"""Run: cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_attribution_verifier.py"""
import os
from app.analysis import attribution
from app import verifier

PROJECTS = [{"id": "proj_pay", "title": "Payments", "team": "Eng",
             "description": "Stripe billing.", "repos": [], "keywords": [], "ticket_key": "PAY"}]

class StubVerifier:
    def __init__(self, verdict): self.verdict, self.calls = verdict, 0
    def verify(self, block_text, dims, project):
        self.calls += 1
        return self.verdict, 0.01

def test_verdict_overrides_borderline():       # AC-5
    attribution.set_projects(PROJECTS)
    v = StubVerifier(True)
    overrides, pairs, ms = attribution.apply_verifier(
        ["ambiguous"], {}, {"proj_pay": 0.45}, ["proj_pay"], v)
    assert overrides == {"proj_pay": True} and pairs == 1 and v.calls == 1

def test_only_borderline_pairs_judged():       # AC-5
    v = StubVerifier(False)
    overrides, pairs, ms = attribution.apply_verifier(
        ["clear"], {}, {"proj_pay": 0.92}, [], v)   # nothing borderline
    assert pairs == 0 and v.calls == 0 and overrides == {}

def test_no_verdict_rejects_a_borderline_pair():  # AC-5 — the rejection direction
    """A NO verdict on a pair the score placed just ABOVE threshold must be
    recorded as an explicit False override, not dropped. An implementation
    doing `if verdict: overrides[pid] = True` (silently omitting the pid on a
    NO) would pass every other test here but fails this one."""
    attribution.set_projects(PROJECTS)
    v = StubVerifier(False)
    overrides, pairs, ms = attribution.apply_verifier(
        ["ambiguous"], {}, {"proj_pay": 0.52}, ["proj_pay"], v)
    assert overrides == {"proj_pay": False} and pairs == 1 and v.calls == 1

def test_mixed_borderline_verdicts_both_directions():  # AC-5
    """Two borderline projects in one block, one verdict each way — the shape
    a real block produces. Pins both the YES-override and NO-override
    directions in a single assertion."""
    attribution.set_projects([
        PROJECTS[0],
        {"id": "proj_growth", "title": "Growth", "team": "Marketing",
         "description": "Landing pages.", "repos": [], "keywords": [], "ticket_key": "GRW"},
    ])
    try:
        class MixedVerifier:
            def __init__(self):
                self.calls = []
            def verify(self, block_text, dims, project):
                self.calls.append(project["id"])
                return project["id"] == "proj_pay", 0.01

        v = MixedVerifier()
        overrides, pairs, ms = attribution.apply_verifier(
            ["ambiguous"], {}, {"proj_pay": 0.45, "proj_growth": 0.52},
            ["proj_pay", "proj_growth"], v)
        assert overrides == {"proj_pay": True, "proj_growth": False}
        assert pairs == 2 and sorted(v.calls) == ["proj_growth", "proj_pay"]
    finally:
        attribution.set_projects(PROJECTS)

def test_opt_out_env():                        # AC-6
    os.environ["KELD_ATTRIBUTION_VERIFIER"] = "0"
    try:
        assert verifier.enabled() is False
    finally:
        del os.environ["KELD_ATTRIBUTION_VERIFIER"]
    assert verifier.enabled() is True          # default ON within the gate

def test_no_weights_is_stated_not_fatal():
    old = os.environ.pop("KELD_VERIFIER_GGUF", None)
    os.environ["KELD_VERIFIER_GGUF"] = "/nonexistent/model.gguf"
    try:
        assert verifier.weights_path() is None
    finally:
        os.environ.pop("KELD_VERIFIER_GGUF")
        if old: os.environ["KELD_VERIFIER_GGUF"] = old

if __name__ == "__main__":
    test_verdict_overrides_borderline()
    test_only_borderline_pairs_judged()
    test_no_verdict_rejects_a_borderline_pair()
    test_mixed_borderline_verdicts_both_directions()
    test_opt_out_env()
    test_no_weights_is_stated_not_fatal()
    print("test_attribution_verifier: 6 passed")
