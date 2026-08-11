package llmstudy

import (
	"regexp"
	"strings"
)

// identifierPat matches the token shapes worth verifying: dotted/hyphenated
// filenames and identifiers, snake_case/SCREAMING_SNAKE_CASE names, and capitalised
// proper nouns. Deliberately narrow — a broad pattern would flag ordinary prose and
// drown the signal.
//
// The underscore alternative was added after INSTALL_SCRIPT_URL, enrichment_pass,
// tool_events, session_record, MAX_LEN and KELD_WATCH were all found to return NO
// match: `_` is a regex word character, so it blocks the `\b` the capitalised
// alternative needs right after "INSTALL", and the dotted/hyphenated alternative
// only fires on `.`/`-`, never `_`. strongIdentifier already special-cases an
// underscore (its own doc names "an ALL_CAPS constant" as a target) — the gate in
// front of it was the bug, silently discarding every candidate of that shape before
// strongIdentifier ever saw it. Before this branch's retain-list replaced
// CarryForward, that whole naming convention still reached a refinement prompt
// verbatim, because CarryForward embedded the sentence it sat in regardless of
// whether Identifiers could parse it out; the retain-list is now the ONLY channel,
// so the gap had to be closed here rather than staying latent.
var identifierPat = regexp.MustCompile(`\b[A-Za-z0-9_]+(?:[.-][A-Za-z0-9_]+)+\b|\b[A-Za-z0-9]+(?:_[A-Za-z0-9]+)+\b|\b[A-Z][a-zA-Z0-9]{2,}\b`)

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

// strongIdentifier reports whether a token is unambiguously an identifier regardless
// of where it appears: a path, a file with an extension, anything containing a digit,
// internal capitalisation, or an ALL_CAPS constant.
// escapeRemnant matches a token carrying a JSON \uXXXX escape the model emitted
// double-escaped, e.g. "u003cToast.Provider" for "<Toast.Provider". It reached the
// identifier gate as an unverifiable specific — a defect in the extractor, not a
// fabrication by the model.
var escapeRemnant = regexp.MustCompile(`^u[0-9a-fA-F]{4}`)

