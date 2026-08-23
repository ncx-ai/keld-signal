"""Standalone tests for the published-test-value gate. Run:
  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_wellknown.py

This is the precision half of PII detection. Presidio happily reports
4111 1111 1111 1111 as a credit card at score 1.0 (it passes Luhn) and
user@example.com as an email — both are PUBLISHED documentation values, and a
developer transcript is saturated with them. Ungated, every engineer's prompts
report pci/pii continuously and the sensitivity facet becomes noise, which is
strictly worse than not having the facet at all.

ALL FIXTURES HERE ARE SYNTHETIC. The card numbers are the card networks' own
published sandbox constants and the SSNs are the published textbook examples;
neither is an account or a person. The "real-looking" values used as positives
were CONSTRUCTED (Luhn completed over an arbitrary prefix / assembled from valid
SSA ranges), not observed.
"""
from app.wellknown import is_well_known

# Constructed, NOT observed: an arbitrary 15-digit prefix with the Luhn check
# digit computed to close it. Valid structure, 10 distinct digits, absent from
# every published sandbox list — the shape a real leak has.
REAL_LOOKING_VISA = "4539821746358200"
REAL_LOOKING_MC = "5419837624180732"
# Area 456 (a valid SSA area, not 000/666/9xx), group 72, serial 8391.
REAL_LOOKING_SSN = "456-72-8391"


def test_published_test_cards_are_gated():
    # Every major brand's canonical sandbox number. All pass Luhn, so the
    # detector cannot tell them apart from an account — only this list can.
    for card in [
        "4111 1111 1111 1111", "4111111111111111", "4012888888881881",
        "4242424242424242", "4000056655665556",
        "5555555555554444", "5105105105105100", "2223003122003222",
        "378282246310005", "371449635398431",
        "6011111111111117", "6011000990139424",
        "3530111333300000", "3566002020360505",
        "30569309025904", "38520000023237",
        "6200000000000005",
    ]:
        assert is_well_known(card, "credit_card"), card


def test_real_looking_cards_are_not_gated():
    # The gate must not swallow a genuine leak.
    assert not is_well_known(REAL_LOOKING_VISA, "credit_card")
    assert not is_well_known(REAL_LOOKING_MC, "credit_card")


def test_textbook_ssns_are_gated():
    assert is_well_known("123-45-6789", "ssn")
    assert is_well_known("123456789", "ssn")
    assert is_well_known("078-05-1120", "ssn")   # the Woolworth wallet card
    assert is_well_known("219-09-9999", "ssn")   # the 1938 advertisement
    assert is_well_known("987-65-4329", "ssn")   # SSA advertising block


def test_filler_digit_runs_are_gated():
    # Documentation, not data: too few distinct digits to be anything real.
    assert is_well_known("000-00-0000", "ssn")
    assert is_well_known("111-11-1111", "ssn")
    assert is_well_known("1111111111111111", "credit_card")
    assert is_well_known("4444444444444448", "credit_card")


def test_sequential_digit_runs_are_gated():
    # 1234567890 is the other filler shape — every digit distinct, so the
    # distinct-count rule cannot see it, but it is never a real number.
    assert is_well_known("1234567890", "phone")
    assert is_well_known("123-456-7890", "phone")


def test_real_looking_ssn_is_not_gated():
    assert not is_well_known(REAL_LOOKING_SSN, "ssn")
    assert not is_well_known("456728391", "ssn")


def test_reserved_domains_are_gated():
    for addr in [
        "user@example.com", "a.b@example.org", "x@example.net",
        "dev@sub.example.com", "root@localhost", "q@foo.test",
        "q@thing.invalid", "n@box.localdomain",
    ]:
        assert is_well_known(addr, "email"), addr


def test_machine_local_parts_are_gated():
    # 85% of the measured `email` volume was one no-reply address out of a
    # Co-Authored-By git trailer. An unattended sink is not personal data.
    for addr in ["noreply@northwind-logistics.co", "no-reply@northwind-logistics.co",
                 "donotreply@Northwind-Logistics.CO", "mailer-daemon@northwind-logistics.co",
                 "notifications@northwind-logistics.co", "git@github.com"]:
        assert is_well_known(addr, "email"), addr


def test_human_role_accounts_are_not_gated():
    # The line is "no person is behind this address", not "this address is
    # uninteresting". A staffed role account routes to a human.
    for addr in ["admin@northwind-logistics.co", "support@northwind-logistics.co",
                 "info@northwind-logistics.co", "sales@northwind-logistics.co"]:
        assert not is_well_known(addr, "email"), addr


def test_module_paths_and_version_domains_are_gated():
    # `host.tld/pkg@v1.23.4` parses as local-part-at-domain and scored 1.0.
    for v in ["northwind-logistics.co/toolkit@v1.23.4", "example-host.co/x/y@v0.1.2"]:
        assert is_well_known(v, "email"), v
    # ...but a punycode IDN top-level label is a real domain and must survive.
    assert not is_well_known("dana@northwind.xn--p1ai", "email")


def test_real_looking_email_is_not_gated():
    assert not is_well_known("dana.whitfield@northwind-logistics.co", "email")
    assert not is_well_known("ops@acme-internal.io", "email")


def test_fictional_555_phone_block_is_gated():
    # 555-0100..555-0199 is reserved for fiction; dev docs are full of them.
    assert is_well_known("(415) 555-0142", "phone")
    assert is_well_known("555-0100", "phone")
    # Outside the reserved block, 555 numbers are assignable — do not gate.
    assert not is_well_known("(415) 555-2671", "phone")


def test_real_looking_phone_is_not_gated():
    assert not is_well_known("(415) 682-4470", "phone")
    assert not is_well_known("+1 415 682 4470", "phone")


def test_mislabelled_values_are_still_gated():
    # The label is NOT trusted: an NER reads 078-05-1120 as a phone about as
    # often as an SSN, and a documentation constant does not stop being one
    # because it arrived under a different label. Verified live: presidio
    # reports 078-05-1120 as PHONE_NUMBER, not US_SSN.
    assert is_well_known("078-05-1120", "phone")
    assert is_well_known("123-45-6789", "phone")
    assert is_well_known("4111111111111111", "phone")


def test_names_and_places_are_never_gated():
    # No digits: nothing to compare against a published-constant list, so the
    # gate must fall through rather than guess.
    assert not is_well_known("Dana Whitfield", "person")
    assert not is_well_known("Portland", "address")
    assert not is_well_known("1847 Kingsbury Avenue", "address")


def test_empty_and_short_values_do_not_crash():
    assert not is_well_known("", "ssn")
    assert not is_well_known("", "credit_card")
    assert not is_well_known("abc", "email")
    assert not is_well_known("42", "phone")
    assert not is_well_known("x", "")


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
