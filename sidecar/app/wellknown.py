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

Widened alongside the region-scoped recognizers (app.pii): IBAN and crypto
wallet addresses arrived as universal types and brought their own documentation
constants with them, and the digit-run fallback that serves every national/tax
identifier now looks at six digits rather than nine, because SG_UEN carries eight
and AU_ACN nine.

Everything listed below is a documentation constant. None is an account, a
person, an address, or a wallet anyone holds a key to.
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

# Published example IBANs. Every one of these is a documentation constant from a
# public standards page, a bank's own "how to read your IBAN" explainer or a
# payment library's fixtures -- and every one passes the mod-97 check, because
# that is the point of an example. Stored digits-and-letters only, uppercased,
# so spacing and case do not matter.
_KNOWN_EXAMPLE_IBANS = {
    "GB82WEST12345698765432", "GB33BUKB20201555555555", "GB29NWBK60161331926819",
    "DE89370400440532013000", "DE75512108001245126199",
    "FR1420041010050500013M02606", "FR7630006000011234567890189",
    "ES9121000418450200051332", "IT60X0542811101000000123456",
    "NL91ABNA0417164300", "BE68539007547034", "BE71096123456769",
    "CH9300762011623852957", "AT611904300234573201", "PT50000201231234567890154",
    "SE4550000000058398257466", "NO9386011117947", "DK5000400440116243",
    "PL61109010140000071219812874", "IE29AIBK93115212345678",
    "FI2112345600000785", "GR9608100010000001234567890",
}

# Published example crypto addresses. The genesis-block coinbase address is the
# single most-quoted string in Bitcoin documentation; the others are the standard
# BIP/test vectors. All are real, checksum-valid addresses -- which is exactly why
# the checksum cannot tell them from a leaked wallet.
_KNOWN_EXAMPLE_WALLETS = {
    "1A1ZP1EP5QGEFI2DMPTFTL5SLMV7DIVFNA",       # genesis block coinbase
    "1BOATSLRHTKNNGKDXEEOBOSWSJZLLKJCH",        # vanity address in every tutorial
    "1BVBMSEYSTWETQTFN5AU4M4GFG7XJANVN2",       # Bitfinex cold wallet, quoted everywhere
    "BC1QW508D6QEJXTDG4Y5R3ZARVARY0C5XW7KV8F3T4",   # BIP-173 test vector
    "BC1QAR0SRRR7XFKVY5L643LYDNW9RE59GTZZWF5MDQ",   # BIP-173 test vector
    "3J98T1WPEZ73CNMQVIECRNYIWRNQRHWNLY",       # BIP-16 example P2SH
}

# Local parts that belong to a MACHINE, not a person.
#
# Measured on 2,000 real developer prompts: 66 of the 78 `email` spans -- 85% of
# the entire type's volume, and the single largest false positive left after the
# NER came out -- were one no-reply address, quoted out of a `Co-Authored-By:`
# git trailer that every AI-assisted commit in the corpus carries. One more was
# the `git@github.com` of an SSH remote URL, which is a login, not a mailbox.
#
# This is the same idea as the reserved-domain rule above, one field to the left:
# an unattended sink is not personal data BY DEFINITION, whatever its structure
# says. The alternative -- leaving it as an honest detection of a real address --
# was rejected because publishing `sensitivity: pii` on a commit trailer tells an
# org nothing true about a person and trains them to ignore the facet, which is
# exactly the failure the whole measurement was about.
#
# Role accounts a HUMAN reads (`admin@`, `support@`, `info@`, `sales@`) are
# deliberately ABSENT: they route to people, and the measurement counted one as a
# true positive. The line drawn here is "no person is behind this address", not
# "this address is uninteresting".
_MACHINE_LOCAL_PARTS = {
    "noreply", "no-reply", "no_reply", "noreply-dev",
    "donotreply", "do-not-reply", "do_not_reply",
    "mailer-daemon", "mailerdaemon", "postmaster",
    "bounce", "bounces", "notifications", "notification",
    "automated", "auto-reply", "autoreply", "git",
}


def is_well_known(value: str, kind: str) -> bool:
    """Report whether `value` is a published test/example value, and therefore
    must not be reported as leaked data.

    `kind` steers which check applies but is NOT trusted to be right: an NER
    reads 078-05-1120 as a phone number about as often as an SSN (verified —
    presidio labels it PHONE_NUMBER), and a documentation constant does not stop
    being one because it arrived under a different label. So any value carrying
    nine or more digits is re-checked against the SSN and card sets whatever the
    label says, while values with no digits — names, addresses — are never gated
    at all. The region-scoped identifiers have no branch of their own: they fall
    through to the digit-run rules, which is the whole of what a published
    example national id ever is.

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
    if kind == "iban":
        return _known_iban(value)
    if kind == "crypto_wallet":
        return _known_wallet(value)

    # Everything else -- the region-scoped national/tax/licence identifiers,
    # plus the unlabelled/person/address fallthrough -- is judged on its digits
    # alone. There is no per-country list of published example ids to hold, and
    # there does not need to be: the shapes that get published as examples are
    # filler runs (000-00-0000, 111111111111) and consecutive runs (123456789012),
    # and _is_filler catches both at any length.
    d = _digits(value)
    if len(d) >= _MIN_CONSTANT_DIGITS and _is_filler(d):
        return True
    if len(d) < 9:  # below SSN length: too short to be one of the sets below
        return False
    return _known_ssn(d) or _known_card(d)


# Shortest digit run worth testing against the filler shapes. Six because
# SG_UEN carries eight digits and AU_ACN nine, while a house number ("1847
# Kingsbury Avenue") and a year must stay untouched.
_MIN_CONSTANT_DIGITS = 6


def _known_iban(value: str) -> bool:
    """Is this a published example IBAN rather than an account?"""
    v = "".join(c for c in value if c.isalnum()).upper()
    if not v:
        return False
    if v in _KNOWN_EXAMPLE_IBANS:
        return True
    # The BBAN is what identifies the account; a filler one is fabricated even
    # though the mod-97 check digits over it are real.
    return _is_filler(_digits(v[4:]))


def _known_wallet(value: str) -> bool:
    """Is this one of the addresses every Bitcoin tutorial quotes?"""
    return value.strip().upper() in _KNOWN_EXAMPLE_WALLETS


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
    local = value[:at].strip().lower()
    domain = value[at + 1:].strip().strip(".").lower()
    if not domain:
        return False

    # A Go module version path -- `northwind-logistics.co/toolkit@v1.23.4` -- parses
    # as local-part-at-domain and presidio reports it at 1.0. Two structural tells,
    # neither of which any real address has: a `/` in the local part, and a
    # top-level label that does not begin with a letter (`4`, from `@v1.23.4`).
    # `[0].isalpha()` rather than `.isalpha()` so IDN punycode (`xn--p1ai`) lives.
    if "/" in local:
        return True
    tld = domain.rsplit(".", 1)[-1]
    if not tld or not tld[0].isalpha():
        return True

    if local in _MACHINE_LOCAL_PARTS:
        return True
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
