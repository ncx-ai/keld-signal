#!/usr/bin/env python3
"""Concept extraction — candidates, ranking, and the two shape bounds that make it publishable.

No real encoder: a controlled fake places each phrase at a known point so the ranking is a fact
about `concepts.py` rather than about Qwen3's opinions. The encoder is measured separately (the
smoke run), and a test that depended on real weights would be a test nobody runs.
"""
import math

from app.analysis import concepts


class Geom:
    """A 3-dim encoder with a hand-placed vocabulary.

    Anything naming the block cutter lands on e0, anything naming attribution on e1, everything
    else on e2. So a document of cutter talk must rank cutter phrases first, and MMR must reach
    for e1 rather than returning three spellings of e0.
    """

    def encode(self, texts):
        out = []
        for t in texts:
            low = t.lower()
            if "cutter" in low or "idle" in low:
                out.append([1.0, 0.0, 0.0])
            elif "attribution" in low or "project" in low:
                out.append([0.0, 1.0, 0.0])
            else:
                out.append([0.0, 0.0, 1.0])
        return out


def test_candidates_never_start_or_end_on_an_edge_word():
    # "of the block cutter" must not become a concept; the clean sub-phrase inside it must.
    got = concepts.candidates(["We changed the block cutter today, of the block cutter."])
    assert "block cutter" in got, got
    for phrase in got:
        first, last = phrase.split()[0].lower(), phrase.split()[-1].lower()
        assert first not in concepts.EDGE_WORDS and last not in concepts.EDGE_WORDS, phrase


def test_a_candidate_is_never_longer_than_MAX_WORDS():
    """⚠️ THE PUBLISH BOUND. A phrase is dropped WHOLE rather than cut (AGENTS.md's
    never-cut-mid-sentence rule as a drop), so this is what stops a sentence crossing the wire."""
    text = "the durable attribution job re-attributes the block once the weights are provisioned"
    for phrase in concepts.candidates([text]):
        assert len(phrase.split()) <= concepts.MAX_WORDS, phrase


def test_candidates_are_capped_so_the_encode_cost_is_bounded():
    # The cost knob: everything else here is microseconds, the encode is one batch of this many.
    long_text = " ".join(f"alpha{i} beta{i} gamma{i}" for i in range(400))
    assert len(concepts.candidates([long_text])) <= concepts.MAX_CANDIDATES


def test_a_phrase_is_reported_in_the_casing_the_block_uses_most():
    got = concepts.candidates(["Block Cutter here", "block cutter there", "Block Cutter again"])
    assert "Block Cutter" in got and "block cutter" not in got, got


def test_ranking_is_by_closeness_to_the_block_not_by_how_often_it_was_said():
    """The whole reason this exists beside terms.py: `n` cannot produce this ordering.

    "the network" is said four times and is peripheral; "block cutter" is said twice and is what
    the block is about. Raw counts rank the first higher; the centroid ranks the second.
    """
    texts = ["block cutter and the network", "the network the network the network cutter idle"]
    tvecs = Geom().encode(texts)
    got, _ms = concepts.extract(texts, tvecs, Geom())
    values = [c["value"].lower() for c in got]
    assert values, got
    assert "network" not in values[0], values


def test_mmr_reaches_for_a_second_idea_rather_than_a_third_spelling_of_the_first():
    texts = ["block cutter idle cutter", "project attribution here"]
    tvecs = Geom().encode(texts)
    got, _ms = concepts.extract(texts, tvecs, Geom(), top_k=3)
    joined = " ".join(c["value"].lower() for c in got)
    assert "cutter" in joined and "attribution" in joined, got


def test_a_pasted_PATH_can_never_become_a_concept():
    """⚠️ REGRESSION, FOUND ON REAL DATA. A slash was in the identifier set, so one pasted
    screenshot path became a single "word" and published verbatim —
    `Image source var/folders/6w/.../Screenshot`. The word BOUND could not catch it: a path is
    one word however long it is. Paths publish workspace-relative as `directories` and have no
    second door here."""
    text = ("Image source /var/folders/6w/xx74jjqd2hnd17yv6cwl79_w0000gn/T/TemporaryItems/"
            "NSIRD_screencaptureui_VTuoi0/Screenshot 2026-09-02 at 02.51.16.png")
    for phrase in concepts.candidates([text]):
        assert "/" not in phrase and "\\" not in phrase, phrase
        for token in phrase.split():
            assert len(token) <= concepts.MAX_TOKEN_CHARS, phrase


def test_a_contractions_orphan_letter_never_reaches_a_phrase():
    """⚠️ REGRESSION, FOUND ON REAL DATA. `don't` tokenises to `don` + `t`, so real blocks
    published `columns don t`, `metadata it s` and `blocks That s` — three slots on punctuation."""
    got = concepts.candidates(["the columns don't make sense and that's the whole problem"])
    for phrase in got:
        assert all(len(w) >= 2 for w in phrase.split()), phrase


def test_a_phrase_that_merely_restates_a_chosen_one_is_not_a_second_slot():
    """⚠️ REGRESSION, FOUND ON REAL DATA: six of eight slots went to `about metadata`,
    `talking about metadata`, `metadata increased`, `metadata it s`, `block metadata`,
    `metadata using usage`. MMR alone cannot fix it — those are genuinely different POINTS, just
    near ones. Containment is lexical and decides before any vector opinion."""

    class Flat:
        def encode(self, texts):
            return [[1.0, 0.0] for _ in texts]     # everything equally relevant: MMR is helpless

    phrases = ["metadata", "about metadata", "block metadata"]
    picked, _rel = concepts._mmr(phrases, Flat().encode(phrases), [1.0, 0.0], 8, 0.5)
    assert len(picked) == 1, [phrases[i] for i in picked]


def test_no_encoder_and_no_vectors_answer_EMPTY_rather_than_raising():
    # A degraded machine must publish an honest blank, never a 500 on the attribution path.
    assert concepts.extract(["some words here"], [], Geom()) == ([], 0)
    assert concepts.extract(["some words here"], [[1.0, 0.0, 0.0]], None) == ([], 0)


def test_a_block_with_no_prose_has_no_concepts_and_that_is_a_real_answer():
    got, _ms = concepts.extract([""], [[1.0, 0.0, 0.0]], Geom())
    assert got == []


def test_the_centroid_is_the_MEAN_not_one_message():
    """Scoring takes a per-message MAX (a block's messages may each concern a project); a concept
    describes the block AS A WHOLE, so ranking against one message would return its topic."""
    c = concepts._centroid([[1.0, 0.0, 0.0], [0.0, 1.0, 0.0]])
    assert math.isclose(c[0], c[1], rel_tol=1e-9) and c[0] > 0


if __name__ == "__main__":
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            fn()
            print(f"ok  {name}")
    print("all concept tests passed")
