package enrich

import (
	"os"
	"regexp"
	"strconv"
	"strings"
)

// gateEnabled reports whether enrichment gating is on. Default ON (empirically
// validated at 0/24 dangerous false gate-offs); set KELD_ENRICH_GATE_ENABLED to
// "0"/"false"/"off"/"no" (case-insensitive) to disable it and run every pass on
// every turn. Mirrors the off-switch shape of taskTypeDescriptionsEnabled() in
// a6_tasktype.go.
func gateEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("KELD_ENRICH_GATE_ENABLED"))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

// gateMaxTokens is the pre-filter's token cap (default 5). Override with
// KELD_ENRICH_GATE_MAX_TOKENS.
func gateMaxTokens() int {
	if v := strings.TrimSpace(os.Getenv("KELD_ENRICH_GATE_MAX_TOKENS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 5
}

// approvalLexicon: the closed set of tokens a content-free approval/continue
// turn is built from. Deliberately narrow — a miss only wastes compute (the turn
// gets enriched anyway); it must never contain a token that appears in real
// requests. See docs/superpowers/specs/2026-08-05-enrichment-gating-design.md.
var approvalLexicon = map[string]struct{}{}

func init() {
	for _, w := range strings.Fields(
		"ok okay yes yep yeah sure go ahead do that it this continue proceed " +
			"lgtm thanks thank you perfect sounds good ship please now cool great " +
			"nice hmm wait sec one works fine done") {
		approvalLexicon[w] = struct{}{}
	}
}

var alphaToken = regexp.MustCompile(`[a-z]+`)

// prefilterContentFree reports whether text is a short approval/continue turn
// answerable with NO model call: 1..gateMaxTokens() alpha tokens, every one in
// the approval lexicon. Model-free and conservative by design.
func prefilterContentFree(text string) bool {
	toks := alphaToken.FindAllString(strings.ToLower(text), -1)
	if len(toks) == 0 || len(toks) > gateMaxTokens() {
		return false
	}
	for _, t := range toks {
		if _, ok := approvalLexicon[t]; !ok {
			return false
		}
	}
	return true
}

// alwaysRunner is an OPTIONAL Extractor capability (mirrors ContextModel /
// MultiLabelModel): a pass that must run on EVERY turn regardless of the gate.
// Only governance (sensitivity) implements it; every other pass is gated.
// Absence ⇒ gated.
type alwaysRunner interface {
	AlwaysRun() bool
}

// THE GATE IS MODEL-FREE, and prefilterContentFree above is the whole of it.
//
// It used to have a second branch, speechActFragment — the committed
// speech_act result being "fragment". That facet was dropped at schema v9
// (docs/superpowers/specs/2026-08-24-facet-value-results.md: accuracy 0.695
// against a 0.713 majority baseline, worth less than the constant `command`),
// and the branch went with it.
//
// Losing it costs nothing measured. The gating design's own validation
// (docs/superpowers/specs/2026-08-05-enrichment-gating-design.md) put the
// pre-filter alone at precision 100% / recall 100% on-corpus and the fragment
// branch at recall 56% — a strict subset of what the lexicon already caught, so
// the combined gate's 0/24-dangerous result survives the removal intact.
//
// It is also compute-positive, which is the reason not to keep the pass purely
// as a gate signal: speech_act cost one inference on EVERY turn to gate out the
// small share of content-free turns that fall outside the lexicon (fragment was
// 4 of 164 gold rows, and the lexicon already catches most of those). Paying 1
// inference always to save 6 rarely is a loss.
//
// The residual risk direction is SAFE: a missed gate-off means a content-free
// turn gets fully enriched — wasted compute, never a wrong published answer.
