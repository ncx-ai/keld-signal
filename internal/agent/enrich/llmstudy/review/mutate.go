package review

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MutationClass is the kind of defect a planted item carries. One per item: an item carrying
// two defects cannot tell you which one the reviewer saw.
//
// The five classes are the judgement-class heuristics' subject matter, restated as things
// that can be planted. T2/T13 reached for the fabricated identifier and the sourceless
// specific; T7 for the invented blocker; T3, T9 and "nearly done" phrasings for the
// unobservable completion; T12, ChangedSubject and SubjectShifted for subject drift.
type MutationClass string

const (
	FabricatedIdentifier   MutationClass = "fabricated_identifier"
	InventedBlocker        MutationClass = "invented_blocker"
	UnobservableCompletion MutationClass = "unobservable_completion"
	SubjectDrift           MutationClass = "subject_drift"
	SourcelessSpecificity  MutationClass = "sourceless_specificity"
)

// MutationClasses is every class, in report order. A class missing from a round's calibration
// is a hole in the calibration, so the scorer iterates this rather than whatever the answer
// key happens to contain.
var MutationClasses = []MutationClass{
	FabricatedIdentifier, InventedBlocker, UnobservableCompletion, SubjectDrift, SourcelessSpecificity,
}

// Mutation is one planted defect, expressed as a rewrite of a REAL output.
//
// Planting by mutation rather than by authoring a defective statement from scratch is the
// whole design of the calibration set: a synthetic statement reads as synthetic, and a
// reviewer that catches it catches the register, not the defect. So Original must be a span
// the genuine output actually contains, and the result is length- and register-bounded
// against it (see Apply).
type Mutation struct {
	ID      string
	Class   MutationClass
	Session string // source session title, as the document titles it
	Ordinal int    // source beat number within that session

	Original    string // the exact span replaced; must occur EXACTLY ONCE in the output
	Replacement string

	// Absent are the tokens this mutation introduces that the item's own evidence does not
	// support. They are VERIFIED absent from the window and the record, case-insensitively,
	// rather than asserted: a "fabricated" identifier that turns out to be in the window is
	// not a planted defect, it is a clean item mislabelled in the answer key, and it would
	// score as a reviewer failure forever.
	Absent []string

	// DrawnFrom is required for SubjectDrift and names the session whose evidence DOES carry
	// the injected subject. Verified too — that is what separates drift ("the right kind of
	// thing, the wrong work") from a sourceless specific nobody has ever mentioned.
	DrawnFrom string

	// Note says what the defect is, in the answer key's words. Never rendered into a packet.
	Note string
}

// Planted is a mutated item with everything the answer key and the scorer need.
type Planted struct {
	Item      Item // the mutated statement, with the original evidence untouched
	Mutation  Mutation
	Source    Item
	SpanStart int // rune offset of the replacement within the mutated output
	SpanEnd   int
	// Signature is the vocabulary the mutation introduced: the words present in the
	// replacement and absent from the span it replaced. The scorer uses it to decide whether
	// a reviewer LOCATED the defect rather than merely disliking the item, so it must be
	// non-empty (Apply refuses a mutation that introduces no new vocabulary at all).
	Signature []string
}

// blockerWords are how an invented obstacle has to read to be one. Checked, so a mutation
// filed under InventedBlocker that forgot to introduce an obstacle fails at emission rather
// than counting as a miss the reviewer never had a chance at.
var blockerWords = []string{
	"blocked", "blocking", "blocker", "cannot", "can't", "until", "waiting", "stalled",
	"held up", "holding up", "stuck", "unable", "pending confirmation", "obstacle",
}

// completionWords are the phrasings BeatPrompt explicitly forbids ("nearly complete",
// "almost done", "only X pending", "all that remains is") plus the near neighbours of that
// instruction. An UnobservableCompletion mutation must introduce one.
var completionWords = []string{
	"nearly", "almost", "only ", "remains", "left to", "left before", "finished",
	"complete", "done", "wrapping up", "final step", "last step", "ready to close",
}

// Apply plants one mutation, verifying every property its class claims.
//
// This function is the reason the calibration set is worth anything, so it fails loudly
// rather than emitting a plausible-looking item. The checks:
//
//  1. the source item exists and Original occurs in its output EXACTLY once (a span that
//     occurs twice makes "the exact span mutated" a lie, and the scorer's located-the-defect
//     test keys on it);
//  2. every Absent token really is absent from the window AND the record, case-insensitively;
//  3. SubjectDrift's tokens really are present in the session it claims to have drawn from;
//  4. the class's own vocabulary is present in the replacement (blocker / completion);
//  5. the mutation introduces new vocabulary at all, so a reviewer can quote it;
//  6. the result stays within a length band of the genuine output and still ends at a
//     sentence boundary — a planted item that is visibly longer, or that trails off
//     mid-clause, is caught for its shape rather than its content.
func Apply(c Corpus, m Mutation) (Planted, error) { return applyMutation(c, m, checkRegister) }

