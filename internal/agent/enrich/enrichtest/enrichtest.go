// Package enrichtest provides a test-only fake enrich.Model carrying the
// regex/keyword detection logic that used to live in enrich.NewDeterministic
// (production deterministic backend; see the purge-deterministic design doc).
// It exists purely so tests can exercise the enrichment pipeline against a
// stable, dependency-free Model without needing the sidecar.
package enrichtest

import (
	"regexp"
	"strings"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// luhnValid strips non-digit characters from s and returns true if the
// resulting digit string passes the Luhn checksum.
func luhnValid(s string) bool {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	n := len(digits)
	if n < 13 || n > 19 {
		return false
	}
	sum := 0
	for i, ch := range digits {
		d := int(ch - '0')
		if (n-i)%2 == 0 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}

type fake struct {
	patterns map[string]*regexp.Regexp
	keywords map[string]map[string][]string // task -> label -> keywords
}

// NewFake returns a regex/keyword enrich.Model test double reproducing the
// detection semantics of the (former) deterministic production backend:
// email/api_key/SSN/credit-card(Luhn-valid)/phone spans, codegen/software
// keyword classification, and abstention (empty label, zero confidence) on
// tasks with no keyword priors (activity/personal/function_guess/subcategory).
func NewFake() enrich.Model {
	// task_type keyword priors keyed by canonical id.
	taskKW := map[string][]string{
		"code_generation":        {"function", "code", "implement", "class", "refactor", "deploy", "migration"},
		"summarization":          {"summarize", "summary", "tldr"},
		"translation":            {"translate", "translation"},
		"information_extraction": {"extract", "parse", "pull out"},
		"classification":         {"classify", "categorize", "label"},
		"reasoning":              {"why", "reason", "prove", "analyze"},
		"question_answering":     {"according to", "based on the", "what does the doc"},
		"text_generation":        {"draft", "compose", "write a post", "write an email"},
		"rewriting":              {"rewrite", "edit", "revise", "paraphrase"},
		"general":                {},
	}
	// A6 (schema v4) classifies task_type against readable label DESCRIPTIONS,
	// not the bare ids. Alias each description text to its id's keyword list so
	// this double recognises both the bare-id path (escape hatch / other facets)
	// and the default description path. Kept drift-proof by walking TaskTypeDefs.
	for _, d := range enrich.TaskTypeDefs {
		if kws, ok := taskKW[d.ID]; ok && d.Text != d.ID {
			taskKW[d.Text] = kws
		}
	}
	// speech_act keyword priors keyed by canonical id, aliased to the A6-style
	// description texts so this double works for both id and description label sets.
	speechKW := map[string][]string{
		"command":   {"add", "write", "fix", "implement", "create", "refactor", "build", "make", "set up", "update"},
		"question":  {"?", "how", "why", "what", "should", "which", "can you", "do i"},
		"statement": {"is broken", "keeps", "fails", "failing", "doubled", "the build", "there is", "we have"},
		"fragment":  {"ok", "ship it", "also", "same", "too", "and the", "ditto", "yes"},
	}
	for _, d := range enrich.SpeechActDefs {
		if kws, ok := speechKW[d.ID]; ok && d.Text != d.ID {
			speechKW[d.Text] = kws
		}
	}
	// domain keyword priors keyed by canonical id, aliased to the readable domain
	// DESCRIPTIONS (the extractor now classifies domain against DomainDefs texts).
	domainKW := map[string][]string{
		"software":  {"go", "python", "code", "api", "function", "bug"},
		"legal":     {"contract", "clause", "liability", "court"},
		"medical":   {"patient", "diagnosis", "symptom", "clinical"},
		"finance":   {"invoice", "revenue", "tax", "payment"},
		"science":   {"experiment", "hypothesis", "molecule"},
		"business":  {"customer", "market", "strategy"},
		"education": {"student", "lesson", "homework"},
		"creative":  {"poem", "story", "novel", "lyrics"},
	}
	for _, d := range enrich.DomainDefs {
		if kws, ok := domainKW[d.ID]; ok && d.Text != d.ID {
			domainKW[d.Text] = kws
		}
	}
	return &fake{
		patterns: map[string]*regexp.Regexp{
			"email":       regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`),
			"ssn":         regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),
			"credit_card": regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`),
			"phone":       regexp.MustCompile(`\b\+?\d{1,3}?[ .-]?\(?\d{2,4}\)?(?:[ .-]?\d{2,4}){2,3}\b`),
			"api_key":     regexp.MustCompile(`\b(?:sk-[A-Za-z0-9\-]{8,}|AKIA[0-9A-Z]{16}|ghp_[A-Za-z0-9]{20,})\b`),
			"secret":      regexp.MustCompile(`(?i)\b(?:password|passwd|secret|token)\s*[:=]\s*\S+`),
		},
		keywords: map[string]map[string][]string{
			"task_type":  taskKW,
			"speech_act": speechKW,
			"domain":     domainKW,
		},
	}
}

func (f *fake) Entities(text string, labels map[string]string) []enrich.Entity {
	var out []enrich.Entity
	for label := range labels {
		re, ok := f.patterns[label]
		if !ok {
			continue
		}
		for _, loc := range re.FindAllStringIndex(text, -1) {
			matched := text[loc[0]:loc[1]]
			if label == "credit_card" && !luhnValid(matched) {
				continue
			}
			out = append(out, enrich.Entity{
				Text:       matched,
				Label:      label,
				Start:      loc[0],
				End:        loc[1],
				Confidence: 0.95,
			})
		}
	}
	return out
}

func (f *fake) Classify(text string, tasks map[string][]string) map[string][]enrich.Ranked {
	lower := strings.ToLower(text)
	out := map[string][]enrich.Ranked{}
	for task, allowed := range tasks {
		kw := f.keywords[task]
		if kw == nil {
			// No keyword priors for this task (e.g. the newer job-category
			// facets): abstain rather than guessing via fallbackLabel, so
			// callers can gate on an empty label instead of treating a
			// meaningless last-resort pick as a real classification.
			out[task] = []enrich.Ranked{{Label: "", Confidence: 0}}
			continue
		}
		var best string
		bestN := 0
		for _, label := range allowed {
			n := 0
			for _, w := range kw[label] {
				n += strings.Count(lower, w)
			}
			if n > bestN {
				bestN, best = n, label
			}
		}
		if best == "" {
			best = fallbackLabel(allowed)
		}
		conf := 0.5
		if bestN > 0 {
			conf = 0.6 + 0.1*float64(min(bestN, 4))
		}
		out[task] = []enrich.Ranked{{Label: best, Confidence: conf}}
	}
	return out
}

func (f *fake) Extract(text string, labels map[string]string, tasks map[string][]string) enrich.ExtractResult {
	return enrich.ExtractResult{Entities: f.Entities(text, labels), Results: f.Classify(text, tasks)}
}

// ClassifyMulti satisfies enrich.MultiLabelModel: score each label by whether
// its own text appears in the prompt, keeping those at/above the threshold. A
// dependency-free stand-in for the sidecar's sigmoid multi-label path.
func (f *fake) ClassifyMulti(text string, tasks map[string]enrich.MultiTask) map[string][]enrich.Ranked {
	lower := strings.ToLower(text)
	out := map[string][]enrich.Ranked{}
	for task, mt := range tasks {
		var ranked []enrich.Ranked
		for _, label := range mt.Labels {
			conf := 0.0
			if strings.Contains(lower, strings.ToLower(label)) {
				conf = 0.8
			}
			if conf > 0 && conf >= mt.Threshold {
				ranked = append(ranked, enrich.Ranked{Label: label, Confidence: conf})
			}
		}
		out[task] = ranked
	}
	return out
}

// fallbackLabel prefers "other"/"general" if present, else the last item.
func fallbackLabel(allowed []string) string {
	for _, l := range allowed {
		if l == "other" || l == "general" {
			return l
		}
	}
	if len(allowed) > 0 {
		return allowed[len(allowed)-1]
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// piiTypes are the entity types the sidecar's presidio layer produces. api_key
// and secret are deliberately absent: credentials are the gitleaks layer's, not
// the PII scan's, and a double that blurred that would hide a real regression.
var piiTypes = []string{"email", "ssn", "credit_card", "phone", "person"}

// NewScan returns a test-only enrich.PIIScanner standing in for the sidecar's
// /pii route: the same regex detection this package's fake Model uses, reported
// as OFFSETS ONLY, exactly as the real endpoint does (the matched value never
// crosses that wire).
//
// It exists because SensitivityExtractor now treats the scan as the gated
// authority on the pattern types — a pipeline test with no scanner wired
// exercises the "no PII backend" path, not the normal one.
func NewScan() enrich.PIIScanner {
	f := NewFake().(*fake)
	return func(text string) (enrich.PIIResult, bool) {
		labels := make(map[string]string, len(piiTypes))
		for _, t := range piiTypes {
			labels[t] = t
		}
		ents := f.Entities(text, labels)
		spans := make([]enrich.Entity, 0, len(ents))
		for _, e := range ents {
			spans = append(spans, enrich.Entity{Label: e.Label, Start: e.Start, End: e.End, Confidence: e.Confidence})
		}
		return enrich.PIIResult{Spans: spans}, true
	}
}

// NewFailingScan returns a scanner that reports the service as unreachable —
// the case where PII detection is genuinely absent and the facet must say so.
func NewFailingScan() enrich.PIIScanner {
	return func(string) (enrich.PIIResult, bool) { return enrich.PIIResult{}, false }
}

// NewTruncatedScan returns a scanner that answers with real spans but reports
// that it could not read the whole input — a partial scan, whose clean-looking
// tail was never examined.
func NewTruncatedScan() enrich.PIIScanner {
	inner := NewScan()
	return func(text string) (enrich.PIIResult, bool) {
		r, ok := inner(text)
		r.Truncated = true
		return r, ok
	}
}
