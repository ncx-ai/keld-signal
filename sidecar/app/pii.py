"""PII detection over presidio-analyzer, gated to the existing vocabulary.

`scan(text)` returns `[{"type","start","end","score"}, ...]` where `type` is one
of the entity names the sensitivity facet already understands (see
internal/agent/enrich/labels.go -> SensitivityFromEntity):

    ssn         -> phi
    credit_card -> pci
    email / phone -> pii

Credentials are NOT handled here: they stay on the Go-side gitleaks layer
(internal/agent/enrich/creddetect), which runs over the FULL prompt text and is
unaffected by the model's token window.

`person` and `address` ARE NOT DETECTED, and that is a deliberate narrowing of
coverage, not an omission -- see WHAT THE CORPUS MEASUREMENT REMOVED below.
The Go rollup still knows both names, so a future detector for them needs no
schema change, but nothing produces them today.

WHAT THE CORPUS MEASUREMENT REMOVED
-----------------------------------
Measured on 2,000 real developer prompts (scripts/pii_precision.py; results in
~/keld/refseries-context/pii-precision/RESULTS.md): 1,090 spans published, at
most 13 genuinely leaked -- precision ~1% -- and **24% of prompts published
`sensitivity: pii`**. A facet that fires on a quarter of all prompts and is
wrong 99 times in 100 is not a privacy signal, it is noise, and noise on a
privacy dashboard is worse than silence.

998 of those 1,090 spans were `person`/`address`, both sourced from
`SpacyRecognizer`'s NER labels. Zero confirmed personal names. Zero addresses.
What they actually were: `JSON` (132 spans), `Docker`, `Atlas`, `YAGNI` (27),
`AI` (27), markdown headings (`**Verdict:**`), exported Go identifiers
(`Getenv`, `Resync()`), hex colour literals, and a bare `❌` scoring **0.85** --
the same score a real name gets. So NO THRESHOLD SEPARATES THEM, and no gate
over the recognizer's output was going to either: the fix has to be the absence
of the recognizer. It is gone, along with the PERSON/LOCATION entries in
_ENTITY_MAP.

Free-form personal names in developer prose are the one thing a pattern
recognizer genuinely cannot do. That capability is therefore lost, not deferred
to a cheaper mechanism. Both types mapped to `pii`, the LOWEST severity class,
so nothing severe went with them -- `phi` and `pci` are unaffected -- but the
facet's coverage is now four pattern-matched types and the docs say so.

(A consequence worth recording: at b4415a7 the Go side gated NER `person`/
`address` on `hasLetter`, and b6137a0 deleted that rule with the NER path,
leaving "a name has letters in it" enforced nowhere -- which is why an emoji
could publish as `person`. Removing the recognizer dissolves that concern
rather than needing the rule back: there is no longer any source of a
letter-free `person`.)

WHICH RECOGNIZERS ARE ENABLED, AND WHY
--------------------------------------
presidio ships 86 predefined recognizers, but only 23 entities register for
`en`, and an enabled recognizer whose output is then discarded is pure cost --
every one runs a regex or a context lookup over every prompt. So the registry is
built explicitly with the four recognizers that produce the four mapped types,
and nothing else:

    CreditCardRecognizer  CREDIT_CARD    (Luhn-validated -> 1.0, else dropped)
    EmailRecognizer       EMAIL_ADDRESS
    PhoneRecognizer       PHONE_NUMBER   (libphonenumber)
    UsSsnRecognizer       US_SSN

Deliberately NOT enabled, in four groups:

1. The spaCy NER entirely (PERSON, LOCATION, ORGANIZATION, NRP, AGE, ID,
   DATE_TIME). PERSON/LOCATION for the measured reason above; the rest were
   never enabled -- ORGANIZATION alone would fire on every library and vendor
   name.

2. Fires constantly on ordinary developer text and is not leaked PERSONAL data:
   DateRecognizer (every timestamp), UrlRecognizer (every link),
   IpRecognizer (matches version strings like 1.2.3.4 as readily as addresses),
   MacAddressRecognizer.

3. Genuinely sensitive but with NO HOME in the current closed vocabulary:
   IBAN_CODE and US_BANK_NUMBER (bank accounts -- `pci` is card-specific, not a
   generic financial class), MEDICAL_LICENSE and UK_NHS (health identifiers --
   the `phi` row is keyed on `ssn` alone), US_ITIN / US_PASSPORT /
   US_DRIVER_LICENSE (government ids), CRYPTO (wallet address). Mapping any of
   these onto `ssn` to reach `phi` would overstate what was found, and a false
   `phi` is worse than a miss. Covering them properly needs new vocabulary
   entries and a SchemaVersion bump -- a deliberate contract change, not a
   silent side effect of adopting a library. Recorded as a known gap.

4. Country-specific national IDs for other locales: only `en` is analysed, so
   they never register.

NO NLP MODEL IS LOADED. Every remaining recognizer is a pattern (regex + Luhn /
libphonenumber), so presidio's NlpEngine is needed only to tokenize -- a blank
`spacy.blank("en")` pipeline, ~5 MB, no weights, no download. Measured: all four
types return byte-identical spans and scores against a blank pipeline as against
en_core_web_sm. That is not a coincidence -- presidio's only other use of the
pipeline is LemmaContextAwareEnhancer, and the shared pipeline this module used
to borrow was already loaded NER-only (no tagger/lemmatizer, see main.py
_analysis_nlp), so `token.lemma_` was already empty and the enhancer was already
inert: a dashed SSN beside the word "SSN" scored 0.5, the bare pattern score,
not the 0.85 a lemma match would give. Dropping the model therefore costs
nothing that was working. What it buys is that `/pii` no longer REQUIRES
en_core_web_sm (~50 MB permanently resident in the never-recycled FastAPI
parent, and subtracted from the inference worker's hard limit via
worker_manager.parent_reserve_mb) -- which matters with KELD_TERMS=0, where
nothing else loads it.

The SCORE THRESHOLD is load-bearing. UsSsnRecognizer carries four "very weak"
patterns at score 0.05 that match any bare nine-digit run; at 0.35 those are
dropped while a context-boosted SSN (0.5 dashed) and a phone (0.4) survive.
Measured, not guessed.

Everything that survives the threshold is then put through two gates: the
published-value gate (app.wellknown), because presidio reports
4111 1111 1111 1111 at 1.0 and user@example.com at 1.0 -- both correct as
structure, both documentation -- and the numeric-fragment gate below.
"""
from __future__ import annotations

