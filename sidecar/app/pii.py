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
presidio ships 63 predefined recognizer classes here, and an enabled recognizer
whose output is discarded is pure cost -- every one runs a regex over every
prompt. So the registry is built EXPLICITLY, and in three tiers (see
UNIVERSAL_RECOGNIZERS / REGION_RECOGNIZERS below):

    universal  CreditCard, Email, Phone, Iban, Crypto
    "us"       UsSsn, AbaRouting, UsNpi, MedicalLicense      <- the default
    opt-in     uk es it pl fi kr in au ng th sg

Everything past the original four is CHECKSUM- OR ALGORITHM-VALIDATED: presidio
sets MAX_SCORE (1.0) only when the recognizer's own validate_result() returned
True, and MIN_SCORE (dropped) when it returned False. A recognizer whose
validation is merely structural is NOT here -- UsItin, UsPassport, UsLicense,
UsMbi, UsBank, UkNino, UkPassport, UkPostcode, InPan, InPassport, InVoter, SgFin,
It{DriverLicense,IdentityCard,Passport}, KrPassport, NgVehicleRegistration all
lack a check digit and would report on shape alone.

WHY REGION-SCOPED RATHER THAN ALL-ON. Almost every national-id recognizer is a
bare digit run plus ONE check digit, so its false-positive floor against
arbitrary numbers is 1-in-10 or 1-in-11 -- a checksum makes a false positive
unlikely, not impossible. Worse, the shapes COLLIDE: a valid US_NPI is ten digits
starting 1 or 2, which is exactly UK_NHS's shape, and UK_NHS rolls up to `phi`.
Enabling `uk` inside a US-only org therefore manufactures the most severe class
out of provider ids. Verified collisions are pinned in test_pii_regions.py
(AU_MEDICARE also validates as UK_NHS; SG_UEN also validates as ES_NIF; US_NPI
also validates as KR_BRN). Scoping to where an org actually operates is what
keeps those cross-firings out of the published signal.

`Iban` and `Crypto` are universal deliberately: they carry their own checksums
(mod-97, base58check/bech32) and are not bounded by where an org operates -- an
IBAN in a US prompt is still a leaked bank account.

MEASURED. Over 2,000 real developer prompts with EVERY region enabled, the whole
candidate set produced three spans, all three readings of one digit run in a URL
path, and all three killed by the fragment gate below -- see
~/keld/refseries-context/pii-precision/RESULTS.md. Cost is ~1 ms/prompt for the
full set.

Several recognizers, all opt-in, are BUSINESS registration numbers rather than
personal data: IT_VAT_CODE, KR_BRN, IN_GSTIN, AU_ABN, AU_ACN, SG_UEN. They are
included because every one of those registers also issues to sole traders, i.e.
to a natural person, but they are the weakest members of their tiers and an org
that only wants person-level signal should leave those regions off. ABA_ROUTING
is the same caveat one class up: a routing number identifies a BANK BRANCH from a
published directory, not an account -- it is kept because it is a reliable marker
that banking data is present in the prompt, not because it is itself leaked.

Still deliberately NOT enabled:

1. The spaCy NER entirely (PERSON, LOCATION, ORGANIZATION, NRP, AGE, ID,
   DATE_TIME). PERSON/LOCATION for the measured reason above; the rest were
   never enabled -- ORGANIZATION alone would fire on every library and vendor
   name.

2. Fires constantly on ordinary developer text and is not leaked PERSONAL data:
   DateRecognizer (every timestamp), UrlRecognizer (every link),
   IpRecognizer (matches version strings like 1.2.3.4 as readily as addresses),
   MacAddressRecognizer.

3. Vehicle registrations (UkVehicleRegistration, InVehicleRegistration) are
   validated, but their patterns are ordinary alphanumeric tokens and a plate is
   a step removed from a person.

4. Recognizers the region list ASKED FOR AND PRESIDIO DOES NOT HAVE, so those
   regions are absent entirely rather than silently empty: there is no German
   recognizer of any kind (no DeIdCard / DeSocialSecurity / DeTaxId / DeVatId /
   DeHealthInsurance / DePassport), no Swedish (SePersonnummer,
   SeOrganisationsnummer), no South African (ZaIdNumber), no Turkish
   (TrNationalId) and no UK driving licence. Verified by introspection against
   presidio-analyzer 2.2.362.

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

