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


def test_detects_person():
    _one("Dana Whitfield approved the change.", "person")


def test_detects_address_as_location():
    _one("The office is at 1847 Kingsbury Avenue, Portland.", "address")


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
    text = ("On 2026-08-23 Dana Whitfield emailed dana@northwind-logistics.co "
            "from 10.2.14.9 about https://internal.example.co/runbook using "
            "MAC 3c:22:fb:81:aa:04 and IBAN GB33BUKB20201555555555.")
    allowed = {"ssn", "credit_card", "email", "phone", "person", "address"}
    assert _types(text) <= allowed, _types(text)


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


def test_code_shape_gate_keeps_real_names():
    # The code-shape gate must not swallow the names it sits next to. These
    # score identically (0.85) to the identifiers it rejects, so shape is the
    # only thing separating them — pin both directions.
    from app.pii import _is_code_like
    for ident in ["runStage", "digitsOnly(d string", "enrich.WithJobContext",
                  "KELD_ENRICH_PASS_TIMEOUT", "app.pii", "scanText"]:
        assert _is_code_like(ident), ident
    for name in ["Dana Whitfield", "Portland", "1847 Kingsbury Avenue",
                 "Dana W. Smith", "O'Brien", "Jean-Luc Bernard"]:
        assert not _is_code_like(name), name


def test_empty_input_is_safe():
    assert scan("") == []
    assert scan("   ") == []
    assert scan(None) == []


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
