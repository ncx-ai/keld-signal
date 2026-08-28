package review

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// checkProdRegister enforces "indistinguishable from a genuine item" for the production shape.
//
// r1's checkRegister ends with a sentence-boundary test, and that test is WRONG here: a production
// beat's entries are bullets and the corpus's own entries end without terminal punctuation, so
// requiring a full stop would reject every honest mutation and quietly steer the calibration set
// towards the few entries that happen to end in one.
//
// What replaces it is the shape the document actually renders, checked instead of assumed:
//
//   - the same number of lines, because a statement that lost or gained a bullet is spotted for its
//     shape and not for its content;
//   - a first line that is still a subject (no "- " marker) and following lines that are all still
//     entries (every one marked);
//   - no empty line, and no line left ending on a dangling delimiter — the repository's rule that
//     nothing read as language is cut mid-clause applies to a planted item exactly as it applies to
//     a generated one, and a bullet trailing "and" or a comma is a defect of the packaging that
//     would be caught as one;
//   - the same length band as r1, so a plant cannot be picked out of a list by its size.
func checkProdRegister(id, before, after string) error {
	b, a := utf8.RuneCountInString(before), utf8.RuneCountInString(after)
	slack := int(float64(b) * maxRegisterDrift)
	if slack < minRegisterSlack {
		slack = minRegisterSlack
	}
	if d := a - b; d > slack || -d > slack {
		return fmt.Errorf("mutation %s: mutated statement is %d runes against %d, a drift of %d over the %d allowed — it would be spotted for its length", id, a, b, d, slack)
	}
	beforeLines := strings.Split(strings.TrimSpace(before), "\n")
	afterLines := strings.Split(strings.TrimSpace(after), "\n")
	if len(beforeLines) != len(afterLines) {
		return fmt.Errorf("mutation %s: mutated statement has %d lines against %d — a beat that gained or lost an entry is spotted for its shape", id, len(afterLines), len(beforeLines))
	}
	for i, line := range afterLines {
		line = strings.TrimSpace(line)
		if line == "" {
			return fmt.Errorf("mutation %s: line %d of the mutated statement is empty", id, i+1)
		}
		if i == 0 {
			if strings.HasPrefix(line, "- ") {
				return fmt.Errorf("mutation %s: the subject line is now marked as an entry", id)
			}
		} else if !strings.HasPrefix(line, "- ") {
			return fmt.Errorf("mutation %s: line %d is no longer an entry: %q", id, i+1, truncate(line, 60))
		}
		body := strings.TrimSpace(strings.TrimPrefix(line, "- "))
		if body == "" {
			return fmt.Errorf("mutation %s: entry %d of the mutated statement is empty", id, i)
		}
		// Punctuation is checked as a suffix; a dangling WORD is checked as the last WORD.
		// Checking a word with HasSuffix is the exact defect this branch has paid for eight
		// times: "…without breaking existing behavior" ends with the letters "or" and would be
		// rejected as trailing off on the conjunction.
		for _, p := range []string{",", ";", ":", "-", "—", "…", "("} {
			if strings.HasSuffix(body, p) {
				return fmt.Errorf("mutation %s: line %d ends mid-clause on %q: %q", id, i+1, p, tail(body, 40))
			}
		}
		if fields := strings.Fields(body); len(fields) > 0 {
			last := strings.ToLower(strings.Trim(fields[len(fields)-1], ".,;:'\"’”"))
			for _, w := range []string{"and", "or", "but", "with", "the", "a", "an", "of", "to", "for", "in", "on"} {
				if last == w {
					return fmt.Errorf("mutation %s: line %d ends mid-clause on %q: %q", id, i+1, w, tail(body, 40))
				}
			}
		}
	}
	return nil
}

