package piidetect

import "testing"

// ---------------------------------------------------------------------------
// All fixtures in this file are WHOLLY SYNTHETIC.
//
// Card numbers were constructed by taking an obviously-invented account body
// (ascending digit runs) under a real brand IIN and computing the Luhn check
// digit for it, so they are structurally valid without being anyone's card and
// without colliding with the published test-card set (which the well-known gate
// excludes — see wellknown_test.go).
//
// The SSN 321-54-9876 is the same idea: descending digits, so it reads as
// invented, while satisfying every SSA structural rule (area not 000/666/900+,
// group not 00, serial not 0000) and appearing on no published example list.
// ---------------------------------------------------------------------------
const (
	synthVisa     = "4539871234567895" // 16-digit, Luhn-valid, IIN 4
	synthAmex     = "374612345678908"  // 15-digit, Luhn-valid, IIN 37
	synthMC       = "5512345678901231" // 16-digit, Luhn-valid, IIN 55
	synthMC2      = "2221234567890128" // 16-digit, Luhn-valid, 2-series MC
	synthDiscover = "6011871234567897" // 16-digit, Luhn-valid, IIN 6011
	synthSSN      = "321-54-9876"
)

func labelsOf(text string) []string {
	var out []string
	for _, s := range Detect(text) {
		out = append(out, s.Label)
	}
	return out
}

func hasLabel(text, label string) bool {
	for _, l := range labelsOf(text) {
		if l == label {
			return true
		}
	}
	return false
}

func TestDetectsSyntheticCards(t *testing.T) {
	for _, tc := range []string{
		"charge the card " + synthVisa + " today",
		"amex " + synthAmex + " on file",
		"card: " + synthMC,
		"card: " + synthMC2,
		"card: " + synthDiscover,
		"card 4539 8712 3456 7895 grouped",
		"card 4539-8712-3456-7895 hyphenated",
	} {
		if !hasLabel(tc, "credit_card") {
			t.Errorf("expected credit_card in %q, got %v", tc, labelsOf(tc))
		}
	}
}

func TestCardSpanCoversTheNumber(t *testing.T) {
	text := "charge " + synthVisa + " now"
	spans := Detect(text)
	if len(spans) != 1 {
		t.Fatalf("spans = %+v, want exactly one", spans)
	}
	if got := text[spans[0].Start:spans[0].End]; got != synthVisa {
		t.Fatalf("span text = %q, want %q", got, synthVisa)
	}
}

func TestRejectsCardNearMisses(t *testing.T) {
	for _, tc := range []string{
		"failed luhn 4539871234567896",             // last digit changed
		"luhn-valid but no brand 9000123456789016", // IIN 9 is not an issuer
		"epoch millis 1700000000000 logged",
		"build 12345678901234567 sequence",
		"order 4539871234 short",               // 10 digits, too short for a card
		"sha a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6", // hex, not digits
		"uuid 550e8400-e29b-41d4-a716-446655440000",
		"version 1.2.3." + synthVisa, // dotted-numeric context, not a card
	} {
		if hasLabel(tc, "credit_card") {
			t.Errorf("near-miss %q wrongly matched credit_card: %+v", tc, Detect(tc))
		}
	}
}

func TestDetectsSyntheticSSN(t *testing.T) {
	text := "her ssn is " + synthSSN + " on the form"
	if !hasLabel(text, "ssn") {
		t.Fatalf("expected ssn in %q, got %v", text, labelsOf(text))
	}
}

func TestRejectsStructurallyInvalidSSNs(t *testing.T) {
	for _, tc := range []string{
		"000-12-3456", // area 000
		"666-12-3456", // area 666
		"900-12-3456", // area 900+
		"999-12-3456",
		"321-00-9876", // group 00
		"321-54-0000", // serial 0000
	} {
		if hasLabel("record "+tc+" filed", "ssn") {
			t.Errorf("invalid SSN %q wrongly matched", tc)
		}
	}
}

// A bare nine-digit run is deliberately NOT an SSN: order ids, account numbers
// and ticket ids share the shape exactly, and precision is the requirement.
func TestBareNineDigitRunIsNotAnSSN(t *testing.T) {
	for _, tc := range []string{
		"order 321549876 shipped",
		"ssn lookup for account 321549876",
		"employee id 321-549876",
		"part number PO-" + synthSSN + "-A", // hyphen-adjacent serial
	} {
		if hasLabel(tc, "ssn") {
			t.Errorf("%q wrongly matched ssn", tc)
		}
	}
}

func TestDetectsEmail(t *testing.T) {
	text := "ping dana.rivers@northwind-logistics.co.uk about it"
	spans := Detect(text)
	if len(spans) != 1 || spans[0].Label != "email" {
		t.Fatalf("spans = %+v, want one email", spans)
	}
	if got := text[spans[0].Start:spans[0].End]; got != "dana.rivers@northwind-logistics.co.uk" {
		t.Fatalf("span text = %q", got)
	}
}

func TestRejectsEmailNearMisses(t *testing.T) {
	for _, tc := range []string{
		"install lodash@4.17.21 please",
		"run user@host:/srv/app",
		"the @types/node package",
		"see foo@bar (no tld)",
		"a@b.c is too short a tld? no: 1-char tld",
	} {
		if hasLabel(tc, "email") {
			t.Errorf("near-miss %q wrongly matched email: %+v", tc, Detect(tc))
		}
	}
}

// Phone numbers are deliberately absent: a bare digit run is indistinguishable
// from a port, version, timestamp or id, and no pattern reaches the precision
// this facet needs. Recall for phone stays with the NER.
func TestPhoneIsNotDetected(t *testing.T) {
	for _, tc := range []string{
		"call +1 415 555 0142 later",
		"listen on 127.0.0.1:8080",
	} {
		if hasLabel(tc, "phone") {
			t.Errorf("%q produced a phone span; phone is intentionally unsupported", tc)
		}
	}
}

func TestDetectFindsBothClassesInOneText(t *testing.T) {
	text := "card " + synthVisa + " and ssn " + synthSSN
	got := map[string]bool{}
	for _, l := range labelsOf(text) {
		got[l] = true
	}
	if !got["credit_card"] || !got["ssn"] {
		t.Fatalf("labels = %v, want both credit_card and ssn", labelsOf(text))
	}
}

func TestSpansDoNotOverlap(t *testing.T) {
	text := "card " + synthVisa + " ssn " + synthSSN + " mail a.b@corp-example-x.net"
	spans := Detect(text)
	for i := range spans {
		for j := i + 1; j < len(spans); j++ {
			if spans[i].Start < spans[j].End && spans[j].Start < spans[i].End {
				t.Fatalf("overlapping spans %+v and %+v", spans[i], spans[j])
			}
		}
	}
}

func TestEmptyAndPlainTextYieldNothing(t *testing.T) {
	for _, tc := range []string{"", "refactor the retry loop in publish.go"} {
		if s := Detect(tc); len(s) != 0 {
			t.Errorf("Detect(%q) = %+v, want none", tc, s)
		}
	}
}
