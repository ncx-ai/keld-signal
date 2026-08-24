"""Standalone tests for the REGION-SCOPED checksum recognizers. Run:
  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_pii_regions.py

app.pii's original four types (ssn, credit_card, email, phone) are pinned by
test_pii.py. This suite pins the widening: presidio's checksum- and
algorithm-validated recognizers, enabled per REGION rather than all at once.

WHY REGION-SCOPED AND NOT ALL-ON — the collisions are measured, not theoretical.
Almost every national-id recognizer is a bare digit run plus ONE check digit, so
its false-positive floor against arbitrary numbers is 1-in-10 or 1-in-11, and the
shapes overlap each other:

  * a valid US_NPI (10 digits, `[12]\\d{9}`) has a ~1-in-11 chance of also passing
    UK_NHS's mod-11 — and UK_NHS rolls up to `phi`, the most severe class. Turning
    `uk` on inside a US-only org therefore manufactures `phi` out of provider ids.
  * a constructed AU_MEDICARE validates as UK_NHS too (verified below).
  * a constructed SG_UEN validates as ES_NIF too (verified below).

Region scoping is what keeps those cross-firings out of a deployment that has no
business seeing them. `universal` carries only the identifiers that are not
bounded by where an org operates.

ALL FIXTURES ARE WHOLLY SYNTHETIC. Each was CONSTRUCTED, never observed: a seeded
pseudo-random generator drew candidates of the right shape, and only those the
recognizer's own published check algorithm accepts (presidio's `validate_result`)
were kept, with filler shapes (<=2 distinct digits, or a strictly consecutive
run) rejected so they survive the well-known gate. No value here was copied from
any real document, register, corpus or web page. The crypto address is a
base58check encoding of 20 random bytes.
"""
import os

from app.pii import DEFAULT_REGIONS, REGION_RECOGNIZERS, default_regions, scan

# --- synthetic, checksum-valid, one per recognizer ---------------------------
VALID = {
    # universal
    "iban": "GB94NWBK10426301779493",
    "crypto_wallet": "1FhJSoC1nS6aerwNE211eMiFQ4jqGqsShE",
    # us (default)
    "aba_routing": "098752030",
    "us_npi": "1122302858",
    "medical_license": "RS7825574",
    # uk
    "uk_nhs": "1372711945",
    # es
    "es_nif": "97458228Y",
    "es_nie": "X2299487Q",
    # it
    "it_fiscal_code": "NKKTVZ69R10L743D",
    "it_vat_code": "18415253675",
    # pl / fi
    "pl_pesel": "67122595850",
    "fi_personal_identity_code": "070191-406W",
    # kr
    "kr_rrn": "140903-3501637",
    "kr_driver_license": "14-33-029199-63",
    "kr_brn": "406-94-50065",
    "kr_frn": "380604-7830885",
    # in
    "in_aadhaar": "539349438350",
    "in_gstin": "18TKARF6189K0ZV",
    # au
    "au_tfn": "493160033",
    "au_abn": "22982932101",
    "au_acn": "204646926",
    "au_medicare": "4789563979",
    # ng / th / sg
    "ng_nin": "33907758915",
    "th_tnin": "7373197686335",
    "sg_uen": "47104826K",
}

# Which region turns each label on. "" means the universal tier.
REGION_OF = {
    "iban": "", "crypto_wallet": "",
    "aba_routing": "us", "us_npi": "us", "medical_license": "us",
    "uk_nhs": "uk",
    "es_nif": "es", "es_nie": "es",
    "it_fiscal_code": "it", "it_vat_code": "it",
    "pl_pesel": "pl",
    "fi_personal_identity_code": "fi",
    "kr_rrn": "kr", "kr_driver_license": "kr", "kr_brn": "kr", "kr_frn": "kr",
    "in_aadhaar": "in", "in_gstin": "in",
    "au_tfn": "au", "au_abn": "au", "au_acn": "au", "au_medicare": "au",
    "ng_nin": "ng", "th_tnin": "th", "sg_uen": "sg",
}

ALL_REGIONS = sorted(REGION_RECOGNIZERS)


def _types(text, regions=None):
    return {r["type"] for r in scan(text, regions=regions)}


# ---------------------------------------------------------------- the tiers

def test_default_regions_is_us():
    assert DEFAULT_REGIONS == ("us",)


def test_every_region_recognizer_actually_registers_and_validates():
    # The list came from introspection and a name that does not register for
    # `en` would be a silently dead tier. Each fixture must come back at 1.0 —
    # presidio sets MAX_SCORE only when validate_result() returned True, so a
    # score below 1.0 means the checksum never ran.
    for label, value in VALID.items():
        region = REGION_OF[label]
        regions = [region] if region else []
        hits = [r for r in scan(f"The value is {value} here.", regions=regions)
                if r["type"] == label]
        assert hits, f"{label} did not fire under region {region!r}"
        assert hits[0]["score"] >= 1.0, f"{label} fired UNVALIDATED at {hits[0]['score']}"


def test_universal_tier_needs_no_region():
    # IBAN and a crypto wallet carry their own checksums and are not bounded by
    # where an org operates: an IBAN in a US prompt is still a leaked account.
    for label in ("iban", "crypto_wallet"):
        assert label in _types(f"Send it to {VALID[label]} today.", regions=[])


