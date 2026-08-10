package llmstudy

import (
	"os"
	"strings"
)

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
	DefaultSynopsisCap  = 650
	DefaultListCap      = 12
)

// CarryForward reduces a digest to the parts a refinement actually needs to read.
//
// Synopsis is kept, and for a specific reason: its "what this work is" half is the most
// stable and most valuable thing in the report, and rederiving it from a late window is
// exactly how it would drift onto the last topic discussed. Its "where it stands" half is
// updated in place instead.
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
	return DigestUpdatePromptWithView(prev, sessionLabel, newTurns, "", facts)
}

// DigestUpdatePromptWithView is DigestUpdatePrompt plus the coarse whole-session view, so a
// refinement can keep the synopsis about the WORK rather than drifting it onto the newest
// window. See DigestCreatePromptWithView.
func DigestUpdatePromptWithView(prev Digest, sessionLabel, newTurns, sessionView, facts string) string {
	return DigestUpdatePromptWithReason(prev, sessionLabel, newTurns, sessionView, facts, TriggerNone)
}

// DigestUpdatePromptWithReason additionally tells the model WHY this refresh fired.
//
// The trigger policy already decides whether the subject of the work changed — that is what
// TriggerFocusShift means — and the prompt was asking the model to infer the same thing
// unaided. Measured on a real 44-window session, it inferred wrong in the costly direction:
// the synopsis still described the branch discussed in the opening windows, forty windows
// after the work had moved on, and asserted the session was "transitioning to a design
// phase" for something long abandoned.
func DigestUpdatePromptWithReason(prev Digest, sessionLabel, newTurns, sessionView, facts string, why TriggerReason) string {
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
	// Hand back the prior open items and require a verdict on each. Prose alone did not
	// work: "drop what is now closed" left resolved items in the list across every
	// refinement, because nothing checked. Naming them and requiring an accounting is the
	// same deterministic anchoring that fixed fact retention.
	if open := priorOpenItems(prev); len(open) > 0 {
		b.WriteString("\n\nOPEN ITEMS FROM THAT REPORT — account for EVERY one, in exactly one place:")
		b.WriteString("\n  keep it in unresolved if it is still open, or name it in closed if the new")
		b.WriteString("\n  part resolved it. Do not silently drop one.\n  ")
		b.WriteString(strings.Join(open, "\n  "))
	}
	if why == TriggerFocusShift {
		b.WriteString("\n\nNOTE: the subject of the work was measured to have CHANGED since that")
		b.WriteString(" report. Re-scope the synopsis to the work as it now is.")
	}
	b.WriteString("\n\nMEASURED CONTEXT for the whole session so far (authoritative — your report must be consistent with it):\n")
	b.WriteString(facts)
	if v := clipSessionViewFor(sessionView, b.Len()+updateTailLen()); v != "" {
		b.WriteString("\n\nWHOLE SESSION so far, sampled from start to now (coarse — for the shape")
		b.WriteString(" of the work, not its detail):\n")
		b.WriteString(v)
	}
	b.WriteString("\nNEW PART OF THE CONVERSATION, since that report:\n")
	b.WriteString(fitTurns(newTurns, b.Len()+updateTailLen()))
	b.WriteString("\nProduce the UPDATED report, same sections:\n")
	b.WriteString(digestSections)
	b.WriteString(updateRules)
	b.WriteString(digestRules)
	b.WriteString("\nRespond with JSON only.\n")
	return b.String()
}