// ApplyProd plants a mutation into a PRODUCTION beat: the same verifications, with the register
// rule that fits a subject line plus bullets rather than prose (see checkProdRegister). Everything
// else about a plant — one defect per item, cut from a real output, absence verified against the
// item's own evidence — is identical, because the calibration set is only worth anything if the two
// rounds plant the same way.
func ApplyProd(c Corpus, m Mutation) (Planted, error) { return applyMutation(c, m, checkProdRegister) }

// register is the shape check a mutated statement must pass. It differs between the two output
// shapes and nothing else does.
type register func(id, before, after string) error

func applyMutation(c Corpus, m Mutation, reg register) (Planted, error) {
	src, err := c.Find(m.Session, m.Ordinal)
	if err != nil {
		return Planted{}, fmt.Errorf("mutation %s: %w", m.ID, err)
	}
	if m.Class == "" || m.Note == "" {
		return Planted{}, fmt.Errorf("mutation %s: class and note are required", m.ID)
	}
	if !classKnown(m.Class) {
		return Planted{}, fmt.Errorf("mutation %s: unknown class %q", m.ID, m.Class)
	}
	if m.Original == "" || m.Replacement == "" || m.Original == m.Replacement {
		return Planted{}, fmt.Errorf("mutation %s: original and replacement must differ and be non-empty", m.ID)
	}
	if n := strings.Count(src.Output, m.Original); n != 1 {
		return Planted{}, fmt.Errorf("mutation %s: original span occurs %d times in the source output, want exactly 1: %q", m.ID, n, m.Original)
	}

	evidence := strings.ToLower(src.Window + "\n" + src.Record)
	switch m.Class {
	case FabricatedIdentifier, SourcelessSpecificity, SubjectDrift:
		if len(m.Absent) == 0 {
			return Planted{}, fmt.Errorf("mutation %s: class %s is DEFINED by absence, so it must name the tokens that are absent", m.ID, m.Class)
		}
	}
	for _, tok := range m.Absent {
		if tok == "" {
			return Planted{}, fmt.Errorf("mutation %s: empty absent token", m.ID)
		}
		if !strings.Contains(strings.ToLower(m.Replacement), strings.ToLower(tok)) {
			return Planted{}, fmt.Errorf("mutation %s: absent token %q is not in the replacement, so the mutation does not introduce it", m.ID, tok)
		}
		if strings.Contains(evidence, strings.ToLower(tok)) {
			return Planted{}, fmt.Errorf("mutation %s: token %q IS in the item's window or record, so this is not an unsupported claim", m.ID, tok)
		}
	}
	if m.Class == SubjectDrift {
		if m.DrawnFrom == "" {
			return Planted{}, fmt.Errorf("mutation %s: subject drift must name the session it drew the wrong subject from", m.ID)
		}
		if m.DrawnFrom == m.Session {
			return Planted{}, fmt.Errorf("mutation %s: subject drift must draw from a DIFFERENT session", m.ID)
		}
		other, err := sessionEvidence(c, m.DrawnFrom)
		if err != nil {
			return Planted{}, fmt.Errorf("mutation %s: %w", m.ID, err)
		}
		for _, tok := range m.Absent {
			if !strings.Contains(other, strings.ToLower(tok)) {
				return Planted{}, fmt.Errorf("mutation %s: %q appears nowhere in %q either, so it is a sourceless specific rather than a drifted subject", m.ID, tok, m.DrawnFrom)
			}
		}
	}
	if m.Class == InventedBlocker && !containsAny(m.Replacement, blockerWords) {
		return Planted{}, fmt.Errorf("mutation %s: an invented blocker must read as an obstacle (one of %v)", m.ID, blockerWords)
	}
	if m.Class == UnobservableCompletion && !containsAny(m.Replacement, completionWords) {
		return Planted{}, fmt.Errorf("mutation %s: an unobservable completion must assert progress or completion (one of %v)", m.ID, completionWords)
	}

	sig := signatureOf(src.Output, m.Replacement)
	if len(sig) == 0 {
		return Planted{}, fmt.Errorf("mutation %s: replacement introduces no new word a reviewer could quote", m.ID)
	}

	out := strings.Replace(src.Output, m.Original, m.Replacement, 1)
	if err := reg(m.ID, src.Output, out); err != nil {
		return Planted{}, err
	}

	start := utf8.RuneCountInString(src.Output[:strings.Index(src.Output, m.Original)])
	mut := src
	mut.Output = out
	return Planted{
		Item:      mut,
		Mutation:  m,
		Source:    src,
		SpanStart: start,
		SpanEnd:   start + utf8.RuneCountInString(m.Replacement),
		Signature: sig,
	}, nil
}

