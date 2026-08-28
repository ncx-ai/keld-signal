"""Standalone tests for PII detection. Run:
  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_pii.py

scan() wraps presidio-analyzer and returns spans in the EXISTING sensitivity
vocabulary (see internal/agent/enrich/labels.go: ssn -> phi, credit_card -> pci,
email/phone/person/address -> pii). Two properties matter and they pull against
each other:

  1. it must actually find leaked data, and
  2. it must stay silent on the published test values and the structured noise
     a developer transcript is made of (version strings, ports, git SHAs,
     UUIDs, order ids, timestamps).

Precision wins ties: a false `phi` is worse than a miss, because the facet is
consumed as a signal about a real person.

MEASURED, AND THE VOCABULARY NARROWED AS A RESULT. On 2,000 real developer
prompts the detector published 1,090 spans of which at most 13 were genuine —
precision ~1%, and 24% of prompts published `sensitivity: pii`. 998 of those
spans were `person`/`address` from the spaCy NER (`JSON` x132, `Docker`,
`YAGNI`, exported Go identifiers, a bare emoji at 0.85), with zero confirmed
names and zero addresses. So `SpacyRecognizer` is gone and the two types are no
longer detected AT ALL — the tests below pin their ABSENCE, which is the
behaviour, not an omission. The remaining four types are pattern-matched.

ALL FIXTURES ARE WHOLLY SYNTHETIC — constructed, never observed. Cards are
Luhn-completed over arbitrary prefixes; the SSN is assembled from valid SSA
ranges; names/companies are invented. This is a privacy-critical repo and a
fixture containing real PII would itself be a defect.

These tests load a real spaCy model, so they are slower than the other suites.
"""
from app.pii import scan

REAL_LOOKING_VISA = "4539821746358200"
REAL_LOOKING_SSN = "456-72-8391"


def _types(text):
    return {r["type"] for r in scan(text)}


def _one(text, want_type):
    hits = [r for r in scan(text) if r["type"] == want_type]
    assert hits, f"expected {want_type} in {text!r}, got {scan(text)}"
    return hits[0]


# ---------------------------------------------------------------- positives

def test_detects_constructed_credit_card():
    r = _one(f"Charge the card {REAL_LOOKING_VISA} for the order.", "credit_card")
    assert r["score"] > 0.5


def test_detects_constructed_ssn():
    _one(f"Employee SSN {REAL_LOOKING_SSN} was in the payload.", "ssn")


def test_detects_email():
    _one("Email dana.whitfield@northwind-logistics.co about it.", "email")


def test_detects_phone():
    _one("Call the vendor at (415) 682-4470 tomorrow.", "phone")


def test_span_offsets_are_exact():
    text = f"Charge the card {REAL_LOOKING_VISA} for the order."
    r = _one(text, "credit_card")
    assert text[r["start"]:r["end"]] == REAL_LOOKING_VISA


def test_result_shape():
    for r in scan("Email dana.whitfield@northwind-logistics.co about it."):
        assert set(r) == {"type", "start", "end", "score"}
        assert isinstance(r["start"], int) and isinstance(r["end"], int)
        assert 0.0 <= r["score"] <= 1.0


def test_only_vocabulary_types_are_returned():
    # Dates, IPs, URLs and MAC addresses stay unreported: none is leaked PERSONAL
    # data and each fires on ordinary developer text constantly.
    #
    # The IBAN here is GB33BUKB20201555555555, the Wikipedia example — and it is
    # in this list for a DIFFERENT reason than the rest. IBAN_CODE became a
    # universal type when the checksum recognizers were widened, so this value is
    # now detected and then dropped by the published-value gate
    # (app.wellknown._KNOWN_EXAMPLE_IBANS), not by the registry. A constructed
    # IBAN in the same sentence WOULD be reported; see app/test_pii_regions.py.
    text = ("On 2026-08-23 Dana Whitfield emailed dana@northwind-logistics.co "
            "from 10.2.14.9 about https://internal.example.co/runbook using "
            "MAC 3c:22:fb:81:aa:04 and IBAN GB33BUKB20201555555555.")
    allowed = {"ssn", "credit_card", "email", "phone", "iban", "crypto_wallet"}
    assert _types(text) <= allowed, _types(text)
    assert "iban" not in _types(text), "the Wikipedia example IBAN must stay gated"


