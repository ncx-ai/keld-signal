package llmstudy

import (
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
// specifics the retain-list is there to preserve. Raising ctx was also measured and
// rejected: 3008 MB at ctx 6144 and 3161 MB at ctx 8192, both over the 3 GB budget.
//
// Prompt room no longer comes from embedding a shrunk copy of the prior report
// (CarryForward, since deleted): the refinement prompt now carries no prior prose at
// all — a beat series (the paraphrase) plus Identifiers' retain-list (the deterministic
// anchor) stand in for it, at a fraction of the character cost. See DigestUpdatePromptFrom.
const (
	DefaultProseCap     = 900
	DefaultStructureCap = 1600
	DefaultHappenedCap  = 1400
	DefaultSynopsisCap  = 650
	DefaultListCap      = 12
)

// beatsHeader, viewHeader and windowHeader are the literal headings the assembly writes
// immediately before the beat series, the whole-session view, and the recent window,
// respectively. Defined once here and used by BOTH DigestUpdatePromptFrom's real
// assembly and fitDiscretionary's overhead estimate, so the two cannot drift apart —
// fitDiscretionary previously hardcoded neither and simply omitted them, so it could
// certify a beat count as "leaves room for the floor" while the real assembly, which
// writes these headings in addition to the content being budgeted, did not. A future
// wording change to any of these three strings now automatically keeps both sides
// honest instead of silently reopening that gap.
const (
	beatsHeader  = "\nBEATS, oldest first — each written from its own window (indicative):\n"
	viewHeader   = "\nWHOLE SESSION, sampled from start to now (coarse — for the shape of the work, not its detail):\n"
	windowHeader = "\nNEW PART OF THE CONVERSATION (evidence):\n"
)

// RefineInput is everything a refinement reads, grouped by truth-status: the record is
// measured, the beat series is paraphrased, the window is evidence. None of it is the
// previous report's own prose — that is the point: compression can only be deliberate,
// as opposed to forbidden, once the model is no longer shown text it must preserve
// verbatim to avoid losing information nothing else carries.
type RefineInput struct {
	SessionLabel string
	Record       SessionRecord
	Beats        []Beat
	SessionView  string
	NewTurns     string
	Why          TriggerReason
}

// DigestUpdatePromptFrom builds the refinement prompt.
//
// Refine loops fail in four known ways. Recency bias: earlier material must not be
// dropped just because the new part is silent on it — it is carried forward unless
// contradicted (see updateRules). Silent contradiction: revise in place and say what
// changed. Drift: insights are merged in CODE, not re-prosed by the model, so it never
// gets the chance to reword an old one. The fourth, unbounded growth, is handled by
// CapSections.
//
// No prior prose is embedded. The previous report reaches the model as a paraphrase
// ladder (the beat series) plus a deterministic retain-list (Identifiers) and open-item
// accounting (priorOpenItems) — "the work has been on X, Y, Z; specifically A, B, C;
// still open: D" — which is what allows compression to be deliberate instead of
// forbidden: a refinement that moves on to a new subject is no longer fighting an
// instruction to preserve the old report's exact wording.
//
// The measured record comes first, ahead of the beats and the window, because
// everything after it is indicative (the model's own prior paraphrasing) or evidence
// (raw turns) rather than authoritative — a model shown counts before prose holds its
// prose consistent with them, the same reasoning DigestCreatePromptWithView uses. It
// is written only when in.Record actually holds measured data (Populated()) — an
// unpopulated SessionRecord's zero-value counts ("turns=0 corrections=0") would
// otherwise be asserted as authoritative, and digestRules tells the model that
// corrections are a measured fact it must be consistent with: a legacy caller with no
// record to offer (see DigestUpdatePrompt below) must not have zero counts fabricated
// on its behalf, which reads as "nothing happened" against digestRules' own
// anti-rubberstamping instruction.
//
// Contains no worked examples, deliberately. An example in an earlier prompt here was
// copied verbatim into a report about an unrelated session — the same failure this
// branch documented for the extraction prompts.
func DigestUpdatePromptFrom(prev Digest, in RefineInput) string {
	var head strings.Builder
	head.WriteString("You are updating a report on a work session, for the person doing the work and for a manager who was not present.\n\n")
	head.WriteString("Session context: ")
	head.WriteString(in.SessionLabel)
	if pop := in.Record.Populated(); len(pop) > 0 {
		head.WriteString("\n\nSESSION RECORD (measured — authoritative):\n")
		head.WriteString(in.Record.Block())
		head.WriteString("populated fields: " + strings.Join(pop, ", ") + "\n")
	}

	// Everything below is load-bearing (the record above, and the window fitTurns
	// writes last, are the other two) — it is never trimmed. Only the beat series and
	// the whole-session view are discretionary; see fitDiscretionary.
	var rest strings.Builder
	// Hand back the previous report's named specifics as an explicit retain-list.
	//
	// Measured need: instrumenting retention showed 7 of 7 lost facts disappeared
	// while sections still had room under their caps — never at a cap. The model was
	// RECOMPRESSING on refinement (a section shrinking from 860 to 306 runes took two
	// named specifics with it), and prose instructions to "keep what the report says"
	// did not stop it. Naming the specifics deterministically is the same anchoring
	// that made the counts authoritative.
	// Identifiers reads the FULL prior digest: a specific first named in a present-state
	// section must still survive, and the retain-list is now the only place a
	// refinement sees it at all — nothing else carries the prior report's text forward.
	if named := Identifiers(prev); len(named) > 0 {
		rest.WriteString("\nSPECIFICS ALREADY REPORTED (each must still appear, unless the new part shows it was wrong):\n  ")
		rest.WriteString(strings.Join(named, ", "))
		rest.WriteString("\n")
	}
	// Hand back the prior open items and require a verdict on each. Prose alone did not
	// work: "drop what is now closed" left resolved items in the list across every
	// refinement, because nothing checked. Naming them and requiring an accounting is the
	// same deterministic anchoring that fixed fact retention. Verbatim by design: this is
	// an accounting requirement (account for EVERY one), not a leak of prior prose.
	if open := priorOpenItems(prev); len(open) > 0 {
		rest.WriteString("\nOPEN ITEMS FROM THAT REPORT — account for EVERY one, in exactly one place:")
		rest.WriteString("\n  keep it in unresolved if it is still open, or name it in closed if the new")
		rest.WriteString("\n  part resolved it. Do not silently drop one.\n  ")
		rest.WriteString(strings.Join(open, "\n  "))
		rest.WriteString("\n")
	}
	// Hand over what the newest user turns are about. Gated on a MEASURED focus shift,
	// not applied to every refinement. Unconditionally, the anchor bought recency at a
	// real price: fact retention fell 96.1% -> 88.3% and fabricated open items rose
	// 4.1% -> 10.2%, both consistent with the model re-weighting toward the newest turns
	// and shedding what came before. The trigger already decides when the subject
	// changed, so a routine refresh is left alone and only a genuine shift gets the pull.
	if in.Why == TriggerFocusShift {
		if subs := recentSubjectsOf(in.NewTurns); subs != "" {
			rest.WriteString("\nTHE LATEST TURNS ARE ABOUT: ")
			rest.WriteString(subs)
			rest.WriteString("\n  The subject of the work was measured to have CHANGED since that")
			rest.WriteString(" report. Re-scope the synopsis to the work as it now is, in its subject")
			rest.WriteString(" as well as its standing, and say what it grew out of. Leave the other")
			rest.WriteString(" sections' accumulated content alone.\n")
		}
	}

	// The beat series and the whole-session view are the only two DISCRETIONARY
	// claimants on the budget — see fitDiscretionary's doc for why both must yield
	// before the recent window, which current/why/next/unresolved are actually WRITTEN
	// from, is allowed to starve.
	beats, view := fitDiscretionary(in.Beats, in.SessionView, head.Len()+rest.Len(), updateTailLen())

	var b strings.Builder
	b.WriteString(head.String())
	if beats != "" {
		b.WriteString(beatsHeader)
		b.WriteString(beats)
	}
	b.WriteString(rest.String())
	if view != "" {
		b.WriteString(viewHeader)
		b.WriteString(view)
	}
	b.WriteString(windowHeader)
	b.WriteString(fitTurns(in.NewTurns, b.Len()+updateTailLen()))
	b.WriteString("\nProduce the UPDATED report, same sections:\n")
	b.WriteString(digestSections)
	b.WriteString(updateRules)
	b.WriteString(digestRules)
	b.WriteString("\nRespond with JSON only.\n")
	return b.String()
}

// fitDiscretionary decides how much of the beat series and the whole-session view the
// budget can afford, so that the recent window — what current, why, next and unresolved
// are actually WRITTEN from — keeps at least MinTurnChars whenever there is anything
// discretionary left to trim first.
//
// Before this, only the view ever yielded (clipSessionViewFor already reserves
// MinTurnChars against the view's OWN encroachment). The beat series was written at
// full size unconditionally, so a session with a long open-item list or a large
// retain-list — both load-bearing, per DigestUpdatePromptFrom — plus a full 12-beat
// series could exhaust the budget before the window's reserve was ever considered,
// leaving fitTurns nothing to work with but its own omitted-turns notice: a report
// written from zero conversation evidence, silently, because the prompt was still
// under budget by the only measure anyone was checking.
//
// `fixed` and `tail` cover everything load-bearing, but NOT the literal headings the
// assembly writes around the two discretionary sections — beatsHeader, viewHeader and
// windowHeader are added in explicitly below. Omitting them was itself a bug an earlier
// version of this function had: it certified a beat count as leaving room for the floor
// while the real, assembled prompt — which also pays for those headings — did not,
// undershooting the floor by exactly their combined length. What this function
// certifies must equal what DigestUpdatePromptFrom actually assembles, which is why the
// header constants are shared between the two rather than re-derived or hardcoded here.
//
// The view yields first (it always has — see clipSessionViewFor's doc, "the lowest-
// priority claimant"), then the beat series, by SHRINKING its selection rather than
// dropping it outright: SelectBeats already keeps the most informative beats as its
// count shrinks (first, last, subject changes), so trimming the COUNT degrades
// gracefully instead of blanking the ladder in one step. The beat count is chosen
// ASSUMING the view is already fully yielded (view's own header costs nothing until a
// candidate view size is found to survive on whatever the beat count leaves behind) —
// consistent with the view yielding first.
//
// If even beats="" and view="" cannot free enough room for the floor, the load-bearing
// content (the record, retain-list and open-item accounting) plus windowHeader plus the
// fixed instructional tail already exceed budget-MinTurnChars on their own — there is
// nothing left here to trim, and this function does not paper over that: it returns
// the smallest beats/view it tried, and the window gets whatever remains, which can be
// less than MinTurnChars. That is a real finding for whoever owns the budget, not a
// bug in this function.
func fitDiscretionary(allBeats []Beat, view string, fixed, tail int) (beats, clippedView string) {
	for k := MaxBeatSelection; k >= 0; k-- {
		var cand string
		if k > 0 {
			cand = RenderBeats(SelectBeats(allBeats, k))
		}
		// windowHeader is unconditional — the assembly always writes it, beats or no
		// beats, view or no view. beatsHeader is paid only when a beat is actually kept.
		overhead := fixed + tail + len(windowHeader)
		if cand != "" {
			overhead += len(beatsHeader) + len(cand)
		}
		// k == 0 (beats fully dropped) is the last possible attempt: whether or not the
		// floor is actually reachable, there is nothing further to trim, so it always
		// returns rather than falling through to the unreachable line below.
		if overhead+MinTurnChars <= DefaultPromptCharBudget || k == 0 {
			// clipSessionViewFor is given overhead PLUS viewHeader's own length: it computes
			// room for the view's CONTENT only, and the header is written in addition to
			// that content, so a view candidate that were sized against `overhead` alone
			// would itself eat into the same MinTurnChars reservation the beat count above
			// was just fitted around.
			return cand, clipSessionViewFor(view, overhead+len(viewHeader))
		}
	}
	return "", "" // unreachable: the k == 0 iteration above always returns.
}

// DigestUpdatePrompt, DigestUpdatePromptWithView and DigestUpdatePromptWithReason are the
// pre-beat entry points. Kept only so callers not yet carrying a RefineInput — several
// eval-harness files build under -tags llmstudy and are rewired to it in a later task —
// keep compiling. They have no beats and no session record to offer, so they delegate to
// DigestUpdatePromptFrom with a RefineInput that has neither. `facts` (the window-scoped
// DigestFacts block the old prompt embedded as "MEASURED CONTEXT") has no field on
// RefineInput at all: SessionRecord is what replaced it for the refine path, and a caller
// still on this legacy path has no record to hand over, so the parameter is accepted for
// signature compatibility and dropped rather than smuggled in some other way.
func DigestUpdatePrompt(prev Digest, sessionLabel, newTurns, facts string) string {
	return DigestUpdatePromptFrom(prev, RefineInput{SessionLabel: sessionLabel, NewTurns: newTurns})
}

// DigestUpdatePromptWithView is DigestUpdatePrompt plus the coarse whole-session view.
func DigestUpdatePromptWithView(prev Digest, sessionLabel, newTurns, sessionView, facts string) string {
	return DigestUpdatePromptFrom(prev, RefineInput{
		SessionLabel: sessionLabel, SessionView: sessionView, NewTurns: newTurns,
	})
}

// DigestUpdatePromptWithReason is DigestUpdatePromptWithView told why the refresh fired.
func DigestUpdatePromptWithReason(prev Digest, sessionLabel, newTurns, sessionView, facts string, why TriggerReason) string {
	return DigestUpdatePromptFrom(prev, RefineInput{
		SessionLabel: sessionLabel, SessionView: sessionView, NewTurns: newTurns, Why: why,
	})
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

// recentSubjectsOf pulls distinctive terms from the newest user turn of an already-rendered
// window, so the prompt builder needs no Window value.
func recentSubjectsOf(rendered string) string {
	lines := strings.Split(rendered, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if !strings.HasPrefix(lines[i], "user: ") {
			continue
		}
		w := Window{Turns: []Turn{{RoleUser, strings.TrimPrefix(lines[i], "user: ")}}}
		if subs := RecentSubjects(w, 1); len(subs) > 0 {
			return strings.Join(subs, ", ")
		}
		return ""
	}
	return ""
}

// updateTailLen is the size of everything appended after the turns, so fitTurns can
// budget against the whole prompt rather than just the part built so far.
func updateTailLen() int {
	return len("\nProduce the UPDATED report, same sections:\n") + len(digestSections) +
		len(updateRules) + len(digestRules) + len("\nRespond with JSON only.\n")
}

const updateRules = `
Updating rules:
  - Do not drop earlier material simply because the new part does not mention it — build
    on the beats and the specifics above unless the new part contradicts it. Every named
    specific listed above must still appear, unless the new part shows it was wrong.
    Compression is fine — shorten or drop framing — as long as no named specific and no
    open item is lost doing it.
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

// RefineFrom produces the next report from a RefineInput. No paraphrase of the previous
// report's own prose: the beats already supply the history, so the closure/staleness
// repairs below are the only code-side correction this path still needs.
func (l *Llama) RefineFrom(prev Digest, in RefineInput) (Digest, error) {
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
	repair := func(d Digest) Digest { return dropStaleOpenItems(applyClosures(d, up.Closed)) }

	// Validation runs on the REPAIRED digest, not the raw response. Validating the raw one
	// rejected a legitimate answer that moved every open item into "closed", leaving
	// unresolved momentarily empty — "unresolved is empty" then burned all 5 retries on a
	// digest the repairs would have completed. The repair supplies the sentinel.
	if err := l.callValid(DigestUpdatePromptFrom(prev, in), DigestUpdateSchema(), &up,
		func() error { return firstProblem(ValidateDigest(repair(up.Digest))) }); err != nil {
		return Digest{}, err
	}
	merged := mergeWithRetirement(prev, repair(up.Digest), up.Retired)
	return CapSections(merged, DefaultProseCap, DefaultListCap), nil
}

// RefineDigest, RefineDigestWithView and RefineDigestWithReason are the pre-beat entry
// points, kept only so callers not yet carrying a RefineInput keep compiling — several
// eval-harness files build under -tags llmstudy and are rewired to it in a later task.
// Like their prompt-builder counterparts they delegate to the new path with a RefineInput
// that has no beats and no record; `facts` has nowhere to go (see DigestUpdatePrompt) so
// it is accepted and dropped.
func (l *Llama) RefineDigest(prev Digest, sessionLabel, newTurns, facts string) (Digest, error) {
	return l.RefineFrom(prev, RefineInput{SessionLabel: sessionLabel, NewTurns: newTurns})
}

// RefineDigestWithView is RefineDigest given the coarse whole-session view.
func (l *Llama) RefineDigestWithView(prev Digest, sessionLabel, newTurns, sessionView, facts string) (Digest, error) {
	return l.RefineFrom(prev, RefineInput{
		SessionLabel: sessionLabel, SessionView: sessionView, NewTurns: newTurns,
	})
}

// RefineDigestWithReason is RefineDigestWithView told why the refresh fired.
func (l *Llama) RefineDigestWithReason(prev Digest, sessionLabel, newTurns, sessionView, facts string, why TriggerReason) (Digest, error) {
	return l.RefineFrom(prev, RefineInput{
		SessionLabel: sessionLabel, SessionView: sessionView, NewTurns: newTurns, Why: why,
	})
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
