package llmstudy

import "strings"

// Default caps. Prose is per-section runes; lists are entry counts.
//
// Structure gets a larger budget than the other prose sections because it is
// cumulative — it describes everything built so far, and truncating it loses the
// earliest parts of the picture, which are exactly the parts a newcomer needs.
// These are report-quality limits, NOT context limits. Lowering them to buy prompt
// room was measured and rejected: 500/900 cut truncation from 5/20 to 2/20 but dropped
// fact retention from 100% to 83.3%, because a shorter cap clips exactly the named
// specifics the retain-list is there to preserve. Prompt room comes from CarryForward
// instead, which costs no report content. Raising ctx was also measured and rejected:
// 3008 MB at ctx 6144 and 3161 MB at ctx 8192, both over the 3 GB budget.
const (
	DefaultProseCap     = 900
	DefaultStructureCap = 1600
	DefaultHappenedCap  = 1400
	DefaultListCap      = 12
)

// CarryForward reduces a digest to the parts a refinement actually needs to read.
//
// Dropped: current, why and next are rewritten wholesale from the new turns, so
// embedding their prior text spent context on prose the model was told to replace.
// That is ~675 tokens of the refine prompt.
//
// Everything else is kept, and each for its own reason — two of which unit tests
// caught after a first version over-trimmed:
//   - done, happened, structure are cumulative: losing their prior text loses history.
//   - unresolved reads as present-state but its update is a DIFF ("drop what is now
//     closed, add what has newly opened"), uncomputable against a list never seen.
//   - insights are merged in CODE by MergeInsights, so they need not be carried — but
//     the prompt also forbids restating an existing one, and MergeInsights dedups only
//     on exact match, so a reworded restatement survives unless the model can see what
//     it must not repeat.
//
// Unlike lowering the section caps, this removes no content from the report itself.
// Specifics named in the dropped sections still survive, via the retain-list, which is
// built from the FULL prior digest.
func CarryForward(d Digest) Digest {
	out := d
	out.Current, out.Why, out.Next = "", "", ""
	return out
}

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
	b.WriteString("\n\nEXISTING REPORT (the cumulative sections; the present-state sections are")
	b.WriteString(" rewritten from the new turns below):\n")
	b.WriteString(DigestJSON(CarryForward(prev)))
	// Hand back the previous report's named specifics as an explicit retain-list.
	//
	// Measured need: instrumenting retention showed 7 of 7 lost facts disappeared
	// while sections still had room under their caps — never at a cap. The model was
	// RECOMPRESSING on refinement (a section shrinking from 860 to 306 runes took two
	// named specifics with it), and prose instructions to "keep what the report says"
	// did not stop it. Naming the specifics deterministically is the same anchoring
	// that made the counts authoritative.
	// Identifiers reads the FULL prior digest, not CarryForward's subset: a specific
	// first named in a present-state section must still survive, and the retain-list is
	// now the only place a refinement sees it.
	if named := Identifiers(prev); len(named) > 0 {
		b.WriteString("\n\nSPECIFICS ALREADY REPORTED (each must still appear in your updated report, unless the new part shows it was wrong):\n  ")
		b.WriteString(strings.Join(named, ", "))
	}
	b.WriteString("\n\nMEASURED CONTEXT for the whole session so far (authoritative — your report must be consistent with it):\n")
	b.WriteString(facts)
	b.WriteString("\nNEW PART OF THE CONVERSATION, since that report:\n")
	b.WriteString(fitTurns(newTurns, b.Len()+updateTailLen()))
	b.WriteString("\nProduce the UPDATED report, same sections:\n")
	b.WriteString(digestSections)
	b.WriteString(updateRules)
	b.WriteString(digestRules)
	b.WriteString("\nRespond with JSON only.\n")
	return b.String()
}

// updateTailLen is the size of everything appended after the turns, so fitTurns can
// budget against the whole prompt rather than just the part built so far.
func updateTailLen() int {
	return len("\nProduce the UPDATED report, same sections:\n") + len(digestSections) +
		len(updateRules) + len(digestRules) + len("\nRespond with JSON only.\n")
}