# ---------------------------------------------------------------- negatives

def test_published_test_cards_are_not_reported():
    for card in ["4111 1111 1111 1111", "4012888888881881", "4242424242424242",
                 "5555555555554444", "5105105105105100", "378282246310005",
                 "371449635398431", "6011111111111117", "3530111333300000",
                 "30569309025904"]:
        assert "credit_card" not in _types(f"Use {card} in the sandbox."), card


def test_textbook_ssns_are_not_reported():
    for ssn in ["123-45-6789", "078-05-1120"]:
        got = _types(f"The SSN {ssn} is the documented example.")
        assert "ssn" not in got and "phone" not in got, (ssn, got)


def test_reserved_domains_are_not_reported():
    for addr in ["user@example.com", "dev@example.org", "x@example.net",
                 "root@localhost", "q@foo.test", "z@thing.invalid"]:
        assert "email" not in _types(f"Mail {addr} for docs."), addr


def test_version_strings_are_not_reported():
    for t in ["Upgrade to 1.2.3.4 and restart.",
              "Pinned numpy 2.5.0 and torch 2.12.1 today.",
              "Bumped from v10.4.1 to v10.4.2."]:
        assert _types(t) == set(), (t, _types(t))


def test_ports_are_not_reported():
    for t in ["Listening on 127.0.0.1:8080 now.",
              "Bind the sidecar to port 33098 instead.",
              "Forward 5432 to 15432 for the db."]:
        assert _types(t) == set(), (t, _types(t))


def test_git_shas_are_not_reported():
    for t in ["Reverted commit 9f2c1ab4d5e6f708192a3b4c5d6e7f8091a2b3c4 today.",
              "Cherry-pick d8ef9ab onto the branch.",
              "Diff 27eb416..f63d524 shows the change."]:
        assert _types(t) == set(), (t, _types(t))


def test_uuids_are_not_reported():
    for t in ["Trace 3f2504e0-4f89-11d3-9a0c-0305e82c3301 dropped.",
              "Job id 550e8400-e29b-41d4-a716-446655440000 requeued."]:
        assert _types(t) == set(), (t, _types(t))


def test_long_numeric_ids_are_not_reported():
    for t in ["Order id 900123456789 failed to sync.",
              "Row 8823410099231 was rejected.",
              "The job retried 1234567890 times."]:
        assert _types(t) == set(), (t, _types(t))


def test_timestamps_are_not_reported():
    for t in ["at 2026-08-23T14:05:09Z the worker recycled",
              "epoch 1755950709 recorded",
              "took 1250ms across 3 attempts"]:
        assert _types(t) == set(), (t, _types(t))


def test_code_identifiers_are_not_reported():
    for t in ["Call enrich.WithJobContext then runStage on the profile.",
              "Set KELD_ENRICH_PASS_TIMEOUT=30s in the unit file.",
              "The func digitsOnly(d string) int helper returns a count."]:
        assert _types(t) == set(), (t, _types(t))


def test_formatted_phones_survive_the_bare_run_rule():
    # The bare-digit-run rule exists to kill epochs and counters; it must not
    # take the formatted shapes a real leaked number arrives in.
    for t in ["Call the vendor at (415) 682-4470 tomorrow.",
              "Reach them on 415-682-4470 please.",
              "Dial +1 415 682 4470 for support."]:
        assert "phone" in _types(t), t


