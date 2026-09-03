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

def test_metadata_boost_model_free():          # AC-4 (AMENDED 2026-09-01)
    attribution.set_projects(PROJECTS)
    dims = {"repo": "acme-billing", "branch": "fix/PAY-12-retry"}
    b = attribution.metadata_boost(PROJECTS[0], dims, ["fix the dunning email"])
    assert b >= attribution.W_REPO + attribution.W_TICKET, f"boost {b}"
    # The boost is computed and reported, but with no encoder NOTHING is assigned:
    # one attribution path only, and exact matches alone never cross the threshold.
    scores, borderline, assigned, used, _tv = attribution.score_block(
        ["fix the dunning email"], dims, encoder=None)
    assert not used and assigned == [] and borderline == [], (scores, assigned, borderline)
    assert scores["proj_pay"] == round(b, 4), "boost must still be visible in the scores"

def test_embedding_ranking_assigns_the_winner():   # AC-3
    attribution.set_projects(PROJECTS)
    scores, borderline, assigned, used, _tv = attribution.score_block(
        ["we migrated stripe webhooks today"], {}, encoder=PayEncoder())
    assert used and scores["proj_pay"] > scores["proj_ui"]
    assert "proj_pay" in assigned and "proj_ui" not in assigned


class GeomEncoder:
    """Controlled geometry for the rank-and-margin rule, in 4 dimensions:
    proj_pay doc -> e1, proj_ui doc -> e2, NULL_DOC -> e3, and the block text
    a unit vector whose components ARE the similarities we want. All vectors
    unit-length, so score_block's re-normalisation is a no-op.

    ⚠️ The null document is recognised by IDENTITY against
    `attribution.NULL_DOC`, never by a phrase copied out of it. This fake used
    to match `"General conversation" in t` and the 2026-09-02 reword silently
    broke it: the null fell through to the `else` branch, took the BLOCK's own
    vector, scored 1.0 against itself, and nothing could ever be assigned. A
    fake holding its own copy of a constant is a second source of truth for it,
    and this one failed in the direction that looks like a logic bug."""
    def __init__(self, text_vec):
        self.text_vec = text_vec
    def encode(self, texts):
        out = []
        for t in texts:
            if t == attribution.NULL_DOC: out.append([0.0, 0.0, 1.0, 0.0])
            elif "Payments" in t: out.append([1.0, 0.0, 0.0, 0.0])
            elif "Design System" in t: out.append([0.0, 1.0, 0.0, 0.0])
            else: out.append(list(self.text_vec))
        return out


def _geom_projects(tag):
    """A fresh copy of PROJECTS whose content differs per test: the vector memo
    is keyed on the project-list hash, so two tests reusing identical projects
    would silently share the FIRST test's encoder geometry."""
    out = [dict(p) for p in PROJECTS]
    for p in out:
        p["description"] = f"{p['description']} ({tag})"
    return out


def test_runner_up_near_the_cut_is_borderline():   # AC-5 groundwork
    # pay=0.60, ui=0.49, null=0.0 -> cut = max(0, 0.60-MARGIN) = 0.52.
    # ui sits 0.03 below the cut: inside VERIFY_HALO (0.04), so borderline
    # and NOT assigned; pay is clear of the halo and assigned.
    attribution.set_projects(_geom_projects("runner-up"))
    enc = GeomEncoder([0.60, 0.49, 0.0, 0.6324])
    scores, borderline, assigned, used, _tv = attribution.score_block(
        ["ambiguous work"], {}, encoder=enc)
    assert assigned == ["proj_pay"], (scores, assigned)
    assert borderline == ["proj_ui"], (scores, borderline)


def test_the_null_competitor_blocks_a_topical_lookalike():   # the LEVEL gate
    # The block reads like general chat: null=0.90 dominates pay=0.30.
    # top <= null, so nothing is assigned and nothing is borderline —
    # "belongs to nothing" won the same ranking the projects competed in.
    attribution.set_projects(_geom_projects("null-gate"))
    enc = GeomEncoder([0.30, 0.10, 0.90, 0.2915])
    scores, borderline, assigned, used, _tv = attribution.score_block(
        ["hey, how was your weekend?"], {}, encoder=enc)
    assert used and assigned == [] and borderline == [], (scores, assigned, borderline)


def test_two_close_winners_are_both_assigned():   # the SHAPE gate, multi-label
    # pay=0.70, ui=0.66: within MARGIN (0.08) of each other and both far
    # above null -> both assigned. A block can genuinely serve two projects.
    attribution.set_projects(_geom_projects("two-winners"))
    enc = GeomEncoder([0.70, 0.66, 0.0, 0.2728])
    scores, borderline, assigned, used, _tv = attribution.score_block(
        ["work spanning both"], {}, encoder=enc)
    assert set(assigned) == {"proj_pay", "proj_ui"}, (scores, assigned)

if __name__ == "__main__":
    test_metadata_boost_model_free()
    test_embedding_ranking_assigns_the_winner()
    test_runner_up_near_the_cut_is_borderline()
    test_the_null_competitor_blocks_a_topical_lookalike()
    test_two_close_winners_are_both_assigned()
    print("test_attribution_scoring: 5 passed")