// maxRegisterDrift bounds how far a planted item may move from the genuine one it was cut
// from, as a fraction of the genuine length, with a floor for short statements. The corpus
// runs 87-503 runes, so 0.35 lets a 200-rune statement move 70 runes — enough to add a
// clause, not enough to change what the item looks like in a list.
const (
	maxRegisterDrift = 0.35
	minRegisterSlack = 60
)

// checkRegister enforces the length half of "indistinguishable from a genuine item", and the
// repository's delimiter rule on top: a statement that trails off mid-clause is a defect of
// the packaging, and it would be caught as one.
func checkRegister(id, before, after string) error {
	b, a := utf8.RuneCountInString(before), utf8.RuneCountInString(after)
	slack := int(float64(b) * maxRegisterDrift)
	if slack < minRegisterSlack {
		slack = minRegisterSlack
	}
	if d := a - b; d > slack || -d > slack {
		return fmt.Errorf("mutation %s: mutated output is %d runes against %d, a drift of %d over the %d allowed — it would be spotted for its length", id, a, b, d, slack)
	}
	trimmed := strings.TrimSpace(after)
	if trimmed == "" {
		return fmt.Errorf("mutation %s: mutated output is empty", id)
	}
	// Decoded as a rune, not a byte: the corpus is full of curly quotes and an en dash, and a
	// byte-wise last-character test on "…outputs.”" reads a continuation byte.
	r := []rune(trimmed)
	switch r[len(r)-1] {
	case '.', '!', '?', '"', '\'', ')', '’', '”':
	default:
		return fmt.Errorf("mutation %s: mutated output does not end at a sentence boundary: %q", id, tail(trimmed, 40))
	}
	return nil
}

func tail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}

func classKnown(c MutationClass) bool {
	for _, k := range MutationClasses {
		if k == c {
			return true
		}
	}
	return false
}

func containsAny(s string, words []string) bool {
	low := strings.ToLower(s)
	for _, w := range words {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}

func sessionEvidence(c Corpus, title string) (string, error) {
	var b strings.Builder
	found := false
	for _, s := range c.Sessions {
		if s.Title != title {
			continue
		}
		found = true
		for _, it := range s.Items {
			b.WriteString(strings.ToLower(it.Record))
			b.WriteByte('\n')
			b.WriteString(strings.ToLower(it.Window))
			b.WriteByte('\n')
			b.WriteString(strings.ToLower(it.Output))
			b.WriteByte('\n')
		}
	}
	if !found {
		return "", fmt.Errorf("no session titled %q to draw a subject from", title)
	}
	return b.String(), nil
}

// signatureOf is the vocabulary the replacement adds TO THE WHOLE STATEMENT, not merely to
// the span it replaced. Subtracting only the span let ordinary words from elsewhere in the
// same item into the signature ("store", in the sweep item, which the genuine text already
// used), and the scorer would then have credited a reviewer with locating the defect for
// quoting a word the defect did not introduce.
//
// Words of four runes or more only: "the", "is" and "a" are in every statement and would
// make every reviewer look like they had located every defect.
func signatureOf(genuineOutput, replacement string) []string {
	old := map[string]bool{}
	for _, w := range words(genuineOutput) {
		old[w] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, w := range words(replacement) {
		if old[w] || seen[w] || utf8.RuneCountInString(w) < 4 {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}

// words tokenises for the signature and for the scorer's quote matching. Interior ".", "_",
// "-", "/" and "," are kept because they are what makes an identifier or an amount one
// token — "fa-register.csv" and "412,880.15" must not shatter into fragments — while the same
// characters are trimmed at the edges, where they are only punctuation.
func words(s string) []string {
	raw := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.' && r != '_' && r != '-' && r != '/' && r != ','
	})
	out := make([]string, 0, len(raw))
	for _, w := range raw {
		if w = strings.Trim(w, "._-/,"); w != "" {
			out = append(out, w)
		}
	}
	return out
}
