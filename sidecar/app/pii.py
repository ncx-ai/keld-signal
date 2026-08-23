"""PII detection over presidio-analyzer, gated to the existing vocabulary.

`scan(text)` returns `[{"type","start","end","score"}, ...]` where `type` is one
of the six entity names the sensitivity facet already understands (see
internal/agent/enrich/labels.go -> SensitivityFromEntity):

    ssn         -> phi
    credit_card -> pci
    email / phone / person / address -> pii

Credentials are NOT handled here: they stay on the Go-side gitleaks layer
(internal/agent/enrich/creddetect), which runs over the FULL prompt text and is
unaffected by the model's token window.

WHICH RECOGNIZERS ARE ENABLED, AND WHY
--------------------------------------
presidio ships 86 predefined recognizers, but only 23 entities register for
`en`, and an enabled recognizer whose output is then discarded is pure cost —
every one runs a regex or a context lookup over every prompt. So the registry is
built explicitly with the five recognizers that produce the six mapped types,
and nothing else:

    CreditCardRecognizer  CREDIT_CARD    (Luhn-validated -> 1.0, else dropped)
    EmailRecognizer       EMAIL_ADDRESS
    PhoneRecognizer       PHONE_NUMBER   (libphonenumber)
    UsSsnRecognizer       US_SSN
    SpacyRecognizer       PERSON, LOCATION

Deliberately NOT enabled, in three groups:

1. Fires constantly on ordinary developer text and is not leaked PERSONAL data:
   DateRecognizer (every timestamp), UrlRecognizer (every link),
   IpRecognizer (matches version strings like 1.2.3.4 as readily as addresses),
   MacAddressRecognizer. Also ORGANIZATION / NRP / AGE / ID from the spaCy NER —
   ORGANIZATION alone would fire on every library and vendor name.

2. Genuinely sensitive but with NO HOME in the current closed vocabulary:
   IBAN_CODE and US_BANK_NUMBER (bank accounts — `pci` is card-specific, not a
   generic financial class), MEDICAL_LICENSE and UK_NHS (health identifiers —
   the `phi` row is keyed on `ssn` alone), US_ITIN / US_PASSPORT /
   US_DRIVER_LICENSE (government ids), CRYPTO (wallet address). Mapping any of
   these onto `ssn` to reach `phi` would overstate what was found, and a false
   `phi` is worse than a miss. Covering them properly needs new vocabulary
   entries and a SchemaVersion bump — a deliberate contract change, not a
   silent side effect of adopting a library. Recorded as a known gap.

3. Country-specific national IDs for other locales: only `en` is analysed, so
   they never register.

The SCORE THRESHOLD is load-bearing. UsSsnRecognizer carries four "very weak"
patterns at score 0.05 that match any bare nine-digit run; at 0.35 those are
dropped while a context-boosted SSN (0.4 dashed, 0.85 with a nearby "SSN") and
a phone (0.4) survive. Measured, not guessed.

Everything that survives the threshold is then put through the published-value
gate (app.wellknown), because presidio reports 4111 1111 1111 1111 at 1.0 and
user@example.com at 1.0 — both correct as structure, both documentation.
"""
from __future__ import annotations

import re
import threading

from app.wellknown import is_well_known

# presidio entity -> the vocabulary name the Go side already consumes.
_ENTITY_MAP = {
    "US_SSN": "ssn",
    "CREDIT_CARD": "credit_card",
    "EMAIL_ADDRESS": "email",
    "PHONE_NUMBER": "phone",
    "PERSON": "person",
    "LOCATION": "address",
}

_PRESIDIO_ENTITIES = list(_ENTITY_MAP)

# Below this, US_SSN's 0.05 "very weak" patterns turn every nine-digit order id
# into a phi report. See the module docstring.
SCORE_THRESHOLD = 0.35

# spaCy model already provisioned for the sidecar; presidio defaults to
# en_core_web_lg, which is NOT installed here.
_SPACY_MODEL = "en_core_web_sm"

_engine = None
_engine_nlp = None      # the preloaded pipeline _engine was built over, if any
_engine_lock = threading.Lock()

# --- code-shape gate for the NER-derived types -----------------------------
# The spaCy NER is trained on prose and reads source-code tokens as names:
# measured on this repo's own vocabulary, `runStage` and `digitsOnly(d string`
# both come back PERSON at 0.85 — the same score a real name gets, so no
# threshold separates them. Only shape does. This gate applies ONLY to `person`
# and `address` (the two NER-derived types); the pattern recognizers do not need
# it, and applying it to them would drop real values.
# Quotes and hyphens are deliberately ABSENT: O'Brien and Jean-Luc are names,
# and a genuine code span carries other markers anyway.
_CODE_CHARS = set("()[]{}<>=/\\;:$#@|&*+`_")
# An identifier-joining dot: `enrich.WithJobContext`, `app.pii`. A human name
# with an initial ("Dana W. Smith") has a SPACE after the dot, so it survives.
_DOTTED_IDENT = re.compile(r"[A-Za-z0-9]\.[A-Za-z0-9]")
# camelCase inside one word: runStage, digitsOnly.
_CAMEL_CASE = re.compile(r"[a-z][A-Z]")


