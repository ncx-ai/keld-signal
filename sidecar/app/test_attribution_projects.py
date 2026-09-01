"""Run: cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_attribution_projects.py"""
from app.analysis import attribution

PROJECTS = [
    {"id": "proj_pay", "title": "Payments", "team": "Eng",
     "description": "Stripe migration.", "repos": ["acme-billing"],
     "keywords": ["stripe", "dunning"], "ticket_key": "PAY"},
    {"id": "proj_seo", "title": "SEO Push", "team": "Marketing",
     "description": "Grow organic signups.", "repos": [], "keywords": ["backlinks"]},
]

class FakeEncoder:
    calls = 0
    def encode(self, texts):
        FakeEncoder.calls += 1
        return [[1.0, 0.0] if "Payments" in t else [0.0, 1.0] for t in texts]

def test_set_and_hash_stability():
    h1 = attribution.set_projects(PROJECTS)
    h2 = attribution.set_projects(list(PROJECTS))
    assert h1 == h2, "same content must hash identically"
    got, h = attribution.current_projects()
    assert h == h1 and [p["id"] for p in got] == ["proj_pay", "proj_seo"]

def test_project_doc_shape():
    doc = attribution.project_doc(PROJECTS[0])
    assert "Payments" in doc and "stripe" in doc and "Stripe migration." in doc

def test_vectors_embedded_once_per_hash():
    attribution.set_projects(PROJECTS)
    enc = FakeEncoder()
    v1 = attribution.project_vectors(enc)
    v2 = attribution.project_vectors(enc)
    assert set(v1) == {"proj_pay", "proj_seo"} and v1 is v2 or v1 == v2
    assert FakeEncoder.calls == 1, f"re-embedded despite unchanged hash: {FakeEncoder.calls}"

if __name__ == "__main__":
    test_set_and_hash_stability()
    test_project_doc_shape()
    test_vectors_embedded_once_per_hash()
    print("test_attribution_projects: 3 passed")