def test_region_scoping_is_actually_scoped():
    # The load-bearing property. Each region-scoped label must be SILENT under
    # the default region set.
    for label, value in VALID.items():
        if not REGION_OF[label] or REGION_OF[label] == "us":
            continue
        assert label not in _types(f"The value is {value} here.", regions=["us"]), label


def test_us_is_what_the_default_gives_you():
    for label in ("aba_routing", "us_npi", "medical_license"):
        assert label in _types(f"The value is {VALID[label]} here.")


def test_unknown_region_is_ignored_not_fatal():
    # An org setting is a free-text field; a typo must not take the facet down.
    assert scan("nothing here", regions=["us", "atlantis"]) == []
    assert "us_npi" in _types(f"NPI {VALID['us_npi']} on file.", regions=["atlantis", "us"])


def test_regions_are_case_and_space_insensitive():
    assert "uk_nhs" in _types(f"NHS {VALID['uk_nhs']} recorded.", regions=[" UK "])


def test_env_default_is_honoured():
    old = os.environ.get("KELD_PII_REGIONS")
    try:
        os.environ["KELD_PII_REGIONS"] = "uk, in"
        # Sorted: the tuple is the engine cache key, so "uk,in" and "in,uk" must
        # not build two analyzers.
        assert default_regions() == ("in", "uk")
        os.environ["KELD_PII_REGIONS"] = ""
        assert default_regions() == DEFAULT_REGIONS
        del os.environ["KELD_PII_REGIONS"]
        assert default_regions() == DEFAULT_REGIONS
    finally:
        if old is None:
            os.environ.pop("KELD_PII_REGIONS", None)
        else:
            os.environ["KELD_PII_REGIONS"] = old


# ---------------------------------------------------------------- the gates

def test_the_measured_corpus_false_positives_stay_gated():
    # The ONLY spans the whole candidate set produced over 2,000 real developer
    # prompts were three readings of one digit run in a URL path — AU_ABN,
    # IT_VAT_CODE and PL_PESEL over the same characters. All three sit directly
    # after a `/`, which is what the fragment gate keys on; this pins that the
    # gate covers the NEW numeric types and not just the original four.
    text = "See https://example.com/api-v2/team-space/threads/root/22982932101 for the log."
    assert scan(text, regions=ALL_REGIONS) == [], scan(text, regions=ALL_REGIONS)


def test_a_validated_id_glued_into_a_longer_token_is_not_reported():
    # MedicalLicenseRecognizer's DEA pattern carries NO \b anchors, so it happily
    # matches inside a longer identifier. A slice of a symbol is a false
    # identifier, never a licence number.
    assert "medical_license" not in _types(f"const {VALID['medical_license']}xyz = 1")
    assert "medical_license" not in _types(f"abc{VALID['medical_license']} = 1")


def test_filler_values_are_well_known_not_leaks():
    # Documentation constants reach the new types exactly as they reach the old
    # ones. 000000000 and 111111111111 pass several of these checksums.
    for regions, value in (
        (["au"], "000000000"),
        (["in"], "000000000000"),
        (["ng"], "00000000000"),
    ):
        assert scan(f"Example id {value} in the docs.", regions=regions) == [], value


def test_placeholder_and_empty_input_stay_safe():
    assert scan("", regions=ALL_REGIONS) == []
    assert scan(None, regions=ALL_REGIONS) == []


def test_result_shape_is_unchanged():
    for r in scan(f"IBAN {VALID['iban']} please.", regions=[]):
        assert set(r) == {"type", "start", "end", "score"}


def test_offsets_are_exact_for_a_new_type():
    text = f"Wire it to {VALID['iban']} tomorrow."
    hits = [r for r in scan(text, regions=[]) if r["type"] == "iban"]
    assert hits and text[hits[0]["start"]:hits[0]["end"]] == VALID["iban"]


def test_original_four_types_still_work_with_regions_set():
    # Widening must not disturb the measured behaviour of the original set.
    assert "email" in _types("Email dana.whitfield@northwind-logistics.co about it.",
                             regions=ALL_REGIONS)
    assert "credit_card" in _types("Charge the card 4539821746358200 for the order.",
                                   regions=ALL_REGIONS)
    assert "ssn" in _types("Employee SSN 456-72-8391 was in the payload.",
                           regions=ALL_REGIONS)


# ---------------------------------------------------------------- collisions

def test_cross_region_collisions_are_real_and_scoping_is_the_answer():
    # Documented, not hypothetical: these constructed values satisfy TWO
    # countries' check algorithms. With both regions on, both labels are
    # reported; with only the owning region on, only one is.
    assert _types(f"id {VALID['au_medicare']}", regions=["au"]) == {"au_medicare"}
    assert "uk_nhs" in _types(f"id {VALID['au_medicare']}", regions=["au", "uk"])

    assert _types(f"id {VALID['sg_uen']}", regions=["sg"]) == {"sg_uen"}
    assert "es_nif" in _types(f"id {VALID['sg_uen']}", regions=["sg", "es"])


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
