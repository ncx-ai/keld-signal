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
    scores, borderline, assigned, used, _tv, _c = attribution.score_block(
        ["fix the dunning email"], dims, encoder=None)
    assert not used and assigned == [] and borderline == [], (scores, assigned, borderline)
    assert scores["proj_pay"] == round(b, 4), "boost must still be visible in the scores"

def test_embedding_ranking_assigns_the_winner():   # AC-3
    attribution.set_projects(PROJECTS)
    scores, borderline, assigned, used, _tv, _c = attribution.score_block(
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
    scores, borderline, assigned, used, _tv, _c = attribution.score_block(
        ["ambiguous work"], {}, encoder=enc)
    assert assigned == ["proj_pay"], (scores, assigned)
    assert borderline == ["proj_ui"], (scores, borderline)


def test_the_null_competitor_blocks_a_topical_lookalike():   # the LEVEL gate
    # The block reads like general chat: null=0.90 dominates pay=0.30.
    # top <= null, so nothing is assigned and nothing is borderline —
    # "belongs to nothing" won the same ranking the projects competed in.
    attribution.set_projects(_geom_projects("null-gate"))
    enc = GeomEncoder([0.30, 0.10, 0.90, 0.2915])
    scores, borderline, assigned, used, _tv, _c = attribution.score_block(
        ["hey, how was your weekend?"], {}, encoder=enc)
    assert used and assigned == [] and borderline == [], (scores, assigned, borderline)


def test_two_close_winners_are_both_assigned():   # the SHAPE gate, multi-label
    # pay=0.70, ui=0.66: within MARGIN (0.08) of each other and both far
    # above null -> both assigned. A block can genuinely serve two projects.
    attribution.set_projects(_geom_projects("two-winners"))
    enc = GeomEncoder([0.70, 0.66, 0.0, 0.2728])
    scores, borderline, assigned, used, _tv, _c = attribution.score_block(
        ["work spanning both"], {}, encoder=enc)
    assert set(assigned) == {"proj_pay", "proj_ui"}, (scores, assigned)


# --- Pooling and centring (2026-09-03) -----------------------------------------

class TwoTextEncoder:
    """Two block texts with KNOWN, different geometry, so pooling is observable:
    text A -> e1 exactly (pay 1.0, ui 0.0, null 0.0); text B -> e3 (null 1.0).
    Docs as in GeomEncoder."""
    def encode(self, texts):
        out = []
        for t in texts:
            if t == attribution.NULL_DOC: out.append([0.0, 0.0, 1.0, 0.0])
            elif "Payments" in t: out.append([1.0, 0.0, 0.0, 0.0])
            elif "Design System" in t: out.append([0.0, 1.0, 0.0, 0.0])
            elif t == "A": out.append([1.0, 0.0, 0.0, 0.0])
            elif t == "B": out.append([0.0, 0.0, 1.0, 0.0])
            else: out.append([0.5, 0.5, 0.5, 0.5])
        return out


def test_pooling_is_mean_over_the_whole_block():
    """A = pure Payments, B = pure chat. MAX would score pay 1.0 and null 1.0 (a tie,
    nothing assigned); MEAN scores both 0.5 — still a tie, still nothing — so use three
    texts: A, A, B -> pay 0.667, null 0.333 under MEAN (assigned), 1.0 vs 1.0 under MAX
    (not). The block is judged as a whole, not by its single loudest message."""
    attribution.set_projects(_geom_projects("pooling"))
    scores, borderline, assigned, used, tv, c = attribution.score_block(
        ["A", "A", "B"], {}, encoder=TwoTextEncoder())
    assert used and len(tv) == 3
    assert abs(scores["proj_pay"] - round(2 / 3, 4)) < 1e-6, scores
    assert assigned == ["proj_pay"], (scores, assigned)
    assert c == {"applied": False, "background_n": 0}, c


def test_centring_waits_for_the_gate_then_subtracts_the_running_mean(tmp_dir=None):
    """Below MIN_BACKGROUND observations the decision is EXACTLY the uncentred one; at the
    gate the per-document running mean is subtracted; and a block never centres on itself
    (observe runs after decide)."""
    import os, tempfile
    attribution.set_projects(_geom_projects("centring"))
    saved = attribution.MIN_BACKGROUND
    attribution.MIN_BACKGROUND = 4
    try:
        path = os.path.join(tempfile.mkdtemp(), "state", "offsets.json")
        off = attribution.Offsets(path)
        enc = TwoTextEncoder()
        # Three chat-shaped blocks of one message each: the null's baseline climbs, gate not
        # met. `background_n` is the count AT DECISION TIME — observe runs after decide, so
        # the third call reports the two messages that preceded it, not itself.
        for i in range(3):
            _, _, _, _, _, c = attribution.score_block(["B"], {}, enc, offsets=off)
            assert c["applied"] is False and c["background_n"] == i, c
        # Fourth block: three observations on entry, gate is four -> still uncentred.
        _, _, _, _, _, c = attribution.score_block(["B"], {}, enc, offsets=off)
        assert c["applied"] is False and c["background_n"] == 3, c
        # Fifth: gate met on entry (4 observations of pure-null messages). The null's baseline
        # is now 1.0 and pay's is 0.0, so a pure-Payments block scores pay 1.0-0.0 = 1.0 vs
        # null 0.0-1.0 = -1.0 — centred, and decisively assigned.
        scores, _, assigned, _, _, c = attribution.score_block(["A"], {}, enc, offsets=off)
        assert c["applied"] is True and c["background_n"] == 4, c
        assert assigned == ["proj_pay"], (scores, assigned)
        # Persisted as scalars: a fresh Offsets on the same path has the same counts.
        again = attribution.Offsets(path)
        keys = [attribution.Offsets.key(attribution.project_doc(p))
                for p in attribution.current_projects()[0]] + [attribution.Offsets.key(attribution.NULL_DOC)]
        assert again.count(keys) == 5, again.count(keys)
        raw = open(path).read()
        assert "[" in raw and raw.count(",") < 40, "two floats per document, never a vector"
    finally:
        attribution.MIN_BACKGROUND = saved


def test_the_gate_is_all_or_nothing_and_a_reworded_document_resets_it():
    """A newly declared (or reworded) project has no baseline; centring switches OFF for every
    document until it does, rather than centring some and not others — the uncentred one
    would keep its raw register bias against corrected competitors."""
    off = attribution.Offsets(None)
    saved = attribution.MIN_BACKGROUND
    attribution.MIN_BACKGROUND = 2
    try:
        ka, kb = attribution.Offsets.key("doc A"), attribution.Offsets.key("doc B")
        off.observe({ka: [1.0, 0.0], kb: [0.0, 1.0]}, [[1.0, 0.0], [1.0, 0.0]])
        assert off.for_docs([ka, kb]) is not None            # both have 2
        kc = attribution.Offsets.key("doc B, reworded")
        assert kc != kb
        assert off.for_docs([ka, kc]) is None                 # the reworded doc has 0
        assert off.count([ka, kc]) == 0
    finally:
        attribution.MIN_BACKGROUND = saved


def test_each_stream_is_centred_against_its_own_baseline():
    """Assistant prose and a person's prompts are different registers, so they carry
    different baselines. Prime the ASSISTANT stream with pure-null messages (its null
    baseline -> 1.0) and the USER stream with pure-Payments messages (its pay baseline ->
    1.0). A block whose user turn is "A" (Payments) and whose assistant turn is "B" (chat)
    then centres each message against ITS stream: user A -> pay 1.0-1.0=0, null 0-0=0;
    asst B -> pay 0-0=0, null 1.0-1.0=0 -> everything cancels to a tie, nothing assigned.
    Under a single MIXED baseline the same block would not cancel. Measured reason to care:
    F1 0.717 vs 0.606 on real agent-only blocks."""
    attribution.set_projects(_geom_projects("per-stream"))
    saved = attribution.MIN_BACKGROUND
    attribution.MIN_BACKGROUND = 2
    try:
        off = attribution.Offsets(None)
        enc = TwoTextEncoder()
        for _ in range(3):   # user stream sees A, assistant stream sees B
            attribution.score_block(["A", "B"], {}, enc, offsets=off, n_user=1)
        scores, _, assigned, _, _, c = attribution.score_block(["A", "B"], {}, enc,
                                                               offsets=off, n_user=1)
        assert c["applied"] is True, c
        assert abs(scores["proj_pay"]) < 1e-6 and assigned == [], (scores, assigned)
        # And a block that is ALL assistant text about Payments beats its own stream's
        # baseline (asst pay baseline is 0.0): pay 1.0 vs null 0-1.0 = -1.0 -> assigned.
        scores, _, assigned, _, _, c = attribution.score_block(["A"], {}, enc,
                                                               offsets=off, n_user=0)
        assert c["applied"] is True and assigned == ["proj_pay"], (scores, assigned, c)
    finally:
        attribution.MIN_BACKGROUND = saved

if __name__ == "__main__":
    test_metadata_boost_model_free()
    test_embedding_ranking_assigns_the_winner()
    test_runner_up_near_the_cut_is_borderline()
    test_the_null_competitor_blocks_a_topical_lookalike()
    test_two_close_winners_are_both_assigned()
    test_pooling_is_mean_over_the_whole_block()
    test_centring_waits_for_the_gate_then_subtracts_the_running_mean()
    test_the_gate_is_all_or_nothing_and_a_reworded_document_resets_it()
    test_each_stream_is_centred_against_its_own_baseline()
    print("test_attribution_scoring: 9 passed")
