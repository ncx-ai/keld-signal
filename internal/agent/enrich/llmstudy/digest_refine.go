package llmstudy

import "strings"

// Default caps. Prose is per-section runes; lists are entry counts.
//
// Structure gets a larger budget than the other prose sections because it is
// cumulative — it describes everything built so far, and truncating it loses the
// earliest parts of the picture, which are exactly the parts a newcomer needs.
const (
	DefaultProseCap     = 900
	DefaultStructureCap = 1600
	DefaultListCap      = 12
)

// DigestUpdatePrompt builds the refinement prompt.
//
// Refine loops fail in four known ways; this prompt addresses three. Recency bias:
// carry earlier material forward unless contradicted. Silent contradiction: revise in
// place and say what changed. Drift: insights are merged in CODE, not re-prosed by
// the model, so it never gets the chance to reword an old one. The fourth, unbounded
// growth, is handled by CapSections.
//
// Contains no worked examples, deliberately. An example in an earlier prompt here was
// copied verbatim into a report about an unrelated session — the same failure this
// branch documented for the extraction prompts.
func DigestUpdatePrompt(prev Digest, sessionLabel, newTurns, facts string) string {
	var b strings.Builder
	b.WriteString("You are updating an existing report on a work session, for the person doing the work and for a manager who was not present.\n\n")
	b.WriteString("Session context: ")
	b.WriteString(sessionLabel)
	b.WriteString("\n\nEXISTING REPORT:\n")
	b.WriteString(DigestJSON(prev))
	b.WriteString("\n\nMEASURED CONTEXT for the whole session so far (authoritative — your report must be consistent with it):\n")
	b.WriteString(facts)
	b.WriteString("\nNEW PART OF THE CONVERSATION, since that report:\n")
	b.WriteString(newTurns)
	b.WriteString("\nProduce the UPDATED report, same sections:\n")
	b.WriteString(digestSections)
	b.WriteString(`
Updating rules:
  - Keep what the existing report says unless the new part contradicts it. Do not
    drop earlier material simply because the new part does not mention it.
  - Where something changed, revise it in place and say what changed.
  - structure: EXTEND the picture with newly revealed parts, and correct anything the
    new part shows was wrong. Do not rewrite it from scratch.
  - insights: add only genuinely new learnings. Do not restate existing ones.
  - unresolved must describe the CURRENT state: drop what is now closed, add what has
    newly opened.
`)
	b.WriteString(digestRules)
	b.WriteString("\nRespond with JSON only.\n")
	return b.String()
}

// RefineDigest produces the next digest, then merges insights and caps growth.
func (l *Llama) RefineDigest(prev Digest, sessionLabel, newTurns, facts string) (Digest, error) {
	var next Digest
	if err := l.call(DigestUpdatePrompt(prev, sessionLabel, newTurns, facts), DigestSchema(), &next); err != nil {
		return Digest{}, err
	}
	return CapSections(MergeInsights(prev, next), DefaultProseCap, DefaultListCap), nil
}

// MergeInsights carries prior insights forward verbatim and appends genuinely new
// ones, deduplicated case-insensitively.
//
// This is the drift mitigation, and it is done in code rather than asked of the model
// on purpose: repeated re-summarising is precisely what erodes the most valuable
// content, so the model never gets the chance to reword an old insight.
//
// Unresolved is NOT merged — it describes current state, so the new answer wins.
// Merging it would accumulate stale blockers forever, which is the opposite failure.
func MergeInsights(prev, next Digest) Digest {
	out := next
	seen := map[string]bool{}
	merged := make([]string, 0, len(prev.Insights)+len(next.Insights))
	add := func(s string) {
		t := strings.TrimSpace(s)
		if t == "" {
			return
		}
		k := strings.ToLower(t)
		if seen[k] {
			return
		}
		seen[k] = true
		merged = append(merged, t)
	}
	for _, s := range prev.Insights {
		add(s)
	}
	for _, s := range next.Insights {
		add(s)
	}
	out.Insights = merged
	return out
}

// CapSections bounds prose length and list size so a long session cannot grow the
// digest past the context the refine loop exists to keep bounded.
func CapSections(d Digest, maxProse, maxList int) Digest {
	d.Done = clip(d.Done, maxProse)
	d.Happened = clip(d.Happened, maxProse)
	d.Structure = clip(d.Structure, DefaultStructureCap)
	d.Current = clip(d.Current, maxProse)
	d.Why = clip(d.Why, maxProse)
	d.Next = clip(d.Next, maxProse)
	d.Insights = tailN(d.Insights, maxList)
	d.Unresolved = tailN(d.Unresolved, maxList)
	return d
}

// tailN keeps the last n entries — the most recent, since older insights have already
// survived several refinements while newer ones have not been seen at all.
func tailN(v []string, n int) []string {
	if n <= 0 || len(v) <= n {
		return v
	}
	return v[len(v)-n:]
}
