"""The published-test-value gate for PII detection.

This is the precision half of the sensitivity facet, and the single thing the
detector cannot ship without. It plays the same role for PII that
internal/agent/enrich/creddetect/placeholder.go plays for credentials: a value
that is structurally perfect but PUBLISHED as an example is not leaked data.

Ported from internal/agent/enrich/piidetect/wellknown.go (commit d8ef9ab), which
the Go side drops in favour of this module. Two rules are added here that the Go
version lacked, both driven by measured presidio behaviour: a sequential-digit
filler rule (presidio reports `1234567890` as a phone at 0.4, and every digit is
distinct so the distinct-count rule cannot see it) and the NANP 555-01XX
fictional block.

Developer transcripts are saturated with these values: 4111 1111 1111 1111 is
the canonical Visa sandbox number and passes Luhn; 123-45-6789 is the textbook
SSN; user@example.com is reserved by RFC 2606. Ungated, every engineer's
transcript reports pci/phi continuously and the facet becomes noise — strictly
worse than not having it at all.

Everything listed below is a documentation constant. None is an account, a
person, or an address.
"""
from __future__ import annotations

# Published test card numbers, normalized to digits. Sources: the public sandbox
# documentation of the major card networks and processors.
_KNOWN_TEST_CARDS = {
    # Visa
    "4111111111111111", "4012888888881881", "4222222222222",
    "4242424242424242", "4000056655665556", "4000000000000002",
    "4917610000000000", "4001919257537193",
    # Mastercard
    "5555555555554444", "5105105105105100", "5200828282828210",
    "5555555555554477", "2223003122003222", "2223000048410010",
    # American Express
    "378282246310005", "371449635398431", "378734493671000",
    "343434343434343",
    # Discover
    "6011111111111117", "6011000990139424", "6011000991300009",
    "6011981111111113",
    # JCB
    "3530111333300000", "3566002020360505", "3566111111111113",
    # Diners Club
    "30569309025904", "38520000023237", "36227206271667",
    "3056930009020004",
    # UnionPay / Maestro sandbox numbers from the same docs
    "6200000000000005", "6759649826438453",
}

# Published example SSNs. 123-45-6789 is the textbook placeholder; 078-05-1120
# is the Woolworth wallet-card number copied by hundreds of thousands of people;
# 219-09-9999 appeared in a 1938 advertisement. 987-65-432x is the block the SSA
# reserves for advertising (handled by prefix below).
_KNOWN_EXAMPLE_SSNS = {"123456789", "078051120", "219099999"}

# Reserved for documentation and testing by RFC 2606 and RFC 6761, plus the
# conventional .localdomain. An address at one of these is never a person's.
_RESERVED_DOMAINS = (
    "example", "test", "invalid", "localhost", "localdomain",
    "example.com", "example.net", "example.org",
)


def is_well_known(value: str, kind: str) -> bool:
    """Report whether `value` is a published test/example value, and therefore
    must not be reported as leaked data.

    `kind` steers which check applies but is NOT trusted to be right: an NER
    reads 078-05-1120 as a phone number about as often as an SSN (verified —
    presidio labels it PHONE_NUMBER), and a documentation constant does not stop
    being one because it arrived under a different label. So any value carrying
    nine or more digits is re-checked against the SSN and card sets whatever the
    label says, while values with no digits — names, addresses — are never gated
    at all.

    It is a gate, not a classifier: when in doubt it returns False and the value
    is reported.
    """
    if not value:
        return False
    kind = (kind or "").lower()

    if kind == "credit_card":
        return _known_card(_digits(value))
    if kind == "ssn":
        return _known_ssn(_digits(value))
    if kind == "email":
        return _reserved_email(value)
    if kind == "phone":
        return _known_phone(value)

    # Unlabelled / person / address: only the digit-bearing constants apply.
    d = _digits(value)
    if len(d) < 9:  # below SSN length: too short to be one of these constants
        return False
    return _known_ssn(d) or _known_card(d)


def _known_card(d: str) -> bool:
    if not d:
        return False
    if d in _KNOWN_TEST_CARDS:
        return True
    # Filler shape: a number built from at most two distinct digits
    # (4444...48, 5454...54) is fabricated even when Luhn-valid under a real
    # IIN. It generalises past any list, and the odds of a real account
    # matching are negligible next to the cost of a false "pci".
    return _is_filler(d)


def _known_ssn(d: str) -> bool:
    if len(d) != 9:
        return False
    if d in _KNOWN_EXAMPLE_SSNS:
        return True
    if d.startswith("98765432"):  # SSA advertising block 987-65-432x
        return True
    # Structurally impossible areas/groups/serials: the SSA never issues them,
    # so a value of this shape is always a placeholder (000-00-0000).
    if d[:3] in ("000", "666") or d[:3] >= "900":
        return True
    if d[3:5] == "00" or d[5:] == "0000":
        return True
    return _is_filler(d)


def _known_phone(value: str) -> bool:
    d = _digits(value)
    if len(d) < 7:
        return False
    # Mislabelled documentation constants arrive here constantly.
    if len(d) >= 9 and (_known_ssn(d) or _known_card(d)):
        return True
    nanp = d[1:] if len(d) == 11 and d.startswith("1") else d
    # NANP 555-0100..555-0199 is reserved for fictional use.
    if len(nanp) == 10 and nanp[3:6] == "555" and nanp[6:8] == "01":
        return True
    if len(nanp) == 7 and nanp[:3] == "555" and nanp[3:5] == "01":
        return True
    return _is_filler(nanp)


def _reserved_email(value: str) -> bool:
    at = value.rfind("@")
    if at < 0:
        return False
    domain = value[at + 1:].strip().strip(".").lower()
    if not domain:
        return False
    return any(domain == r or domain.endswith("." + r) for r in _RESERVED_DOMAINS)


def _is_filler(d: str) -> bool:
    """A digit run that is documentation rather than data.

    Two shapes: too few distinct digits (000-00-0000, 1111111111111111), and a
    strictly consecutive run (1234567890) where every digit is distinct so the
    count rule is blind to it. Both are narrow enough that a real value
    matching one is vanishingly unlikely next to the cost of a false report.
    """
    if not d:
        return False
    if len(set(d)) <= 2:
        return True
    return _is_sequential(d)


def _is_sequential(d: str) -> bool:
    if len(d) < 4:
        return False
    ns = [int(c) for c in d]
    up = all((b - a) % 10 == 1 for a, b in zip(ns, ns[1:]))
    down = all((a - b) % 10 == 1 for a, b in zip(ns, ns[1:]))
    return up or down


def _digits(s: str) -> str:
    return "".join(c for c in s if c.isdigit())
