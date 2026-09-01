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
    test_opt_out_env()
    test_no_weights_is_stated_not_fatal()
    print("test_attribution_verifier: 4 passed")