def test_person_and_address_are_never_reported():
    # The NER-derived types are GONE, not merely gated. Pin both halves of that:
    # the ordinary developer text that used to produce them, and the real names
    # and addresses it also no longer produces — because the honest statement of
    # this change is that the coverage narrowed, not that the noise was tuned out.
    noise = ["Serialize it to JSON before the Docker build.",
             "Verdict: YAGNI. The Worker and Store stay as they are.",
             "Getenv, Resync() and Drain() all need the PATH set.",
             "❌ CANNOT ship — the UI is ~250MB over budget (#ADADAA)."]
    names = ["Dana Whitfield approved the change.",
             "The office is at 1847 Kingsbury Avenue, Portland.",
             "Jean-Luc Bernard and O'Brien both signed off in Rotterdam."]
    for t in noise + names:
        got = _types(t)
        assert "person" not in got and "address" not in got, (t, got)


def test_the_spacy_recognizer_is_not_registered():
    # Belt and braces on the test above: no threshold separates `JSON` from a
    # name (a bare emoji scored 0.85), so the fix has to be the ABSENCE of the
    # recognizer, not a stricter gate over its output. Assert the registry.
    from app.pii import REGION_RECOGNIZERS, _ENTITY_MAP, _get_engine, _normalize_regions
    assert "PERSON" not in _ENTITY_MAP and "LOCATION" not in _ENTITY_MAP
    # Every region set, not just the default: the widening added twelve more
    # registries and the NER must be absent from all of them.
    supported = set()
    for regions in ((), _normalize_regions(list(REGION_RECOGNIZERS))):
        engine, _ = _get_engine(regions)
        for rec in engine.registry.recognizers:
            supported.update(rec.supported_entities)
    assert "PERSON" not in supported and "LOCATION" not in supported, supported


def test_number_sliced_out_of_a_longer_token_is_not_reported():
    # The worst measured finding: the 13 digits AFTER the decimal point of a
    # 17-digit float, Luhn-passing by coincidence, published as `pci` at 1.0.
    # The float below is synthetic and its fractional tail is Luhn-completed on
    # purpose, so this fails without the fragment gate.
    for t in ["Throughput settled at 1234.4539821746358 per second.",
              "The ratio came out 0.9/4539821746358200 in the run.",
              "Row 88-4539821746358200 was rejected by the loader."]:
        assert "credit_card" not in _types(t), (t, _types(t))


def test_file_line_ranges_and_ratios_are_not_phones():
    # 11 of 11 measured `phone` hits. Both shapes are endemic to developer text.
    for t in ["See server.go:118-140 for the retry loop.",
              "`internal/agent/daemon/daemon.go:1620-1647` is the wiring.",
              "Weights 0.5/0.3/0.7 gave the best score.",
              "Split (0.25/0.5/0.7) across the three passes."]:
        assert "phone" not in _types(t), (t, _types(t))


def test_real_numbers_next_to_ordinary_punctuation_survive():
    # The cost side of the fragment gate, pinned. A trailing comma or full stop
    # is prose, not a longer token — rejecting on it (which the whitespace-token
    # rule the measurement proposed would do) would cost real detections.
    assert "phone" in _types("Reach them on 415-682-4470, then email.")
    assert "phone" in _types("Dial +1 415 682 4470.")
    assert "credit_card" in _types(f"The card is {REAL_LOOKING_VISA}.")
    assert "credit_card" in _types(f"Charged {REAL_LOOKING_VISA}, twice.")
    assert "credit_card" in _types(f'Field "card": "{REAL_LOOKING_VISA}" leaked.')


def test_short_digit_runs_are_not_phones():
    # NANP is ten digits; a bare 7-digit local number is not resolvable to a
    # person, and every measured 7-digit hit was a line range.
    for t in ["Lines 118-140 changed.", "Bumped 250-400 rows."]:
        assert "phone" not in _types(t), (t, _types(t))


def test_machine_email_addresses_are_not_reported():
    # 66 of 78 measured `email` spans were one no-reply address quoted out of a
    # Co-Authored-By git trailer.
    for t in ["Co-Authored-By: Some Bot <noreply@northwind-logistics.co>",
              "Remote is git@github.com:northwind/logistics.git",
              "Pinned northwind-logistics.co/toolkit@v1.23.4 in go.mod."]:
        assert "email" not in _types(t), (t, _types(t))


def test_empty_input_is_safe():
    assert scan("") == []
    assert scan("   ") == []
    assert scan(None) == []


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
