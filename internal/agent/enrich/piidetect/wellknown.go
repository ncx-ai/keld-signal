package piidetect

import "strings"

// This file is the precision gate for piidetect, playing the same role
// creddetect/placeholder.go plays for credentials: a value that is structurally
// perfect but PUBLISHED as an example is not leaked data.
//
// It is the single thing this detector cannot ship without. Developer
// transcripts are saturated with these values — 4111 1111 1111 1111 is the
// canonical Visa test number and passes Luhn; 123-45-6789 is the textbook SSN;
// user@example.com is reserved by RFC 2606. Without the gate every engineer's
// transcript reports pci/phi continuously and the facet becomes noise, which is
// strictly worse than not having it at all.

// Published test card numbers, normalized to digits. Sources: the public
// sandbox documentation of the major processors and card networks. Listed here
// as documentation constants; none is an account.
var knownTestCards = map[string]bool{
	// Visa
	"4111111111111111": true, "4012888888881881": true, "4222222222222": true,
	"4242424242424242": true, "4000056655665556": true, "4000000000000002": true,
	"4917610000000000": true, "4001919257537193": true,
	// Mastercard
	"5555555555554444": true, "5105105105105100": true, "5200828282828210": true,
	"5555555555554477": true, "2223003122003222": true, "2223000048410010": true,
	// American Express
	"378282246310005": true, "371449635398431": true, "378734493671000": true,
	"343434343434343": true,
	// Discover
	"6011111111111117": true, "6011000990139424": true, "6011000991300009": true,
	"6011981111111113": true,
	// JCB
	"3530111333300000": true, "3566002020360505": true, "3566111111111113": true,
	// Diners Club
	"30569309025904": true, "38520000023237": true, "36227206271667": true,
	"3056930009020004": true,
	// UnionPay / Maestro sandbox numbers seen in the same docs
	"6200000000000005": true, "6759649826438453": true,
}

// Published example SSNs. 123-45-6789 is the textbook placeholder; 078-05-1120
// is the Woolworth wallet-card number issued to hundreds of thousands of people
// who copied it; 219-09-9999 appeared in a 1938 advertisement; 987-65-4320
// through 987-65-4329 are the block the SSA reserves for advertising.
var knownExampleSSNs = map[string]bool{
	"123456789": true,
	"078051120": true,
	"219099999": true,
}

// Domains reserved for documentation and testing by RFC 2606 and RFC 6761, plus
// the conventional .localdomain. An address at one of these is never a person's.
var reservedDomains = []string{
	"example", "test", "invalid", "localhost", "localdomain",
	"example.com", "example.net", "example.org",
}

// IsWellKnown reports whether value is a published test/example value and
// therefore must not be reported as leaked data.
//
// The label steers which check applies, but it is not trusted to be right: an
// NER reads 123-45-6789 as a phone number about as often as an SSN, and a
// documentation constant does not stop being one because it arrived under a
// different label. Values of nine digits or more are therefore re-checked
// against the SSN and card sets whatever the label says, while values with no
// digits — names, addresses — are never gated at all. It is a gate, not a
// classifier: when in doubt it returns false and the value is reported.
func IsWellKnown(label, value string) bool {
	switch label {
	case "credit_card":
		return knownCard(digitsOnly(value))
	case "ssn":
		return knownSSN(digitsOnly(value))
	case "email":
		return reservedEmail(value)
	}
	d := digitsOnly(value)
	if len(d) < 9 { // below SSN length: too short to be one of these constants
		return false
	}
	return knownSSN(d) || knownCard(d)
}

func knownCard(d string) bool {
	if d == "" {
		return false
	}
	if knownTestCards[d] {
		return true
	}
	// Filler shape: a number built from at most two distinct digits (4444...48,
	// 5454...54) is a fabricated example even when Luhn-valid under a real IIN.
	// It generalises past any list, and the odds of a real account matching are
	// negligible next to the cost of a false "pci".
	return distinctDigits(d) <= 2
}

func knownSSN(d string) bool {
	if len(d) != 9 {
		return false
	}
	if knownExampleSSNs[d] {
		return true
	}
	if strings.HasPrefix(d, "98765432") { // SSA advertising block 987-65-432x
		return true
	}
	return distinctDigits(d) == 1 // 111-11-1111 and friends
}

func reservedEmail(value string) bool {
	at := strings.LastIndex(value, "@")
	if at < 0 {
		return false
	}
	domain := strings.ToLower(strings.Trim(strings.TrimSpace(value[at+1:]), "."))
	if domain == "" {
		return false
	}
	for _, r := range reservedDomains {
		if domain == r || strings.HasSuffix(domain, "."+r) {
			return true
		}
	}
	return false
}

func distinctDigits(d string) int {
	var seen [10]bool
	n := 0
	for i := 0; i < len(d); i++ {
		if v := d[i] - '0'; v < 10 && !seen[v] {
			seen[v] = true
			n++
		}
	}
	return n
}