func strongIdentifier(tok string) bool {
	if escapeRemnant.MatchString(tok) {
		return false
	}
	if strings.ContainsAny(tok, "/_") {
		return true
	}
	for _, r := range tok {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	// Internal capitalisation: PostgreSQL, KpiCard, resolveInstallers.
	for _, r := range tok[1:] {
		if r >= 'A' && r <= 'Z' {
			return true
		}
	}
	// A dotted token counts only with a plausible file extension, so "e.g" does not.
	if i := strings.LastIndex(tok, "."); i > 0 && i < len(tok)-1 {
		ext := tok[i+1:]
		if len(ext) >= 2 && len(ext) <= 5 && !strings.Contains(ext, ".") {
			return true
		}
	}
	return false
}

// Identifiers extracts the candidate specifics from a digest's prose.
//
// Prose needs a different rule from extraction. In extraction the model emits bare
// spans that should be verbatim, so any non-matching span is suspect. In prose,
// ordinary English produces capitalised sentence openings and hyphenated compounds
// that will never appear verbatim in a transcript — measured directly, an earlier
// version of this function drove T2 to 20.7% while flagging almost nothing real:
// of 71 flags, the top tokens were "Key", "Initial", "four-screen", "well-documented"
// and "e.g", with at most one genuine candidate.
//
// So the rule is POSITION-AWARE, which is how English works: a capitalised word at a
// sentence boundary is presumed to be English; mid-sentence it is presumed a proper
// noun and worth verifying. Strong identifiers (paths, extensions, digits, internal
// caps) are always checked wherever they appear. This keeps a fabricated
// "cross-checked against Globex" while ignoring "Key findings...".
func Identifiers(d Digest) []string {
	var b strings.Builder
	for _, s := range ProseFields(d) {
		b.WriteString(s)
		b.WriteString(". ")
	}
	for _, s := range append(append([]string{}, d.Insights...), d.Unresolved...) {
		b.WriteString(s)
		b.WriteString(". ")
	}
	text := b.String()

	seen := map[string]bool{}
	var out []string
	for _, m := range identifierPat.FindAllStringIndex(text, -1) {
		tok := text[m[0]:m[1]]
		if seen[tok] || digestStopWords[tok] {
			continue
		}
		if !strongIdentifier(tok) {
			// A weak token is a proper-noun CANDIDATE only if it is capitalised,
			// appears mid-sentence, and is not an ordinary hyphenated compound.
			// A lowercase weak token ("e.g") is just a word.
			initial := rune(tok[0])
			if initial < 'A' || initial > 'Z' {
				continue
			}
			if sentenceInitial(text, m[0]) || strings.Contains(tok, "-") {
				continue
			}
		}
		seen[tok] = true
		out = append(out, tok)
	}
	return out
}

// sentenceInitial reports whether the token at i opens a sentence, so its capital
// carries no information about proper-noun-hood.
func sentenceInitial(text string, i int) bool {
	for j := i - 1; j >= 0; j-- {
		switch text[j] {
		case ' ', '\t', '\n', '"', '\'', '(', '*', '`':
			continue
		case '.', '!', '?', ':', ';', '-':
			return true
		default:
			return false
		}
	}
	return true
}

// LeakedPromptWords returns content words that appear in the digest and in the digest
// INSTRUCTIONS but nowhere in the session — a direct check for the model borrowing
// from its own prompt rather than from the conversation.
//
// Generic on purpose. An earlier version hardcoded two words from a since-removed
// prompt example and misfired: a session containing "resolve" and
// "internal/agent/resolve/claude.go" produced the legitimate nominalisation
// "resolver", which exact-substring matching flagged. A manual list also cannot catch
// the NEXT example someone adds. Stems are compared crudely (a five-character shared
// prefix in both directions) so morphology no longer misfires.
// LeakedPromptWords reports instruction text the digest borrowed rather than took from
// the conversation. It matches PHRASES, not words.
//
// The single-word version of this check was wrong, and wrong in a way already paid for
// once: it flagged any word >=5 chars that occurred in digestSections+digestRules but
// not in the source. Those instructions are ordinary English prose, so "changed",
// "structure", "report" and "specifics" were all instruction vocabulary AND ordinary
// description, and the check reported ~100 leaks per sweep that were merely a digest
// describing work in English. The identifier check had the identical defect and
// measured 22.6% before position-awareness cut it to under 1%.
//
// The failure actually observed was a WORKED EXAMPLE copied verbatim into a report about
// an unrelated session. That is a run of consecutive words, so a run is what is matched:
// leakPhraseLen words in a row shared with the instructions and absent from the source.
// Ordinary English coincides for a word or two, effectively never for five.
const leakPhraseLen = 5

// allPhrases returns every leakPhraseLen-word run in ws.
func allPhrases(ws []string) []string {
	var out []string
	for i := 0; i+leakPhraseLen <= len(ws); i++ {
		out = append(out, strings.Join(ws[i:i+leakPhraseLen], " "))
	}
	return out
}

func LeakedPromptWords(d Digest, source string) []string {
	instr := wordsOf(strings.ToLower(digestSections + digestRules))
	if len(instr) < leakPhraseLen {
		return nil
	}
	instrPhrases := make(map[string]bool, len(instr))
	for i := 0; i+leakPhraseLen <= len(instr); i++ {
		instrPhrases[strings.Join(instr[i:i+leakPhraseLen], " ")] = true
	}
	// UnresolvedSentinel is prescribed text the model is TOLD to emit verbatim when
	// nothing is open, so reproducing it is compliance, not borrowing. Left in, it was
	// the entire leak signal: 39 of 39 flagged phrases across a 54-digest sweep were its
	// three overlapping 5-grams, and genuine borrowing was zero.
	for _, sw := range allPhrases(wordsOf(strings.ToLower(UnresolvedSentinel))) {
		delete(instrPhrases, sw)
	}

	srcWords := wordsOf(strings.ToLower(source))
	srcPhrases := make(map[string]bool, len(srcWords))
	for i := 0; i+leakPhraseLen <= len(srcWords); i++ {
		srcPhrases[strings.Join(srcWords[i:i+leakPhraseLen], " ")] = true
	}

	// This is a THIRD copy of proseHay's join (ProseFields + Insights + Unresolved) —
	// RetainedFacts/proseHay and the eval harness's lostFacts diagnostic are the other
	// two — not refactored to call it (task-7b brief item, "also, cheaply"; not fixed,
	// flagged). All current copies key off ProseFields, so a new STRING field on Digest
	// is picked up everywhere automatically; the residual risk is a future non-string
	// prose-like field (as Insights/Unresolved themselves are) being added to only one
	// of the four copies.
	body := wordsOf(strings.ToLower(strings.Join(
		append(ProseFields(d), append(d.Insights, d.Unresolved...)...), " ")))

	seen := map[string]bool{}
	var out []string
	for i := 0; i+leakPhraseLen <= len(body); i++ {
		ph := strings.Join(body[i:i+leakPhraseLen], " ")
		if seen[ph] || !instrPhrases[ph] || srcPhrases[ph] {
			continue
		}
		seen[ph] = true
		out = append(out, ph)
	}
	return out
}

// wordsOf splits text into lowercase alphanumeric words.
func wordsOf(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
}

// containsWord reports whether hay uses w as a whole word.
func containsWord(hay, w string) bool {
	for _, x := range wordsOf(hay) {
		if x == w {
			return true
		}
	}
	return false
}

// coveredByStem reports whether the source contains w or a morphological relative, so
// "resolve" in the source covers "resolver" in the digest.
func coveredByStem(src, w string) bool {
	for _, x := range wordsOf(src) {
		if x == w {
			return true
		}
		if len(x) >= 5 && len(w) >= 5 && strings.HasPrefix(x, w[:5]) && strings.HasPrefix(w, x[:5]) {
			return true
		}
	}
	return false
}

// digestCommonWord filters vocabulary any report shares with any instruction —
// flagging these would drown the signal.
func digestCommonWord(w string) bool {
	switch w {
	case "conversation", "report", "section", "sections", "entry", "entries",
		"outcomes", "concrete", "described", "describe", "nothing", "stopping",
		"point", "reached", "progress", "further", "understood", "invent",
		"invented", "supports", "actually", "already", "should", "anything",
		"everything", "specifics", "systems", "people", "amounts", "blocked",
		"abandoned", "there", "these", "those", "their", "which", "where",
		"while", "about", "above", "below", "other", "another", "changed",
		"ревис", "revise", "current", "state", "context", "session", "measured":
		return true
	}
	return false
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
// UnverifiedIdentifiers reports named specifics the source does not support.
//
// It deliberately does NOT reuse VerifyTopics' exact-substring rule unchanged. That rule
// is the open-vocabulary publish gate and must stay strict, but as a FABRICATION metric
// it counted plain plurals: a transcript saying "KPI" and a digest saying "KPIs" was
// scored a fabrication. Six of 22 flagged tokens in one sweep were that (KPIs, CTAs,
// PRs). Pluralisation is not invention, so the metric folds it in — while the genuine
// catches it found stay caught, e.g. a DNS record completed with a domain the source
// never stated.
func UnverifiedIdentifiers(d Digest, source string) []string {
	_, dropped := VerifyTopics(Identifiers(d), source)
	if len(dropped) == 0 {
		return nil
	}
	hay := strings.ToLower(source)
	var out []string
	for _, t := range dropped {
		if !inflectionPresent(strings.ToLower(t), t, hay) {
			out = append(out, t)
		}
	}
	return out
}

// pluralOfSomethingPresent reports whether the term is merely an inflection of text the
// source does contain. Only the two regular English plural forms; anything cleverer would
// start excusing real fabrications.
func pluralOfSomethingPresent(term, hay string) bool {
	return inflectionPresent(term, term, hay)
}

// inflectionPresent takes both the lowercased term and the ORIGINAL spelling, because the
// compound rule needs the capitalisation to tell a named part from a generic modifier.
func inflectionPresent(term, orig, hay string) bool {
	if hay == "" {
		return false
	}
	for _, suf := range []string{"s", "es"} {
		if base := strings.TrimSuffix(term, suf); base != term && len(base) >= 2 {
			if strings.Contains(hay, base) {
				return true
			}
		}
		if strings.Contains(hay, term+suf) {
			return true
		}
	}
	// A coined compound whose every part is in the source is not invention either. This was
	// the dominant flagged class once the synopsis existed — "CPU-efficient", "CPU-based",
	// "OAuth-first", "Signal-to-Atlas" — all formed from vocabulary the conversation used.
	// A synopsis has to generalise to be worth reading, and compounding is how English
	// generalises, so scoring it as fabrication penalises the section for doing its job.
	if parts := strings.Split(orig, "-"); len(parts) > 1 {
		checked, all := 0, true
		for _, part := range parts {
			// Only the capital-bearing parts are the claim. Such a token reaches this gate
			// BECAUSE of its internal capitals, so those parts name the thing; a lowercase
			// modifier like "first", "based" or "efficient" is generic English and requiring
			// it in the source would flag "CPU-efficient" over the word "efficient".
			if !hasUpper(part) || len(part) < 2 {
				continue
			}
			checked++
			if !strings.Contains(hay, strings.ToLower(part)) {
				all = false
				break
			}
		}
		if checked > 0 && all {
			return true
		}
	}
	return false
}

func hasUpper(s string) bool {
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			return true
		}
	}
	return false
}