import threading

from app.wellknown import is_well_known

# presidio entity -> the vocabulary name the Go side already consumes.
_ENTITY_MAP = {
    "US_SSN": "ssn",
    "CREDIT_CARD": "credit_card",
    "EMAIL_ADDRESS": "email",
    "PHONE_NUMBER": "phone",
}

_PRESIDIO_ENTITIES = list(_ENTITY_MAP)

# Below this, US_SSN's 0.05 "very weak" patterns turn every nine-digit order id
# into a phi report. See the module docstring.
SCORE_THRESHOLD = 0.35

_engine = None
_engine_lock = threading.Lock()

# --- numeric-fragment gate for the pattern-matched types --------------------
# Measured on 2,000 real prompts, and it produced the single worst finding in
# the study: the ONLY `credit_card` ever reported was the 13 digits AFTER the
# decimal point of a 17-digit float, Luhn-passing by coincidence, published as
# **`pci` at score 1.0** with no payment word within 60 characters. Alongside it,
# 11 of 11 `phone` hits were `daemon.go:1620-1647` line ranges or `0.5/0.3/0.7`
# ratio triples. One defect, twice: a number recognizer handed a FRAGMENT of a
# larger numeric construct.
#
# The rule is ASYMMETRIC, and that is the correction the corpus forced. The
# measurement proposed rejecting any match that is a proper substring of its
# whitespace-delimited token; checked against findings.jsonl that is both too
# weak and too strong. Too weak: `0.5/0.3/0.7` IS the whole token, so four of the
# eleven phone spans survive it. Too strong: `sliced_from_longer_token` is true
# for nearly every TRUE positive too (`[dg@keld.co].`, `4470,`) because ordinary
# sentence punctuation joins the token -- so the rule would cost real detections
# to buy nothing. Hence:
#
#   LEFT  -- any of `0-9 . , - : /` immediately before the match means the match
#            is a tail: `1234.5678901234567`, `daemon.go:1620-1647`, `pkg/0.5`,
#            `88-4539821746358200`. No real card or phone is ever written with
#            one of these directly in front of it. True-positive cost: none.
#   RIGHT -- the same list would be WRONG here: "call 415-682-4470, then email"
#            and "the card is 4539821746358200." are prose. So the right side
#            rejects only a CONTINUATION -- another digit, or a `. , -` that is
#            itself followed by a digit.
#
# The ratio triples are killed by the digit-count rule below, not by this one.
_FRAGMENT_LEFT = frozenset("0123456789.,-:/")
_FRAGMENT_RIGHT_SEP = frozenset(".,-")