const updateRules = `
Updating rules:
  - Keep what the existing report says unless the new part contradicts it. Do not
    drop earlier material simply because the new part does not mention it.
  - Your updated report must not become shorter or less specific than the existing
    one. Refinement ADDS; it does not compress. Every named specific listed above
    must survive.
  - Where something changed, revise it in place and say what changed.
  - structure: EXTEND the picture with newly revealed parts, and correct anything the
    new part shows was wrong. Do not rewrite it from scratch.
  - insights: add only genuinely new learnings. Do not restate existing ones in
    different words — a reworded repeat is still a repeat.
  - retired: list an existing insight here, copied as written, ONLY if the new part shows
    it is WRONG or was reversed. This is for correcting the record, not for tidying it:
    an insight that is merely old, less interesting, or now less relevant stays. Use an
    empty list when nothing was contradicted.
  - unresolved must describe the CURRENT state: drop what is now closed, add what has
    newly opened.
`

// digestUpdate is a refinement response: a digest plus the insights it retires.
//
// Retired is deliberately NOT a field of Digest. It is an instruction to the merge, not
// part of the report, and storing it would publish a list of things the report no longer
// claims — noise for a reader and a second place for the record to disagree with itself.
type digestUpdate struct {
	Digest
	Retired []string `json:"retired"`
}

// DigestUpdateSchema is the digest schema plus the retirement list.
func DigestUpdateSchema() map[string]any {
	sc := DigestSchema()
	props, _ := sc["properties"].(map[string]any)
	props["retired"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	req, _ := sc["required"].([]string)
	sc["required"] = append(append([]string{}, req...), "retired")
	return sc
}

// RefineDigest produces the next digest, then merges insights and caps growth.
func (l *Llama) RefineDigest(prev Digest, sessionLabel, newTurns, facts string) (Digest, error) {
	var up digestUpdate
	if err := l.callValid(DigestUpdatePrompt(prev, sessionLabel, newTurns, facts), DigestUpdateSchema(), &up,
		func() error { return firstProblem(ValidateDigest(up.Digest)) }); err != nil {
		return Digest{}, err
	}
	merged := mergeWithRetirement(prev, up.Digest, up.Retired)
	return CapSections(merged, DefaultProseCap, DefaultListCap), nil
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
	return mergeWithRetirement(prev, next, nil)
}

// mergeWithRetirement is MergeInsights plus the ability to drop prior insights the new
// material contradicted. Retirement is bounded by maxRetiredPerRefinement and an entry
// that matches nothing is ignored, so a mis-stated retirement cannot delete the wrong
// insight.
func mergeWithRetirement(prev, next Digest, retired []string) Digest {
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
		// Exact match is not enough: a real digest carried the same sentence twice,
		// differing only by a leading "The".
		for _, existing := range merged {
			if insightsMatch(existing, t) {
				return
			}
		}
		seen[k] = true
		merged = append(merged, t)
	}
	retire := func(s string) bool {
		for _, r := range retired {
			if insightsMatch(r, s) {
				return true
			}
		}
		return false
	}
	dropped := 0
	for _, s := range prev.Insights {
		if dropped < maxRetiredPerRefinement && retire(s) {
			dropped++
			continue
		}
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
	d.Done = clipProse(d.Done, maxProse)
	// Happened gets its own larger budget because it is cumulative AND it is where
	// difficulty belongs. At the shared 900 it sat at its cap in real sessions, and since
	// clipping keeps the OLDEST text, the material lost was the most recent — which is
	// where a reversal appears. A marketing session's positioning reversal vanished
	// exactly this way while the opening survived, and that is a rubberstamped report
	// produced by a cap rather than by the model.
	d.Happened = clipProse(d.Happened, DefaultHappenedCap)
	d.Structure = clipProse(d.Structure, DefaultStructureCap)
	d.Current = clipProse(d.Current, maxProse)
	d.Why = clipProse(d.Why, maxProse)
	d.Next = clipProse(d.Next, maxProse)
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
