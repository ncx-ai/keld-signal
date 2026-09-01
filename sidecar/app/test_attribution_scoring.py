"""Run: cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_attribution_scoring.py"""
from app.analysis import attribution

PROJECTS = [
    {"id": "proj_pay", "title": "Payments", "team": "Eng",
     "description": "Stripe billing migration.", "repos": ["acme-billing"],
     "keywords": ["stripe", "dunning"], "ticket_key": "PAY"},
    {"id": "proj_ui", "title": "Design System", "team": "Design",
     "description": "Component library and tokens.", "repos": ["acme-ui"],
     "keywords": ["storybook", "tokens"], "ticket_key": "DS"},
]

class PayEncoder:
    """proj_pay doc -> [1,0]; proj_ui doc -> [0,1]; text about stripe -> [0.9, 0.1]."""
    def encode(self, texts):
        out = []
        for t in texts:
            if "Payments" in t: out.append([1.0, 0.0])
            elif "Design System" in t: out.append([0.0, 1.0])
            elif "stripe" in t.lower(): out.append([0.9, 0.1])
            else: out.append([0.3, 0.3])
        return out

def test_metadata_boost_model_free():          # AC-4
    attribution.set_projects(PROJECTS)
    dims = {"repo": "acme-billing", "branch": "fix/PAY-12-retry"}
    b = attribution.metadata_boost(PROJECTS[0], dims, ["fix the dunning email"])
    assert b >= attribution.W_REPO + attribution.W_TICKET, f"boost {b}"
    scores, borderline, assigned, used = attribution.score_block(
        ["fix the dunning email"], dims, encoder=None)
    assert not used and "proj_pay" in assigned, (scores, assigned)

def test_embedding_plus_threshold():           # AC-3
    attribution.set_projects(PROJECTS)
    scores, borderline, assigned, used = attribution.score_block(
        ["we migrated stripe webhooks today"], {}, encoder=PayEncoder())
    assert used and scores["proj_pay"] > scores["proj_ui"]
    assert "proj_pay" in assigned and "proj_ui" not in assigned

def test_borderline_band():                    # AC-5 groundwork
    attribution.set_projects(PROJECTS)
    class MidEncoder(PayEncoder):
        def encode(self, texts):
            return [[1.0, 0.0] if "Payments" in t else
                    [0.0, 1.0] if "Design System" in t else
                    [0.47, 0.2] for t in texts]  # cosine vs proj_pay ≈ threshold
    scores, borderline, assigned, used = attribution.score_block(
        ["ambiguous work"], {}, encoder=MidEncoder())
    assert "proj_pay" in borderline, (scores, borderline)

if __name__ == "__main__":
    test_metadata_boost_model_free()
    test_embedding_plus_threshold()
    test_borderline_band()
    print("test_attribution_scoring: 3 passed")