# NANP is ten digits. Every measured `phone` false positive carried six to
# eight.
_MIN_PHONE_DIGITS = 10

# The types the fragment gate applies to. See scan().
_NUMERIC_TYPES = frozenset(("ssn", "credit_card", "phone"))


def _is_number_fragment(text: str, start: int, end: int) -> bool:
    """Is this span a slice of a longer numeric construct rather than a number?"""
    if start > 0 and text[start - 1] in _FRAGMENT_LEFT:
        return True
    if end < len(text):
        nxt = text[end]
        if nxt.isdigit():
            return True
        if nxt in _FRAGMENT_RIGHT_SEP and end + 1 < len(text) and text[end + 1].isdigit():
            return True
    return False


def _too_few_digits_for_phone(value: str) -> bool:
    """A phone span too short to be a dialable number.

    Ten digits is NANP; an international number carries a `+` country code, and
    that escape hatch is what keeps the short-country cases. This is what kills
    `0.5/0.3/0.7` (6 digits) and `118-140` (6) -- shapes that occupy a whole
    whitespace token, so the fragment gate above cannot see them.

    Cost: the 7-digit bare local number, which is not resolvable to a person
    anyway. Same precision-over-recall trade `_is_bare_digit_run` already makes.
    """
    if value.strip().startswith("+"):
        return False
    return sum(1 for c in value if c.isdigit()) < _MIN_PHONE_DIGITS


def _is_bare_digit_run(value: str) -> bool:
    """A phone span with no formatting at all.

    A unix epoch is ten digits -- exactly NANP length, so `_too_few_digits_for_phone`
    cannot see it -- and libphonenumber accepts it for some region, so
    `epoch 1755950709` comes back PHONE_NUMBER at the same 0.4 a real number gets.
    Developer transcripts are full of epochs, counters and numeric ids; a genuinely
    leaked phone number almost always carries a separator or a country code
    ("(415) 682-4470", "415-682-4470", "+1 415 682 4470"). Requiring one costs the
    unformatted "call 4156824470" case, which is the right side of
    precision-over-recall for a facet that publishes a person-level signal.
    """
    return value.isdigit()


