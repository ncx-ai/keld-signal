package llmstudy

import (
	"regexp"
	"strings"
)

// identifierPat matches the token shapes worth verifying: dotted/hyphenated
// filenames and identifiers, and capitalised proper nouns. Deliberately narrow — a
// broad pattern would flag ordinary prose and drown the signal.
var identifierPat = regexp.MustCompile(`\b[A-Za-z0-9_]+(?:[.-][A-Za-z0-9_]+)+\b|\b[A-Z][a-zA-Z0-9]{2,}\b`)

// digestStopWords are capitalised words that merely begin sentences or name generic
// concepts. Without these the gate flags ordinary English as invented proper nouns.
var digestStopWords = map[string]bool{
	"The": true, "This": true, "That": true, "These": true, "Those": true,
	"There": true, "Then": true, "They": true, "Their": true, "It": true, "Its": true,
	"Also": true, "After": true, "Before": true, "Both": true, "Because": true,
	"Everything": true, "All": true, "Any": true, "Some": true, "None": true,
	"One": true, "Two": true, "Three": true, "Four": true, "Five": true,
	"Nothing": true, "Next": true, "Currently": true, "Still": true, "Work": true,
	"Completed": true, "Reconciled": true, "Added": true, "Fixed": true,
	"During": true, "While": true, "When": true, "With": true, "Without": true,
	"However": true, "Although": true, "Since": true, "Once": true, "Now": true,
	"User": true, "Session": true, "No": true, "Not": true, "Several": true,
}

// Identifiers extracts the candidate specifics from a digest's prose.
func Identifiers(d Digest) []string {
	var b strings.Builder
	for _, s := range []string{d.Done, d.Happened, d.Structure, d.Current, d.Why, d.Next} {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	for _, s := range append(append([]string{}, d.Insights...), d.Unresolved...) {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range identifierPat.FindAllString(b.String(), -1) {
		if digestStopWords[m] || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// UnverifiedIdentifiers returns the specifics that do not appear in the source.
//
// This is the same gate that caught 11 of 11 fabrications by Qwen3-0.6B in the
// extraction study, where the invented values were lifted from the prompt's own
// examples ("Slack connector" for a Notion prompt; "Northwind" for a Globex one).
//
// It bounds fabricated SPECIFICS only. A fabricated judgement — "the team decided to
// prioritise X" — contains no verifiable token and passes untouched, which is why the
// spec keeps a human review gate that cannot be automated away.
func UnverifiedIdentifiers(d Digest, source string) []string {
	_, dropped := VerifyTopics(Identifiers(d), source)
	return dropped
}

// frictionWords are the vocabulary a report uses when it admits difficulty.
var frictionWords = []string{
	"revert", "reverse", "reversed", "fail", "failed", "failing", "broke", "broken",
	"wrong", "incorrect", "retry", "retried", "again", "corrected", "correction",
	"disagree", "mismatch", "blocked", "stuck", "abandoned", "unresolved", "issue",
	"problem", "did not", "didn't", "unable", "reworked", "redo", "backtrack",
	"struggl", "difficult", "confusion", "misunderstood", "clarif",
}

// LooksRubberstamped reports whether a digest claims a clean run despite measured
// corrections.
//
// This is the metric the whole anti-rubberstamping design is judged by, and it is
// only possible because `corrected` is harvested ground truth rather than an opinion
// (base rate 6.9%, 94 positives over 1,357 turns). With no corrections there is
// nothing to under-report, so it stays silent rather than manufacturing a finding.
func LooksRubberstamped(d Digest, f DigestFacts) bool {
	if f.Corrections == 0 && f.CorrectedTurns == 0 {
		return false
	}
	hay := strings.ToLower(strings.Join(append([]string{
		d.Done, d.Happened, d.Structure, d.Current, d.Why, d.Next,
	}, append(d.Insights, d.Unresolved...)...), " "))
	for _, w := range frictionWords {
		if strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

// RetainedFacts counts how many of the given facts still appear after refinement.
// Used by the drift test: inject known facts, refine, measure survival.
func RetainedFacts(after Digest, facts []string) int {
	hay := strings.ToLower(strings.Join(append([]string{
		after.Done, after.Happened, after.Structure, after.Current, after.Why, after.Next,
	}, append(after.Insights, after.Unresolved...)...), " "))
	n := 0
	for _, f := range facts {
		if strings.Contains(hay, strings.ToLower(f)) {
			n++
		}
	}
	return n
}