// frictionWords are the vocabulary a report uses when it admits difficulty.
var frictionWords = []string{
	// Stems, not inflections. The list previously mixed the two — "struggl" and "clarif"
	// were stems while "reverse"/"reversed" were spelled out, so the noun "reversal" was
	// missed entirely by a list that exists to find exactly that word.
	"revert", "revers", "fail", "broke", "broken",
	"wrong", "incorrect", "retr", "again", "correct",
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
	// This is a FOURTH copy of proseHay's join (ProseFields + Insights + Unresolved) —
	// see the identical note above LeakedPromptWords' body join. Not refactored here
	// either (task-7b brief item, "also, cheaply"): same risk, same reasoning, flagged
	// rather than fixed.
	hay := strings.ToLower(strings.Join(
		append(ProseFields(d), append(d.Insights, d.Unresolved...)...), " "))
	for _, w := range frictionWords {
		if strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

// proseHay lowercases and joins every prose field, insight, and unresolved item into one
// substring-containment haystack. Factored out so RetainedFacts (production) and the eval
// harness's lostFacts diagnostic (digest_eval_test.go, -tags llmstudy) share exactly one
// build of it — the two had drifted into separate copies of the same join, which is the same
// class of defect a hand-enumerated field list is: a future section addition only needs to be
// remembered ONCE, here, instead of in every caller that wants "all the prose".
func proseHay(d Digest) string {
	return strings.ToLower(strings.Join(append(ProseFields(d), append(d.Insights, d.Unresolved...)...), " "))
}

// RetainedFacts counts how many of the given facts still appear after refinement.
// Used by the drift test: inject known facts, refine, measure survival.
//
// Reads ProseFields (via proseHay) rather than a hand-enumerated list of sections — the same
// defect ProseFields was introduced to fix elsewhere in this package. This function
// hand-listed six sections and so never checked Synopsis: the section a reader is most
// likely to read was the one section this drift check never verified.
func RetainedFacts(after Digest, facts []string) int {
	hay := proseHay(after)
	n := 0
	for _, f := range facts {
		if strings.Contains(hay, strings.ToLower(f)) {
			n++
		}
	}
	return n
}

// UnresolvedSentinel is the exact entry a digest must use when nothing is open.
//
// A required `unresolved` field defeats rubberstamping, but on a clean session it
// pushes the model to invent blockers — observed on real output, where a session the
// source described as "Fixed and verified live" produced "needs further testing" and
// "not fully understood", neither of which appeared anywhere in the conversation.
// An invented blocker is worse than a missing one, because a reader will act on it.
//
// The sentinel gives "nothing is open" a first-class expression, so the field can be
// honoured without fabrication, and so a scorer can tell the two apart.
const UnresolvedSentinel = "none - the work reached a stopping point"

// UsesUnresolvedSentinel reports whether the digest declared nothing open.
func UsesUnresolvedSentinel(d Digest) bool {
	if len(d.Unresolved) != 1 {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(d.Unresolved[0])), "none -")
}

// speculationMarkers are the phrasings a model reaches for when it has nothing real
// to report but a field it must fill.
var speculationMarkers = []string{
	"needs further", "need further", "further testing", "not fully understood",
	"may need", "might need", "could be improved", "should be monitored",
	"remains to be seen", "unclear whether", "not yet verified", "would benefit from",
	"potential", "possibly", "consider whether",
}

// LooksFabricatedUnresolved reports whether `unresolved` asserts speculative open
// items on work the source presents as finished.
//
// HEURISTIC, and weaker than LooksRubberstamped: that metric has real ground truth in
// the harvested `corrected` label, whereas this one infers "the source says it is
// done" from completion language. It will miss fabrications phrased confidently and
// may flag a genuine open item phrased tentatively. It is a screen for review, not a
// verdict — which is why the spec pairs it with the human usefulness gate.
func LooksFabricatedUnresolved(d Digest, f DigestFacts, source string) bool {
	if UsesUnresolvedSentinel(d) || len(d.Unresolved) == 0 {
		return false
	}
	// If the work actually hit friction, open items are expected.
	if f.Corrections > 0 || f.CorrectedTurns > 0 {
		return false
	}
	low := strings.ToLower(source)
	done := false
	for _, m := range []string{"verified", "all pass", "tests pass", "9 pass", "complete", "fixed and", "landed", "committed"} {
		if strings.Contains(low, m) {
			done = true
			break
		}
	}
	if !done {
		return false
	}
	for _, u := range d.Unresolved {
		ul := strings.ToLower(u)
		for _, m := range speculationMarkers {
			if strings.Contains(ul, m) {
				return true
			}
		}
	}
	return false
}
