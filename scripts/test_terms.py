#!/usr/bin/env python3
"""Tests for the named-term level. Each asserts a defect that was measured, not imagined."""
import sys, os
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from terms import tally, split_list, normalize


def test_list_of_names_is_split():
    """"Bedrock/Together.ai/Vertex" arrived as ONE span and buried two of three vendors: Vertex
    scored 0 until this split existed."""
    assert split_list("Bedrock/Together.ai/Vertex") == ["Bedrock", "Together.ai", "Vertex"]
    assert split_list("ACME and UnityPredict") == ["ACME", "UnityPredict"]


def test_an_identifier_is_never_split_on_its_own_punctuation():
    """A dot inside a vendor and a hyphen inside an artefact name are part of the name. Splitting
    on them would manufacture false identifiers, which AGENTS.md forbids outright."""
    assert split_list("Together.ai") == ["Together.ai"]
    assert split_list("keld-acme-routing-scenarios") == ["keld-acme-routing-scenarios"]


def test_case_is_folded_and_the_common_spelling_kept():
    """Magenta/magenta survived as two refs and halved the prominence of the hour's actual
    subject (measured: 24 mentions split 17/7)."""
    # Uses a dotted vendor because the shape patterns are case-insensitive, so this holds with or
    # without spaCy installed. The real case was Magenta/magenta, which needs the NER pass.
    out = {t["term"]: t for t in tally(["together.ai Together.ai Together.ai"])}
    assert list(out) == ["Together.ai"], out
    assert out["Together.ai"]["n"] == 3


def test_message_spread_is_counted_separately_from_frequency():
    """A name said eight times in one turn is one topic; in eight turns it is what the stretch is
    about. Frequency alone cannot tell those apart."""
    out = {t["term"]: t for t in tally(["ACME ACME ACME", "ACME"])}
    assert out["ACME"]["n"] == 4 and out["ACME"]["messages"] == 2


def test_no_term_is_truncated():
    """A cut identifier is a false identifier: normalize trims punctuation and articles only."""
    assert normalize("  the  keld-acme-routing-scenarios.pptx  ") == "keld-acme-routing-scenarios.pptx"
    assert normalize("(ACME)") == "ACME"


def test_bare_numbers_and_chat_noise_are_dropped():
    """Half of spaCy's raw output on this corpus is CARDINAL/ORDINAL. None of it attributes."""
    got = {t["term"] for t in tally(["ok yes 42 the ACME"])}
    assert "ACME" in got and "42" not in got and "ok" not in got


def test_shapes_survive_without_spacy():
    """The frames must build on a machine with no spaCy: the regex shapes alone still find the
    identifier-like names, so the level degrades rather than disappearing."""
    got = {t["term"] for t in tally(["ACME shipped UnityPredict via Together.ai"], nlp=None)}
    assert {"ACME", "UnityPredict", "Together.ai"} <= got, got


def test_malformed_tokens_are_rejected_but_common_words_are_not():
    """`\\n` and `427px` are not names at any frequency, so they are dropped by SHAPE. `API` is a
    real term that is merely ubiquitous — that is lift's job, not a stoplist's, so it survives
    here and is ranked down later."""
    got = {t["term"] for t in tally([r"427px \n toolu_01ABCDEF ACME API"], nlp=None)}
    assert "ACME" in got and "API" in got, got
    assert not {"427px", r"\n"} & got, got


def test_shouting_is_dropped_but_acronyms_and_product_names_survive():
    """ALL-CAPS emphasis ranked as "distinctive" under lift because it IS rare corpus-wide, and
    put DUDE and FUCKING in an admin-facing digest. Casing is the discriminator: a common English
    word in all caps is emphasis, while Bedrock/Vertex/Magenta arrive title-cased from NER and are
    never touched by this test."""
    got = {t["term"] for t in tally(["DUDE this is FUCKING RED but ACME and OTEL and TEE"], nlp=None)}
    assert {"ACME", "OTEL", "TEE"} <= got, got
    assert not {"DUDE", "FUCKING", "RED"} & got, got


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_")]
    bad = 0
    for n, f in fns:
        try:
            f(); print(f"PASS {n}")
        except AssertionError as e:
            bad += 1; print(f"FAIL {n}: {e}")
    print(f"\n{len(fns)-bad}/{len(fns)} passed")
    sys.exit(1 if bad else 0)
