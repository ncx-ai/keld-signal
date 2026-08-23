package piidetect

import "testing"

// The published test/example values below are quoted here BECAUSE they are
// public documentation constants, not because they identify anyone: every one
// of them appears verbatim in a payment processor's docs, an RFC, or a
// government notice. They are exactly the values a developer transcript is full
// of, and each must be excluded or this facet reports pci/phi continuously.
func TestWellKnownTestCardsAreExcluded(t *testing.T) {
	for _, card := range []string{
		// Visa
		"4111111111111111", "4012888888881881", "4222222222222", "4242424242424242",
		"4000056655665556", "4111 1111 1111 1111", "4111-1111-1111-1111",
		// Mastercard
		"5555555555554444", "5105105105105100", "5200828282828210", "2223003122003222",
		// American Express
		"378282246310005", "371449635398431", "378734493671000",
		// Discover
		"6011111111111117", "6011000990139424", "6011000991300009",
		// JCB
		"3530111333300000", "3566002020360505",
		// Diners Club
		"30569309025904", "38520000023237", "36227206271667",
	} {
		if !IsWellKnown("credit_card", card) {
			t.Errorf("%q is a published test card and must be excluded", card)
		}
		if hasLabel("charge "+card+" now", "credit_card") {
			t.Errorf("Detect surfaced published test card %q", card)
		}
	}
}

func TestWellKnownExampleSSNsAreExcluded(t *testing.T) {
	for _, ssn := range []string{
		"123-45-6789", // the textbook example
		"078-05-1120", // the Woolworth wallet-card number
		"219-09-9999", // the 1938 advertising number
		"987-65-4320", // SSA's reserved advertising block 987-65-4320..4329
		"987-65-4329",
		"111-11-1111", // single repeated digit
		"222-22-2222",
	} {
		if !IsWellKnown("ssn", ssn) {
			t.Errorf("%q is a published example SSN and must be excluded", ssn)
		}
		if hasLabel("ssn "+ssn+" on file", "ssn") {
			t.Errorf("Detect surfaced published example SSN %q", ssn)
		}
	}
}

// RFC 2606 (example.com/.net/.org, .test, .example, .invalid, .localhost) and
// RFC 6761 (localhost, invalid) reserve these names precisely so documentation
// can use them. An address at one of them is never a person's address.
func TestReservedDomainEmailsAreExcluded(t *testing.T) {
	for _, addr := range []string{
		"user@example.com", "jane@example.org", "bob@example.net",
		"a@mail.example.com", "dev@myapp.test", "x@thing.invalid",
		"root@localhost.localdomain", "someone@example",
		"USER@EXAMPLE.COM",
	} {
		if !IsWellKnown("email", addr) {
			t.Errorf("%q is at a reserved/documentation domain and must be excluded", addr)
		}
		if hasLabel("mail "+addr+" now", "email") {
			t.Errorf("Detect surfaced reserved-domain address %q", addr)
		}
	}
}

// The gate must not swallow real values: it is a precision gate, and a false
// exclusion is recall loss on genuinely leaked data.
func TestGateDoesNotExcludeSyntheticRealShapedValues(t *testing.T) {
	for _, tc := range []struct{ label, value string }{
		{"credit_card", synthVisa},
		{"credit_card", synthAmex},
		{"credit_card", synthMC},
		{"ssn", synthSSN},
		{"email", "dana.rivers@northwind-logistics.co.uk"},
		{"email", "ops@internal-tools.io"},
	} {
		if IsWellKnown(tc.label, tc.value) {
			t.Errorf("IsWellKnown(%q, %q) = true; this value is not a published example", tc.label, tc.value)
		}
	}
}

// A number whose body is one repeated digit or one repeated short block is a
// filler value even when it is Luhn-valid and carries a real IIN — the shape
// covers the long tail of test cards no explicit list can enumerate.
func TestLowEntropyCardBodiesAreExcluded(t *testing.T) {
	for _, card := range []string{
		"4444444444444448", // one distinct body digit
		"5454545454545454", // repeated two-digit block
	} {
		if !IsWellKnown("credit_card", card) {
			t.Errorf("%q is a filler-shaped number and must be excluded", card)
		}
	}
}

func TestUnknownLabelIsNeverGated(t *testing.T) {
	if IsWellKnown("person", "Dana Rivers") {
		t.Fatal("IsWellKnown must return false for labels it does not govern")
	}
}

// The NER does not always agree with us about what a value IS: GLiNER2 reads
// 123-45-6789 as a phone number about as often as an SSN. A documentation
// constant does not stop being one because it arrived under a different label,
// so digit-shaped values are re-checked against the SSN and card sets whatever
// the label says — while label-free text (names, addresses) is never gated.
func TestDigitConstantsAreGatedUnderAnyLabel(t *testing.T) {
	for _, tc := range []struct{ label, value string }{
		{"phone", "123-45-6789"},
		{"phone", "4111 1111 1111 1111"},
		{"person", "078-05-1120"},
	} {
		if !IsWellKnown(tc.label, tc.value) {
			t.Errorf("IsWellKnown(%q, %q) = false; it is a published constant whatever the label", tc.label, tc.value)
		}
	}
	for _, tc := range []struct{ label, value string }{
		{"person", "Dana Rivers"},
		{"address", "44 Kestrel Way, Unit 7"},
		{"phone", "+1 415 555 0142"},
		{"phone", "12345678"}, // shorter than an SSN: never gated here
	} {
		if IsWellKnown(tc.label, tc.value) {
			t.Errorf("IsWellKnown(%q, %q) = true; the gate must not swallow ordinary values", tc.label, tc.value)
		}
	}
}