// priorOpenItems is the previous report's open list, excluding the sentinel — there is
// nothing to account for when the last report said nothing was open.
func priorOpenItems(prev Digest) []string {
	var out []string
	for _, item := range prev.Unresolved {
		if !UsesUnresolvedSentinelText(item) {
			out = append(out, item)
		}
	}
	return out
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
  - synopsis: decide first whether the new part CONTINUES the work the report describes or
    has moved to a different subject. If it continues, keep the subject and framing and
    update only where the work now stands and where it is going. If the subject has genuinely
    moved on, RE-SCOPE it to the work as it now is, and say what it grew out of. A synopsis
    that still describes an abandoned starting point is worse than one that changed, because
    a reader will believe it. Do not re-scope merely because the newest turns are about a
    detail of the same work.
  - structure: EXTEND the picture with newly revealed parts, and correct anything the
    new part shows was wrong. Do not rewrite it from scratch.
  - insights: add only genuinely new learnings. Do not restate existing ones in
    different words — a reworded repeat is still a repeat.
  - retired: list an existing insight here, copied as written, ONLY if the new part shows
    it is WRONG or was reversed. This is for correcting the record, not for tidying it:
    an insight that is merely old, less interesting, or now less relevant stays. Use an
    empty list when nothing was contradicted.
  - unresolved must describe the CURRENT state. An item the new part resolved goes in
    the "closed" list, NOT in unresolved — a reader will act on anything left in
    unresolved, so a resolved item left there costs them real work. Add what has newly
    opened.
  - current names what is underway RIGHT NOW. If the last thing described is finished,
    the answer is "nothing in progress" — do not report a completed action here.
`

// digestUpdate is a refinement response: a digest plus the insights it retires.
//
// Retired is deliberately NOT a field of Digest. It is an instruction to the merge, not
// part of the report, and storing it would publish a list of things the report no longer
// claims — noise for a reader and a second place for the record to disagree with itself.
type digestUpdate struct {
	Digest
	Retired []string `json:"retired"`
	// Closed names prior open items the new part resolved. Like Retired it is an
	// instruction to the merge, not report content: a reader wants the current open list,
	// not a list of things that are no longer open.
	Closed []string `json:"closed"`
}

// DigestUpdateSchema is the digest schema plus the retirement list.
func DigestUpdateSchema() map[string]any {
	sc := DigestSchema()
	props, _ := sc["properties"].(map[string]any)
	strList := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	props["retired"] = strList
	props["closed"] = strList
	req, _ := sc["required"].([]string)
	sc["required"] = append(append([]string{}, req...), "retired", "closed")
	return sc
}

// RefineDigest produces the next digest, then merges insights and caps growth.
func (l *Llama) RefineDigest(prev Digest, sessionLabel, newTurns, facts string) (Digest, error) {
	return l.RefineDigestWithView(prev, sessionLabel, newTurns, "", facts)
}

// RefineDigestWithView is RefineDigest given the coarse whole-session view.
func (l *Llama) RefineDigestWithView(prev Digest, sessionLabel, newTurns, sessionView, facts string) (Digest, error) {
	return l.RefineDigestWithReason(prev, sessionLabel, newTurns, sessionView, facts, TriggerNone)
}

// RefineDigestWithReason is RefineDigestWithView told why the refresh fired.
func (l *Llama) RefineDigestWithReason(prev Digest, sessionLabel, newTurns, sessionView, facts string, why TriggerReason) (Digest, error) {
	// KELD_DIGEST_NO_CLOSURE=1 disables the code-side repairs. Study harness only.
	//
	// It does NOT isolate the staleness fix, and an earlier comment here claimed it did.
	// The prompt's open-item accounting block and the "closed" field stay active when this
	// is set, so only the repair is removed — and measured both ways, T8 is 0.0% either
	// way (0 of 61 with, 0 of 51 without). The PROMPT is what fixed staleness; the repair
	// guarantees the property rather than relying on compliance.
	//
	// What the repair demonstrably buys is delivery: T1 is 82.1% with it disabled versus
	// 100% with it. Introducing "closed" lets the model empty "unresolved" legitimately,
	// and without the sentinel substitution those refinements fail validation and are
	// dropped after exhausting their retries.
	enforce := os.Getenv("KELD_DIGEST_NO_CLOSURE") == ""

	var up digestUpdate

	// Two deterministic repairs: closures the model declared are applied, and an open item
	// the report itself contradicts under `done` is removed — one of those two claims is
	// wrong regardless of the conversation, and the open item is the one a reader acts on.
	//
	// Both are done in CODE rather than demanded of the model, which was measured. An
	// earlier version rejected a refinement whose accounting was incomplete: the model
	// could not satisfy it, so 10 of 56 attempts burned all 5 retries and were dropped,
	// trading T1 76.8% for T8 0%. A dropped digest is worse than a stale open item, and
	// retrying a failure the model cannot pass is the same mistake as retrying a
	// deterministic one.
	repair := func(d Digest) Digest {
		if !enforce {
			return d
		}
		return dropStaleOpenItems(applyClosures(d, up.Closed))
	}

	// Validation runs on the REPAIRED digest, not the raw response. Validating the raw one
	// rejected a legitimate answer that moved every open item into "closed", leaving
	// unresolved momentarily empty — "unresolved is empty" then burned all 5 retries on a
	// digest the repairs would have completed. The repair supplies the sentinel.
	if err := l.callValid(DigestUpdatePromptWithReason(prev, sessionLabel, newTurns, sessionView, facts, why), DigestUpdateSchema(), &up,
		func() error { return firstProblem(ValidateDigest(repair(up.Digest))) }); err != nil {
		return Digest{}, err
	}
	merged := mergeWithRetirement(prev, repair(up.Digest), up.Retired)
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
	// Synopsis is capped tighter than the rest on purpose: it is the one section a reader is
	// guaranteed to read, and three or four sentences is the whole point of it.
	d.Synopsis = clipProse(d.Synopsis, DefaultSynopsisCap)
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