// ProdMutations is the calibration set for the production round: ONE planted defect per class, cut
// from five real beats in five different sessions, one of them from the hand-authored pair.
//
// One per class is a real weakening against r1's two, and it is named here rather than discovered
// in the score: **with one item per class, a class that comes back uncaught cannot be told apart
// from one reviewer having an off item.** r1's own comment says two is the minimum that can make
// that distinction. The round is sized at 24 packets against r1's 46, and the trade taken is
// breadth of genuine material — sixteen beats over fourteen sessions rather than thirty over three
// — because the number this round exists to move is a dimension failure count on GENUINE items, and
// that number's denominator is the genuine sample. The scorer prints the limitation beside the
// calibration table.
//
// Every property claimed here is verified by ApplyProd against the item's real evidence, so a wrong
// entry fails emission instead of silently becoming an item no reviewer could ever get right. In
// particular the Absent tokens are checked against that beat's own window AND record.
var ProdMutations = []Mutation{
	{
		ID: "p01", Class: FabricatedIdentifier,
		Session: "2927f65b-2e67-4293-90e2-ee4a02429e93.jsonl", Ordinal: 3,
		Original:    "a4_compositional.go",
		Replacement: "a7_sourcemap.go",
		Absent:      []string{"a7_sourcemap"},
		Note: "The entry lists the files the rename touched, and a4_compositional.go is one of them. " +
			"The substitute follows the same aN_-prefixed naming family, so it is wrong without looking wrong " +
			"in a list of five real filenames.",
	},
	{
		ID: "p02", Class: InventedBlocker,
		Session: "finance-close", Ordinal: 3,
		Original:    "a payroll journal was validated and a receivable of 1,840.00 was noted for a leaver's overpayment",
		Replacement: "a payroll journal was validated but the leaver's overpayment of 1,840.00 is disputed and cannot be recorded until it is agreed",
		Absent:      []string{"disputed"},
		Note: "Nothing in the window is disputed or waiting on anybody: the user says the overpayment is being " +
			"recovered next month and asks for it to be left as a receivable and noted. An overpayment in dispute " +
			"is an ordinary accounting obstacle, which is what makes it credible, and it is planted on the " +
			"hand-authored session because a reviewer who only reads code is the failure mode the audience " +
			"requirement cares about.",
	},
	{
		ID: "p03", Class: UnobservableCompletion,
		Session: "0ac739ad-90b9-4d65-87fe-372d8ab44290.jsonl", Ordinal: 3,
		Original:    "The second run is dispatched to exercise notarization and staple, with the .p8 secret now available at run start",
		Replacement: "The signing work is now essentially complete, with only the notarization verdict left before release",
		Note: "Trades an observed dispatch for a claim about the job as a whole. The window is one slice of the " +
			"session and cannot show how much of the signing work remains — and the design under review has no " +
			"status field at all, so a completion claim can only arrive as a fabricated event. It introduces no " +
			"new specific, so it can only be caught as a judgement.",
	},
	{
		ID: "p04", Class: SubjectDrift,
		Session: "1a3aa6d2-609f-4f8f-87a0-91976253bb2c.jsonl", Ordinal: 5,
		Original:    "re-theming the app to use warm-paper canvas and forest accents",
		Replacement: "re-theming the Meridian close dashboard to use warm-paper canvas and forest accents",
		Absent:      []string{"Meridian"},
		DrawnFrom:   "finance-close",
		Note: "The theming work is real in this window; the thing being themed belongs to the accounting session. " +
			"Cross-domain drift, which is the drift a domain-blind reviewer misses.",
	},
	{
		ID: "p05", Class: SourcelessSpecificity,
		Session: "43492104-1861-4682-9890-506bd7f41e67.jsonl", Ordinal: 5,
		Original:    "resting at 2,547 MB and fitting within the budget",
		Replacement: "resting at 2,547 MB on the Ardent bench host and fitting within the budget",
		Absent:      []string{"Ardent"},
		Note: "A proper noun with no source at all, dropped into an entry dense with real measured numbers. " +
			"Named rather than numeric, because a reviewer scanning an entry full of figures for an unsupported " +
			"digit will not necessarily scan it for an unsupported name.",
	},
}

// ProdCleanDuplicates names the genuine items emitted a SECOND time under a fresh packet id,
// byte-identical.
//
// Without them the round measures sensitivity and reports it as accuracy: a reviewer who flags
// something in every item catches every planted defect and looks perfect. r1 ran six against ten
// plants and returned 0 of 6 self-inconsistency; three against five plants holds the same ratio.
// They span both populations and the sample's length range, so a false alarm cannot be blamed on
// the controls being uniform.
//
// Each must be in the genuine sample, because a duplicate's whole point is that its twin is in the
// round under another id. Checked at emission.
var ProdCleanDuplicates = []struct {
	Session string
	Ordinal int
}{
	{"129e9a80-12a1-478f-9d47-cd68c47b8739.jsonl", 4}, // 702 runes, the longest in the sample
	{"bf277ad6-9e18-4b58-82a0-dedb068ab5d8.jsonl", 3}, // 383 runes, three entries, the shortest real
	{"finance-close", 1},                              // 435 runes, hand-authored, dense with real amounts
}