def _is_bare_digit_run(value: str) -> bool:
    """A phone span with no formatting at all.

    A unix epoch is ten digits — exactly NANP length — and libphonenumber
    accepts it for some region, so `epoch 1755950709` comes back PHONE_NUMBER
    at the same 0.4 a real number gets. Developer transcripts are full of
    epochs, counters and numeric ids; a genuinely leaked phone number almost
    always carries a separator or a country code ("(415) 682-4470",
    "415-682-4470", "+1 415 682 4470"). Requiring one costs the unformatted
    "call 4156824470" case, which is the right side of precision-over-recall
    for a facet that publishes a person-level signal.
    """
    return value.isdigit()


def _is_code_like(value: str) -> bool:
    if not value:
        return False
    if any(c in _CODE_CHARS for c in value):
        return True
    if _DOTTED_IDENT.search(value):
        return True
    if _CAMEL_CASE.search(value):
        return True
    # A person/place name is capitalized. A span whose first letter is lower
    # case is a bare identifier, not a name.
    for c in value:
        if c.isalpha():
            return c.islower()
    return False


def _nlp_engine(nlp):
    """presidio's NLP engine, over a CALLER-SUPPLIED spaCy pipeline when there is one.

    Left to itself presidio loads its own en_core_web_sm. In the sidecar that is a SECOND copy in
    the same long-lived FastAPI parent, which already holds one for the named-terms analysis level
    (app/main.py -> _analysis_nlp) and is never recycled — so the duplicate is permanent resident
    cost, and the parent's size is subtracted from the inference worker's hard limit
    (worker_manager.parent_reserve_mb). Measured here: the duplicate pipeline is ~50-65 MB.

    ONE CONSEQUENCE, MEASURED. The shared pipeline is loaded NER-only (no tagger/attribute_ruler/
    lemmatizer), so `token.lemma_` is empty and presidio's LemmaContextAwareEnhancer finds no
    context words: a dashed SSN next to the word "SSN" scores 0.5 (the bare pattern score) instead
    of 0.85. That is above SCORE_THRESHOLD either way, so nothing changes about what is DETECTED —
    and context enhancement only ever RAISES scores, so losing it can only make this stricter,
    which is the direction this module already errs in. It does mean the 0.05 "very weak" SSN
    patterns can never be boosted over the threshold at all.
    """
    from presidio_analyzer.nlp_engine import NlpEngineProvider, SpacyNlpEngine

    if nlp is None:
        return NlpEngineProvider(nlp_configuration={
            "nlp_engine_name": "spacy",
            "models": [{"lang_code": "en", "model_name": _SPACY_MODEL}],
        }).create_engine()

    class _PreloadedSpacyNlpEngine(SpacyNlpEngine):
        # is_loaded() is `self.nlp is not None`, so AnalyzerEngine will not call load() and no
        # second spacy.load() happens. `models` is metadata only once nlp is populated.
        def __init__(self):
            super().__init__(models=[{"lang_code": "en", "model_name": _SPACY_MODEL}])
            self.nlp = {"en": nlp}

    return _PreloadedSpacyNlpEngine()


def _build_engine(nlp=None):
    from presidio_analyzer import AnalyzerEngine, RecognizerRegistry
    from presidio_analyzer.predefined_recognizers import (
        CreditCardRecognizer, EmailRecognizer, PhoneRecognizer,
        SpacyRecognizer, UsSsnRecognizer,
    )

    nlp_engine = _nlp_engine(nlp)

    registry = RecognizerRegistry()
    for rec in (
        CreditCardRecognizer(),
        EmailRecognizer(),
        PhoneRecognizer(),
        UsSsnRecognizer(),
        # Restricted to the two NER labels that map; the same recognizer would
        # otherwise also emit ORGANIZATION/NRP/AGE/ID/DATE_TIME.
        SpacyRecognizer(supported_entities=["PERSON", "LOCATION"]),
    ):
        registry.add_recognizer(rec)

    return AnalyzerEngine(
        nlp_engine=nlp_engine,
        registry=registry,
        supported_languages=["en"],
    )


def _get_engine(nlp=None):
    """The analyzer, built once. Rebuilt if a DIFFERENT preloaded pipeline arrives — in the
    sidecar there is exactly one for the process's lifetime, so this is a guard against a caller
    silently getting an engine wired to a stale model, not a hot path."""
    global _engine, _engine_nlp
    stale = nlp is not None and nlp is not _engine_nlp
    if _engine is None or stale:
        with _engine_lock:
            if _engine is None or (nlp is not None and nlp is not _engine_nlp):
                _engine = _build_engine(nlp)
                _engine_nlp = nlp
    return _engine


def scan(text: str, nlp=None) -> list[dict]:
    """Find leaked personal data in `text`.

    Returns spans sorted by position, each `{"type","start","end","score"}` with
    `type` drawn only from the six-name vocabulary. Published test/example
    values are filtered out. Never raises on empty or non-string input.

    NEVER returns the matched substring: the caller holds the text and the offsets index it, and
    a matched value in a return value is one log line away from being a leak.

    `nlp` is an already-loaded spaCy pipeline to reuse instead of loading a second one; see
    _nlp_engine. None means load our own.

    The first call builds the analyzer, which loads a spaCy model — seconds, hundreds of MB.
    NEVER call this from an event loop."""
    if not text or not isinstance(text, str) or not text.strip():
        return []

    results = _get_engine(nlp).analyze(
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
        if kind in ("person", "address") and _is_code_like(value):
            continue
        if kind == "phone" and _is_bare_digit_run(value):
            continue
        out.append({
            "type": kind,
            "start": int(r.start),
            "end": int(r.end),
            "score": float(r.score),
        })

    out.sort(key=lambda d: (d["start"], d["end"]))
    return out