def _nlp_engine():
    """A tokenizer-only NlpEngine over a BLANK spaCy pipeline -- no model, no weights.

    AnalyzerEngine always calls nlp_engine.process_text() to build NlpArtifacts, so an
    NlpEngine is not optional; a MODEL is. Every recognizer registered here is a pattern, and
    presidio's only other use of the pipeline is LemmaContextAwareEnhancer, which was already
    inert against the NER-only pipeline this module used to borrow (no lemmatizer -> empty
    `token.lemma_`). Verified end-to-end: all four types return identical spans and identical
    scores against `spacy.blank("en")` as against en_core_web_sm, SSN included at 0.5.

    `spacy.blank("en")` builds from the Language class -- tokenizer and stop-word list only,
    ~5 MB, and nothing to download or provision. presidio depends on spacy, so it is always
    importable wherever this module is.
    """
    import spacy
    from presidio_analyzer.nlp_engine import SpacyNlpEngine

    class _BlankSpacyNlpEngine(SpacyNlpEngine):
        # is_loaded() is `self.nlp is not None`, so AnalyzerEngine never calls load() and no
        # spacy.load() happens at all. `models` is metadata only once nlp is populated.
        def __init__(self):
            super().__init__(models=[{"lang_code": "en", "model_name": "blank"}])
            self.nlp = {"en": spacy.blank("en")}

    return _BlankSpacyNlpEngine()


def _build_engine():
    from presidio_analyzer import AnalyzerEngine, RecognizerRegistry
    from presidio_analyzer.predefined_recognizers import (
        CreditCardRecognizer, EmailRecognizer, PhoneRecognizer, UsSsnRecognizer,
    )

    registry = RecognizerRegistry()
    for rec in (
        CreditCardRecognizer(),
        EmailRecognizer(),
        PhoneRecognizer(),
        UsSsnRecognizer(),
    ):
        registry.add_recognizer(rec)

    return AnalyzerEngine(
        nlp_engine=_nlp_engine(),
        registry=registry,
        supported_languages=["en"],
    )


def _get_engine():
    """The analyzer, built once. There is no per-caller variation left to invalidate it: the
    registry is fixed and the pipeline is a blank tokenizer this module owns."""
    global _engine
    if _engine is None:
        with _engine_lock:
            if _engine is None:
                _engine = _build_engine()
    return _engine


def scan(text: str, nlp=None) -> list[dict]:
    """Find leaked personal data in `text`.

    Returns spans sorted by position, each `{"type","start","end","score"}` with
    `type` drawn only from the four detected names (`ssn`, `credit_card`,
    `email`, `phone`). Published test/example values are filtered out, as are
    numeric fragments of longer tokens. Never raises on empty or non-string
    input.

    NEVER returns the matched substring: the caller holds the text and the offsets index it, and
    a matched value in a return value is one log line away from being a leak.

    `nlp` is accepted and IGNORED. It used to hand this module the caller's already-loaded
    en_core_web_sm so presidio would not load a second one; no recognizer here needs a model any
    more (see the module docstring), and the parameter is kept only so an out-of-tree caller
    passing it does not break. It will go on the next contract touch.

    The first call builds the analyzer -- presidio imports plus a blank tokenizer, no weights.
    Still not something to do on an event loop."""
    if not text or not isinstance(text, str) or not text.strip():
        return []

    results = _get_engine().analyze(
        text=text,
        language="en",
        entities=_PRESIDIO_ENTITIES,
        score_threshold=SCORE_THRESHOLD,
    )

    out = []
    for r in results:
        kind = _ENTITY_MAP.get(r.entity_type)
        if kind is None:  # defensive: registry is restricted, but never leak a
            continue      # presidio-native name into the published vocabulary
        value = text[r.start:r.end]
        if is_well_known(value, kind):
            continue
        # Numeric types only. `email` is deliberately EXCLUDED: `mailto:dana@x.co`
        # is a real address and the left rule would reject it on the colon. The
        # email-shaped false positives have their own, structural gates in
        # app.wellknown.
        if kind in _NUMERIC_TYPES and _is_number_fragment(text, r.start, r.end):
            continue
        if kind == "phone" and (_is_bare_digit_run(value) or _too_few_digits_for_phone(value)):
            continue
        out.append({
            "type": kind,
            "start": int(r.start),
            "end": int(r.end),
            "score": float(r.score),
        })

    out.sort(key=lambda d: (d["start"], d["end"]))
    return out