The SCORE THRESHOLD is load-bearing FOR THE UNVALIDATED TYPES ONLY.
UsSsnRecognizer carries four "very weak" patterns at score 0.05 that match any
bare nine-digit run; at 0.35 those are dropped while a context-boosted SSN (0.5
dashed) and a phone (0.4) survive. Measured, not guessed. It buys the validated
recognizers NOTHING -- a passing checksum is promoted straight to 1.0 whatever
the pattern scored, which is why AbaRouting's 0.05 pattern is not protected by
it and the gates below have to be.

Everything that survives the threshold is then put through the gates: the
published-value gate (app.wellknown), because presidio reports
4111 1111 1111 1111 at 1.0 and user@example.com at 1.0 -- both correct as
structure, both documentation -- the numeric-fragment gate, and (for the
region-scoped types) the token-delimitation gate below.
"""
from __future__ import annotations

import os
import threading

from app.wellknown import is_well_known

# --- the recognizer tiers ----------------------------------------------------
#
# Each entry is (presidio recognizer class name, language override or None).
# The override exists because presidio ships several of these bound to their own
# locale (`es`, `it`, `ko`, `pl`, `fi`, `th`) and this service only ever analyses
# `en`, so without it the recognizer registers for a language nobody asks for and
# the tier is silently dead. The regex and the check algorithm are language
# independent; only the (unused, lemma-gated) context words are not.

# Not bounded by where an org operates. A card, a mailbox, a phone number, an
# IBAN and a crypto wallet are the same leak in any jurisdiction.
UNIVERSAL_RECOGNIZERS = (
    ("CreditCardRecognizer", None),
    ("EmailRecognizer", None),
    ("PhoneRecognizer", None),
    ("IbanRecognizer", None),
    ("CryptoRecognizer", None),
)

# region code -> recognizers that region turns on. "us" is the default; see
# DEFAULT_REGIONS and the module docstring for why this is scoped rather than
# all-on.
REGION_RECOGNIZERS = {
    "us": (("UsSsnRecognizer", None), ("AbaRoutingRecognizer", None),
           ("UsNpiRecognizer", None), ("MedicalLicenseRecognizer", None)),
    "uk": (("NhsRecognizer", None),),
    "es": (("EsNifRecognizer", "en"), ("EsNieRecognizer", "en")),
    "it": (("ItFiscalCodeRecognizer", "en"), ("ItVatCodeRecognizer", "en")),
    "pl": (("PlPeselRecognizer", "en"),),
    "fi": (("FiPersonalIdentityCodeRecognizer", "en"),),
    "kr": (("KrRrnRecognizer", "en"), ("KrDriverLicenseRecognizer", "en"),
           ("KrBrnRecognizer", "en"), ("KrFrnRecognizer", "en")),
    "in": (("InAadhaarRecognizer", None), ("InGstinRecognizer", None)),
    "au": (("AuTfnRecognizer", None), ("AuAbnRecognizer", None),
           ("AuAcnRecognizer", None), ("AuMedicareRecognizer", None)),
    "ng": (("NgNinRecognizer", None),),
    "th": (("ThTninRecognizer", "en"),),
    "sg": (("SgUenRecognizer", None),),
}

# The regions in force when the caller names none. `us` because that is where the
# product ships first; an org elsewhere sets KELD_PII_REGIONS (or, once Atlas
# serves it, the `pii_regions` org setting -- see internal/agent/settings).
DEFAULT_REGIONS = ("us",)

REGIONS_ENV = "KELD_PII_REGIONS"

# presidio entity -> the vocabulary name the Go side consumes (see
# internal/agent/enrich/labels.go -> SensitivityFromEntity). The naming rule is
# "presidio's entity name, lowercased", with two exceptions where that name is
# not English anyone writes: IBAN_CODE -> iban, and CRYPTO -> crypto_wallet
# (bare `crypto` names a field of study, not the datum). The four original
# entries predate the rule and keep their shorter legacy names, which are a
# published contract.
_ENTITY_MAP = {
    "US_SSN": "ssn",
    "CREDIT_CARD": "credit_card",
    "EMAIL_ADDRESS": "email",
    "PHONE_NUMBER": "phone",
    # universal
    "IBAN_CODE": "iban",
    "CRYPTO": "crypto_wallet",
    # us
    "ABA_ROUTING_NUMBER": "aba_routing",
    "US_NPI": "us_npi",
    "MEDICAL_LICENSE": "medical_license",
    # uk
    "UK_NHS": "uk_nhs",
    # es
    "ES_NIF": "es_nif",
    "ES_NIE": "es_nie",
    # it
    "IT_FISCAL_CODE": "it_fiscal_code",
    "IT_VAT_CODE": "it_vat_code",
    # pl / fi
    "PL_PESEL": "pl_pesel",
    "FI_PERSONAL_IDENTITY_CODE": "fi_personal_identity_code",
    # kr
    "KR_RRN": "kr_rrn",
    "KR_DRIVER_LICENSE": "kr_driver_license",
    "KR_BRN": "kr_brn",
    "KR_FRN": "kr_frn",
    # in
    "IN_AADHAAR": "in_aadhaar",
    "IN_GSTIN": "in_gstin",
    # au
    "AU_TFN": "au_tfn",
    "AU_ABN": "au_abn",
    "AU_ACN": "au_acn",
    "AU_MEDICARE": "au_medicare",
    # ng / th / sg
    "NG_NIN": "ng_nin",
    "TH_TNIN": "th_tnin",
    "SG_UEN": "sg_uen",
}

# The labels the original four recognizers produce. Everything else is
# region-scoped and gets the extra token-delimitation gate (see scan()); the
# original four are excluded from it only so their MEASURED behaviour is not
# perturbed by this widening.
_LEGACY_TYPES = frozenset(("ssn", "credit_card", "email", "phone"))

# Below this, US_SSN's 0.05 "very weak" patterns turn every nine-digit order id
# into a phi report. See the module docstring.
SCORE_THRESHOLD = 0.35

_engines: dict = {}
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

# The types the fragment gate applies to: everything numeric. `email` is the one
# exclusion and it is deliberate -- `mailto:dana@x.co` is a real address and the
# LEFT rule would reject it on the colon. See scan().
#
# The region-scoped national ids are all in scope. That is not a precaution: the
# ONLY spans the full candidate set produced over 2,000 real prompts were three
# readings (AU_ABN, IT_VAT_CODE, PL_PESEL) of one digit run sitting after a `/`
# in a URL path, and the LEFT rule is exactly what removes them.
_NON_NUMERIC_TYPES = frozenset(("email",))


def _is_glued_to_a_longer_token(text: str, start: int, end: int) -> bool:
    """Does this span sit INSIDE a longer alphanumeric token?

    Most presidio patterns are `\\b`-anchored, for which this is always false and
    the check costs nothing. Several of the region-scoped ones are NOT:
    MedicalLicenseRecognizer's DEA pattern, ItFiscalCodeRecognizer and
    CryptoRecognizer carry no word boundary at all, so they match happily in the
    middle of an identifier. AGENTS.md's rule applies directly -- an identifier
    cut short is a FALSE identifier -- and a licence number spliced out of a
    symbol name is not a licence number.

    Applied only to the region-scoped types (see _LEGACY_TYPES), so the measured
    behaviour of the original four is untouched by this widening.
    """
    if start > 0 and (text[start - 1].isalnum() or text[start - 1] == "_"):
        return True
    if end < len(text) and (text[end].isalnum() or text[end] == "_"):
        return True
    return False


def _trim_span(text: str, start: int, end: int) -> tuple[int, int]:
    """Pull whitespace out of the span's edges.

    Not cosmetic. ItVatCodeRecognizer's pattern is `\\b([0-9][ _]?){11}\\b`, whose
    optional separator is allowed to be the LAST character, so it reports
    `"18415253675 "` -- trailing space included. Two things break on that: the
    published mask carries a stray character, and the delimitation gate below
    reads the wrong neighbour and rejects a real detection. Offsets are the whole
    contract of this module, so they are made exact here, once, before any gate
    or any mask sees them.
    """
    while start < end and text[start].isspace():
        start += 1
    while end > start and text[end - 1].isspace():
        end -= 1
    return start, end


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


def default_regions() -> tuple[str, ...]:
    """The regions in force when a caller names none.

    `KELD_PII_REGIONS` (comma-separated, e.g. "us,uk") is the operator lever and
    the sidecar-side half of the setting; the daemon sends `regions` explicitly
    on every /pii request once an org value is in play, so this is the fallback
    for a bare sidecar (scripts/pii_precision.py, a manual curl). Empty or unset
    means DEFAULT_REGIONS. Unknown codes are dropped by _normalize_regions, not
    treated as an error -- an org setting is free text and a typo must not take
    the facet down.
    """
    raw = os.environ.get(REGIONS_ENV, "")
    parsed = _normalize_regions(raw.split(","))
    return parsed if parsed else DEFAULT_REGIONS


def _normalize_regions(regions) -> tuple[str, ...]:
    """Lowercase, trim, drop unknown/empty, dedupe, and SORT.

    Sorted because the tuple is the engine cache key: ["us","uk"] and ["uk","us"]
    must not build two analyzers.
    """
    if not regions:
        return ()
    seen = []
    for r in regions:
        if not isinstance(r, str):
            continue
        code = r.strip().lower()
        if code in REGION_RECOGNIZERS and code not in seen:
            seen.append(code)
    return tuple(sorted(seen))


def _build_engine(regions: tuple[str, ...]):
    from presidio_analyzer import AnalyzerEngine, RecognizerRegistry
    from presidio_analyzer import predefined_recognizers as pr

    specs = list(UNIVERSAL_RECOGNIZERS)
    for code in regions:
        specs.extend(REGION_RECOGNIZERS[code])

    registry = RecognizerRegistry()
    entities = []
    for class_name, lang in specs:
        cls = getattr(pr, class_name)
        # The language override is what makes a locale-bound recognizer visible
        # to an `en` analysis at all; see UNIVERSAL_RECOGNIZERS' comment.
        rec = cls(supported_language=lang) if lang else cls()
        registry.add_recognizer(rec)
        entities.extend(rec.supported_entities)

    engine = AnalyzerEngine(
        nlp_engine=_nlp_engine(),
        registry=registry,
        supported_languages=["en"],
    )
    return engine, entities


def _get_engine(regions: tuple[str, ...]):
    """The analyzer for one region set, built once per set and cached.

    Keyed on the normalized region tuple because that is the only per-caller
    variation there is: the pipeline is a blank tokenizer this module owns and
    the universal tier never changes. The cache is unbounded ON PURPOSE and is
    still bounded in fact -- _normalize_regions only admits codes from
    REGION_RECOGNIZERS, so an adversarial caller cannot grow it past the number
    of subsets an org would ever configure, and in practice one process sees one
    or two.
    """
    engine = _engines.get(regions)
    if engine is None:
        with _engine_lock:
            engine = _engines.get(regions)
            if engine is None:
                engine = _build_engine(regions)
                _engines[regions] = engine
    return engine


def scan(text: str, regions=None, nlp=None) -> list[dict]:
    """Find leaked personal data in `text`.

    Returns spans sorted by position, each `{"type","start","end","score"}` with
    `type` drawn only from _ENTITY_MAP's values -- the universal tier plus
    whichever regions are in force. Published test/example values are filtered
    out, as are numeric fragments and slices of longer tokens. Never raises on
    empty or non-string input.

    `regions` selects the region-scoped recognizers (see REGION_RECOGNIZERS).
    None means default_regions(); an EMPTY list means the universal tier only,
    and is distinct from None on purpose -- "this org wants nothing
    country-specific" is a real answer, not an absent one. Unknown codes are
    ignored.

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

    codes = default_regions() if regions is None else _normalize_regions(regions)
    engine, entities = _get_engine(codes)

    results = engine.analyze(
        text=text,
        language="en",
        entities=entities,
        score_threshold=SCORE_THRESHOLD,
    )

    out = []
    for r in results:
        kind = _ENTITY_MAP.get(r.entity_type)
        if kind is None:  # defensive: registry is restricted, but never leak a
            continue      # presidio-native name into the published vocabulary
        start, end = _trim_span(text, int(r.start), int(r.end))
        if start >= end:
            continue
        value = text[start:end]
        if is_well_known(value, kind):
            continue
        # `email` is deliberately EXCLUDED from the fragment gate:
        # `mailto:dana@x.co` is a real address and the left rule would reject it
        # on the colon. The email-shaped false positives have their own,
        # structural gates in app.wellknown.
        if kind not in _NON_NUMERIC_TYPES and _is_number_fragment(text, start, end):
            continue
        if kind not in _LEGACY_TYPES and _is_glued_to_a_longer_token(text, start, end):
            continue
        if kind == "phone" and (_is_bare_digit_run(value) or _too_few_digits_for_phone(value)):
            continue
        out.append({
            "type": kind,
            "start": start,
            "end": end,
            "score": float(r.score),
        })

    out.sort(key=lambda d: (d["start"], d["end"]))
    return out
