package llmstudy

import "strings"

// VerifyTopics filters model-emitted topic terms down to those that literally
// occur in the source text, case-insensitively.
//
// This is the deterministic gate that makes an open vocabulary safe. A 9-entry
// `domain` enum cannot express what a conversation is actually about, but free
// text cannot be published either — it can paraphrase or quote the prompt and no
// check can prove it did not. Verification splits the difference: a term the model
// invented or reworded cannot be located and is dropped, so a surviving term is
// always text that demonstrably occurred in the conversation. That is the same
// guarantee enrich.Mask relies on for spans.
//
// Original casing is preserved — we report what the model said, having proven the
// text occurs. Duplicates and blanks are removed. Verification proves a term
// OCCURRED; it does not prove the model used it meaningfully, which is what the
// human quality review is for.
func VerifyTopics(raw []string, source string) (kept, dropped []string) {
	hay := strings.ToLower(source)
	seen := map[string]bool{}
	for _, term := range raw {
		t := strings.TrimSpace(term)
		if t == "" {
			continue
		}
		low := strings.ToLower(t)
		if seen[low] {
			continue
		}
		seen[low] = true
		if hay != "" && strings.Contains(hay, low) {
			kept = append(kept, t)
		} else {
			dropped = append(dropped, t)
		}
	}
	return kept, dropped
}
