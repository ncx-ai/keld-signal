package llmstudy

import (
	"strings"
)

// The report's caps are LIST caps, and only list caps: how many insights and open items a
// report carries (DefaultListCap) and how long one entry may be (DefaultListEntryCap).
//
// There is no prose cap. Seven per-section rune caps used to live here — synopsis 650,
// done/current/why/next 900, happened 1400, structure 1600 — and CapSections clipped every
// refinement's output to them. They are removed, both the enforcement and the constants; see
// CapSections for why, for what the measurements taken under them do and do not establish,
// and for where length guidance lives now (the prompt, as guidance).
//
// The two survive for different reasons, and only one of them is a prompt bound.
// DefaultListCap is: priorOpenItems applies it directly to what a refinement is shown, so it
// bounds the prompt's open-item accounting whether or not this function ever ran on that prev.
// DefaultListEntryCap is NOT — promptOpenItemCap intercepts every item at 80 runes regardless
// of its stored length, and the prompt is measured insensitive to this value (see its own doc).
// It stays as a REPORT-QUALITY bound on a single entry: a list entry is one sentence or two by
// construction, so a 2,000-rune "entry" is a section in the wrong field, which is a different
// thing from a prose section being long.
const (
	DefaultListCap = 12
	// DefaultListEntryCap bounds a single insights/unresolved ENTRY's length, separately
	// from DefaultListCap's bound on entry COUNT. Neither DigestSchema (which only floors
	// prose length, via digestMinProse — there is no ceiling) nor CapSections bounded this
	// before, so a schema-legal digest with DefaultListCap Unresolved items of 2,000 runes
	// each blew a refine prompt to ~30,180 runes against the 11,000 budget once fed back
	// verbatim as priorOpenItems — and fitTurns then dropped the new turn outright because
	// its room went negative, which is the worst-consequence failure mode this package has
	// (an over-budget prompt truncates mid-JSON and drops the digest).
	//
	// A list entry is ONE insight or ONE open item, not a section — it should hold a real
	// sentence or two ("waiting on vendor confirmation of the rollback window" is 47 runes;
	// this package's own real examples run 20-60) but nothing near 2,000. 300 is roughly
	// two generous sentences: 5-15x this package's own real usage, so an honest entry is
	// never touched.
	//
	// NOT justified on prompt-room grounds (an earlier version of this doc was, and that
	// justification is now WRONG): task-7b fix round 3 added promptOpenItemCap (clips
	// every open item to 80 runes for the PROMPT) and boundRetainList (bounds
	// Identifiers(prev)'s output, which reads Insights too), both independent of what
	// this constant allows in STORAGE — so the assembled prompt is now completely
	// insensitive to this value. Measured directly against
	// TestWorstCasePromptOnBothPaths' construction (then named
	// TestRefinePromptFromRealisticWorstCaseMargin) with insights/
	// unresolved items sized at 300, 400, 800 and 2000: all four yield an identical
	// worst-case prompt. (The rune figure that used to be quoted here, 10,921, was
	// measured against the 11,000 budget; the live number at the 14,000 budget is
	// 13,994 — TestWorstCasePromptOnBothPaths/refine prints it. The insensitivity is
	// the point, not the absolute value.) This constant's only remaining job is report
	// quality: what a human reads in the stored digest. See promptOpenItemCap and
	// boundRetainList for the prompt-facing bounds, and store-full-feed-bounded
	// generally.
	DefaultListEntryCap = 300
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
	// updateSectionsMarker is the literal line the assembly writes immediately after the
	// window (i.e. where it ENDS) — the refine path's counterpart to
	// createSectionsMarker in digest.go. Named for the same reason: updateTailLen, the
	// assembly below, and the backstop's window-extraction all reference the identical
	// string instead of independently-typed copies (this exact string used to be
	// duplicated in two tests as a local `const tailMarker`, which is exactly the kind
	// of copy that can drift).
	updateSectionsMarker = "\nProduce the UPDATED report, same sections:\n"
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
// gets the chance to reword an old one. The fourth, unbounded growth, is handled for the
// LISTS by CapSections; prose growth is no longer bounded at all — it was, by a rune cap per
// section, when the previous report's prose was embedded here and its length was this prompt's
// length. Nothing embeds it now, so length is a question about the report's reader rather than
// about this prompt, and it is asked in the prompt as guidance. See CapSections.
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
	p, _ := updatePromptAndWindow(prev, in)
	return p
}

// updatePromptAndWindow is DigestUpdatePromptFrom's body, returning the conversation window
// fitTurns produced alongside the prompt — see createPromptAndWindow's doc in digest.go for
// why the window is returned rather than recovered by landmark.
func updatePromptAndWindow(prev Digest, in RefineInput) (prompt, window string) {
	var head strings.Builder
	head.WriteString("You are updating a report on a work session, for the person doing the work and for a manager who was not present.\n\n")
	head.WriteString("Session context: ")
	// Bounded, not verbatim — task-7b fix round 3 (minor G); see sessionLabelCap's doc
	// in digest_fit.go and DigestCreatePromptWithView's identical fix.
	head.WriteString(clipProse(in.SessionLabel, sessionLabelCap))
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
	// that made the counts authoritative. (Those caps no longer exist — CapSections
	// clips no prose. The measurement is quoted for what it always said: the losses
	// were not happening at a cap, so removing the caps was never going to be the fix,
	// and Part 8 of the findings doc measures exactly that.)
	//
	// ⚠️ MEASURED INSUFFICIENT ON ITS OWN. That reasoning held while the retain-list
	// REINFORCED embedded prior prose. As the ONLY channel carrying the prior report's
	// specifics — which is what it became once CarryForward was deleted — it does not
	// hold: task-8 measured fact retention at 50.0% (anchor on) / 56.2% (off) against a
	// >=90% threshold, down from 94.9% under the embedded-prose scheme, with 161 of 240
	// named specifics dropped by the model while explicitly listed below under "each must
	// still appear". The list is re-derived from the LAST report only, so every drop is
	// permanent (the one-way cascade). Raising either cap cannot help — neither ever
	// bound. The next experiment is a CUMULATIVE retain-list (a union over all prior
	// reports), which is a design change and is deliberately not landed unmeasured.
	// Identifiers reads the FULL prior digest: a specific first named in a present-state
	// section must still survive, and the retain-list is now the only place a
	// refinement sees it at all — nothing else carries the prior report's text forward.
	//
	// boundRetainList is applied HERE, at the rendering layer — Identifiers() itself
	// stays unbounded (task-7b constraint: its regex and dedup logic are untouched, and
	// its full output is what UnverifiedIdentifiers/FabricatedNext/the eval harness need
	// to check a report's specifics against the source, not just what a prompt can
	// afford). CRITICAL per task-7b fix round 3 (finding B): Identifiers reads
	// d.Insights and d.Unresolved, not just prose, and NOTHING bounded its output's
	// count or total length before this — a digest whose sections are densely full of
	// distinct identifier-shaped names (not the sparse "2 specifics + filler" shape
	// packSpecifics tests exercised) can produce a retain-list many times the entire
	// prompt budget on its own; see boundRetainList's doc for the measured numbers.
	if named := boundRetainList(Identifiers(prev)); len(named) > 0 {
		rest.WriteString("\nSPECIFICS ALREADY REPORTED (each must still appear, unless the new part shows it was wrong):\n  ")
		rest.WriteString(strings.Join(named, ", "))
		rest.WriteString("\n")
	}
	// Hand back the prior open items and require a verdict on each. Prose alone did not
	// work: "drop what is now closed" left resolved items in the list across every
	// refinement, because nothing checked. Naming them and requiring an accounting is the
	// same deterministic-anchoring shape the retain-list above uses — and note that shape
	// is NOT uniformly effective: it worked here (T8 stale open items 0.0%) and it did NOT
	// work for fact retention once it became the only channel (50.0%/56.2% vs >=90%; see
	// the retain-list note above). What is measured is that naming beats prose
	// instructions for open-item CLOSURE, not that naming fixes retention. A single
	// item's TEXT is
	// bounded for the prompt (promptOpenItemCap on priorOpenItems): store full, feed
	// bounded, the same principle DefaultListEntryCap applies to the stored report — two
	// deliberately different numbers for different readers, since the report a person
	// reads can afford a richer item than the accounting block a model must fit
	// alongside everything else. As of task-7b fix round 3 (finding C), COUNT is bounded
	// there too, not just length: DefaultListCap is meant to hold this at 12, but that
	// bound is enforced by CapSections, which runs on a REFINEMENT's OUTPUT — the very
	// FIRST prev a session ever sees comes straight from CreateDigestWithView, which
	// returns straight from callValid with no cap at all (DigestSchema places no
	// maxItems on Unresolved), so the first refinement's prev can carry far more than 12
	// items. priorOpenItems now bounds count defensively, independent of whether
	// CapSections ever ran on this particular prev.
	if open := priorOpenItems(prev); len(open) > 0 {
		rest.WriteString("\nOPEN ITEMS FROM THAT REPORT — account for EVERY one, in exactly one place:")
		rest.WriteString("\n  keep it in unresolved if it is still open, or name it in closed if the new")
		rest.WriteString("\n  part resolved it. Do not silently drop one.\n  ")
		rest.WriteString(strings.Join(open, "\n  "))
		rest.WriteString("\n")
	}
	// Hand over what the newest user turns are about. Gated on a MEASURED focus shift,
	// not applied to every refinement. Unconditionally, the anchor bought recency at a
	// real price: fact retention fell 97.4% -> 88.3% and fabricated open items rose
	// 4.1% -> 10.2%, both consistent with the model re-weighting toward the newest turns
	// and shedding what came before. The trigger already decides when the subject
	// changed, so a routine refresh is left alone and only a genuine shift gets the pull.
	//
	// 97.4%, not 96.1%: this comment and SubjectShifted's doc (digest_recency.go) quoted
	// two different baselines for the SAME experiment. 97.4% is the figure the design doc
	// records and is the one both sites now use.
	//
	// Those figures predate the story rollup and are NOT the current evidence for gating.
	// Under the current scheme, task-8 measured anchor-always vs anchor-never at T4 50.0%
	// vs 56.2%, T7 4.5% vs 2.3%, T3 16.7% vs 8.3% — the same direction, and the reason
	// the anchor stays gated. But that comparison is CONFOUNDED and the magnitudes are not
	// attributable to the anchor: the sweep derived one `reason` value that fed both this
	// block and SessionRecord.NoteTurningPoint, so the anchor-off arm also ran with an
	// empty TurningPoints list for its entire duration (see digest_eval_test.go). The
	// harness is fixed; the corrected comparison is UNMEASURED.
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
	beats, view := fitDiscretionary(in.Beats, in.SessionView,
		runeLen(head.String())+runeLen(rest.String()), updateTailLen())

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
	// Held in a variable, not just written: the backstop is handed the window fitTurns
	// actually produced rather than re-deriving it from the finished prompt, which content
	// quoting windowHeader or updateSectionsMarker defeats in both directions (task-7b fix
	// round 4 — see assertPromptWithinBudget's doc).
	window = fitTurns(in.NewTurns, runeLen(b.String())+updateTailLen())
	b.WriteString(window)
	b.WriteString(updateSectionsMarker)
	b.WriteString(digestSections)
	b.WriteString(updateRules)
	b.WriteString(digestRules)
	b.WriteString("\nRespond with JSON only.\n")
	p := b.String()
	// The backstop (task-7b fix round 3, finding A) — see its doc in digest_fit.go.
	assertPromptWithinBudget(p, window)
	return p, window
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
		// RUNES throughout, via runeLen — and `fixed` MUST be a rune count too. An earlier
		// version of this comment argued the byte/rune mixture was "always safe (over- not
		// under-estimating overhead)". It is not, and that reasoning is what let the defect
		// live: this function reserves MinTurnChars in RUNES, so if the ASSEMBLY then charges
		// its own prefix in bytes, the reservation this function certified is short by the
		// prefix's multi-byte excess. Over-estimating overhead is safe for the BUDGET and
		// unsafe for the FLOOR, which is the invariant this function exists to protect. Six
		// of 293 real refine steps panicked on exactly that.
		overhead := fixed + tail + runeLen(windowHeader)
		if cand != "" {
			overhead += runeLen(beatsHeader) + runeLen(cand)
		}
		// + len(omittedNotice) is task-7b fix round 3 (finding F): fitTurns reserves
		// omittedNotice's own length out of `room` whenever the turns it is handed do
		// not already fit — which is the case in every one of these worst-case
		// scenarios, and in general whenever this function's own k==0 fallback is
		// reached, since that is exactly "nothing discretionary is left to give the
		// window more room". Without this term, fitDiscretionary could certify a k that
		// leaves EXACTLY MinTurnChars of `room`, and fitTurns would then hand back
		// room-97 runes of actual window content once the notice is written — the same
		// unaccounted-constant shape as findings (b) and (c), just one level up: an
		// overhead estimate omitting a cost the real assembly (here, fitTurns) always
		// pays once it needs to clip at all.
		//
		// k == 0 (beats fully dropped) is the last possible attempt: whether or not the
		// floor is actually reachable, there is nothing further to trim, so it always
		// returns rather than falling through to the unreachable line below.
		if overhead+MinTurnChars+runeLen(omittedNotice) <= DefaultPromptCharBudget || k == 0 {
			// clipSessionViewFor is given overhead PLUS viewHeader's own length: it computes
			// room for the view's CONTENT only, and the header is written in addition to
			// that content, so a view candidate that were sized against `overhead` alone
			// would itself eat into the same MinTurnChars reservation the beat count above
			// was just fitted around. clipSessionViewFor folds in its own omittedNotice
			// reservation (see its doc in digest_synopsis.go), so it is not repeated here.
			return cand, clipSessionViewFor(view, overhead+runeLen(viewHeader))
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

// promptOpenItemCap bounds a single open item's length AS EMBEDDED IN THE PROMPT — a
// separate, tighter number from DefaultListEntryCap, which bounds the STORED report.
//
// This is task-7b's fix-round 2 finding: DefaultListEntryCap (300) is a report-quality
// limit, tuned so a human reader never loses a real item's substance — but 12 items at
// 300 runes each (the actual result of a digest that has passed through CapSections,
// not the looser 60-rune assumption an earlier version of the worst-case test used)
// push the FIXED part of a refine prompt — before a single beat, view rune, or
// conversation turn is added — past what's left once MinTurnChars and the instructional
// tail are reserved.
//
// Lowering DefaultListEntryCap instead was rejected (per the fix-round instruction this
// responds to): that constant governs what a human reads in the stored report, and
// shrinking it to buy prompt room is the exact trade the prose caps were shrunk on once
// (500/900 measured fact retention 100%->83.3% under the then-current embedded-prose
// scheme — a measurement, but NOT evidence that caps are what delete named specifics;
// task-8 refuted that mechanism outright, and the prose caps are now gone entirely — see
// CapSections). The report-quality argument stands on its own: the report keeps its richer
// items; only the prompt's rendering of them is bounded.
//
// 80 is still generous against this package's own real item lengths (20-60 runes, per
// DefaultListEntryCap's doc) — real items are essentially never truncated — while
// cutting the worst case enough to matter. Measured in ISOLATION against fix round 3's
// own worst-case construction (now TestWorstCasePromptOnBothPaths, which by
// then also carries boundRetainList and the open-item COUNT bound — reverting only this
// clip, temporarily, to a no-op): assembled prompt 11,681 runes against the THEN-CURRENT
// 11,000 budget (margin -681), window 97 against the 1,600 floor. Restored: prompt 10,921
// runes (margin +79), window 1,797 (margin +197) — both real margins, not a graze,
// which matters because a bound that only just clears was exactly what let an earlier
// worst-case measurement here read "+44" while the design's own enforced constants
// actually produced "-860" (task-7b fix round 2's own finding, since independently
// reproduced at a denser, more adversarial construction in fix round 3 — see
// boundRetainList). TestWindowKeepsItsFloorAtTheBoundary is the sibling test isolating
// the window floor under beat pressure alone (no open items, no view).
//
// The -681/+79 pair above is stated against the 11,000 budget it was measured at, and is
// left as-is deliberately: it is a BEFORE/AFTER pair for this clip, and both halves move
// together when the budget does. The budget is now 14,000 (DefaultPromptCharBudget), at
// which TestWorstCasePromptOnBothPaths/refine reports 13,994 with this clip in place —
// re-derive from the test rather than from this comment when an absolute number is needed.
const promptOpenItemCap = 80

// priorOpenItems is the previous report's open list, excluding the sentinel — there is
// nothing to account for when the last report said nothing was open. Every surviving
// item is clipped to promptOpenItemCap for the PROMPT (clipProse marks a truncated item
// with a trailing "…" so the model is not misled into thinking it has the item's full
// text).
//
// COUNT is also bounded here, at DefaultListCap — task-7b fix round 3 (finding C): an
// earlier version of this doc claimed count was "untouched" because DefaultListCap was
// assumed to already hold it there via CapSections. That is true for `prev` on the
// SECOND refinement onward (RefineFrom caps its own output before returning it) but
// NOT for the very first one: CreateDigestWithView returns straight from callValid, and
// DigestSchema places no maxItems on Unresolved, so a schema-legal first digest can
// carry far more than DefaultListCap items. Measured directly with 40 identifier-dense
// items (this count cap reverted, promptOpenItemCap's length clip left active): window
// 1,107 against the 1,600 floor (-493), total still inside budget (10,940 of 11,000) —
// silent, because nothing was checking the window specifically. Bounded here,
// independent of whether CapSections ever ran on this particular prev, the same fix
// shape as promptOpenItemCap's own length bound.
func priorOpenItems(prev Digest) []string {
	var out []string
	for _, item := range prev.Unresolved {
		if !UsesUnresolvedSentinelText(item) {
			out = append(out, item)
		}
	}
	out = tailN(out, DefaultListCap)
	return capEntryLength(out, promptOpenItemCap)
}

// retainListMaxCount and retainListMaxTotal bound the retain-list — Identifiers(prev)'s
// output — AS EMBEDDED IN THE PROMPT. task-7b fix round 3 (finding B, marked CRITICAL):
// Identifiers has no count or length cap of its own (by design — task-7b's own
// constraints keep its regex and dedup logic untouched, and its full output is what
// UnverifiedIdentifiers/FabricatedNext/the eval harness check a report's specifics
// against the source with, not just what one prompt can afford), and DigestUpdatePromptFrom
// used to splice its ENTIRE output into the prompt verbatim.
//
// The earlier worst-case test's packSpecifics-based construction (2 named specifics +
// filler WORDS per field/item) badly understated this: filler words are not
// identifier-shaped (no digit, no internal capital, no separator) and contribute
// essentially nothing to Identifiers()' output, so that test's own retain-list measured
// only 938 runes across 94 distinct identifiers — a real number for THAT input, but not
// a worst case, because ordinary English can occupy the exact same rune budget while
// contributing dozens of times as many identifiers if every "word" is identifier-shaped
// instead of filler. An independent review reported finding a threshold as low as 90
// distinct identifiers already breaching the window floor with the total still inside
// budget (silent), 304 pushing the total itself over budget, and a worst case — every
// OTHER constant honoured post-CapSections — of 24,689 runes with a 97-rune window.
// Reproduced directly with this fix reverted, using packIdentifiers (not packSpecifics)
// against the worst-case construction now in TestWorstCasePromptOnBothPaths (which also
// combines finding (C)'s 40-item count pressure): 33,566 runes, 97-rune window — the
// window figure matches the review's exactly; the total is larger here because that
// test's construction is more adversarial again, per the instruction not to report a
// worst case without deriving every length from an enforced constant.
//
// Both count AND total length are bounded, independently, because identifiers vary
// wildly in length (a bare short word vs. a long dotted path) — a count cap alone
// cannot bound total rune cost, and a length cap alone cannot bound how many distinct
// names a densely-specific report can still cram into it. Recency-preferring on both
// axes: tailN's own reasoning (older specifics have already survived several
// refinements; newer ones have not yet been seen at all) extended to a second pass that
// fills from the NEWEST end of whatever tailN kept, since Identifiers' output order
// follows the underlying digest's own field order (prose fields, then insights, then
// unresolved) — later text is newer state. A dropped specific is a real drop, never a
// silent truncation of one — clipping an identifier mid-name would manufacture a fake
// one, so only which ENTRIES survive changes, never their spelling.
const (
	retainListMaxCount = 60
	retainListMaxTotal = 700
)

// boundRetainList keeps the newest specifics that fit, dropping the ones that do not.
//
// Fills newest-first and SKIPS an entry too long to fit, rather than dropping everything
// older than it — task-7b fix round 4 (finding 3). The earlier version evicted only from
// the oldest end (`for len(kept) > 0 && ... { kept = kept[1:] }`), which cannot remove an
// entry that exceeds retainListMaxTotal ON ITS OWN: the loop shrank from the far end while
// the offending entry survived every iteration, until `len(kept) > 0` finally failed and the
// function returned EMPTY. Measured: retainListMaxCount-1 ordinary specifics plus one
// 1,118-rune path-shaped identifier at the newest end kept ZERO entries.
//
// The retain-list is the only channel carrying the prior report's named specifics into a
// refinement — nothing else carries its text forward at all — so that silently deleted the
// entire fact-retention anchor, the metric this design is judged on, over one oversized
// name, and on the input shape most likely to produce one: a long real path.
//
// Skipping rather than stopping is what makes the drop proportionate: one unusable entry
// costs exactly itself, and the older entries behind it, which DO fit, are still offered.
func boundRetainList(named []string) []string {
	kept := tailN(named, retainListMaxCount)
	var out []string
	for i := len(kept) - 1; i >= 0; i-- {
		// Measured with retainListJoinedLen rather than a running sum, so what is bounded is
		// exactly what the prompt writes, separators included.
		cand := append([]string{kept[i]}, out...)
		if retainListJoinedLen(cand) > retainListMaxTotal {
			continue
		}
		out = cand
	}
	return out
}

// retainListJoinedLen is the rune length of the retain-list exactly as it is written
// into the prompt (strings.Join(named, ", ")), so the total-length bound above measures
// what the prompt actually pays, not a proxy for it.
func retainListJoinedLen(v []string) int {
	if len(v) == 0 {
		return 0
	}
	return runeLen(strings.Join(v, ", "))
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
//
// RUNES, and via updateSectionsMarker rather than a re-typed copy of it — task-7b fix round
// 4 (finding 5), following createTailLen, which already did both. The two defects were the
// same defect twice: a hardcoded literal is the drift updateSectionsMarker was introduced to
// prevent (fix round 3 unified the assembly and the backstop on the constant but left this
// function's copy behind), and len() on a literal carrying em dashes — which this package's
// prose leans on throughout — counts BYTES, while every budget this figure is compared
// against (DefaultPromptCharBudget, MinTurnChars) is a rune count. The byte version was
// described here as "always safe (bytes >= runes, so it only ever over-estimated overhead)":
// safe for the BUDGET, and NOT for the floor — over-estimated overhead spends real window
// room, and the round-3 review measured that same reasoning, applied to the assemblies'
// b.Len(), starving the floor on 2% of real transcripts. See runeLen in digest_fit.go.
func updateTailLen() int {
	return len([]rune(updateSectionsMarker)) + len([]rune(digestSections)) +
		len([]rune(updateRules)) + len([]rune(digestRules)) +
		len([]rune("\nRespond with JSON only.\n"))
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
	return CapSections(merged, DefaultListCap), nil
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

// CapSections bounds the report's LISTS — how many insights and open items it carries, and how
// long a single entry may be. It does not touch prose.
//
// It used to clip all seven prose sections at rune counts and append an ellipsis. That is
// removed, for two reasons — one structural, one about the product.
//
// STRUCTURAL: the caps existed to bound the digest's length because the digest's length WAS
// prompt length. The previous report was embedded verbatim in the next prompt (CarryForward,
// since deleted), so every rune a section kept was a rune the next refinement paid for. Nothing
// embeds prior prose any more: a refinement reads the measured record, a beat series, the
// retain-list and the recent window (see DigestUpdatePromptFrom). Traced rather than assumed —
// this function has exactly ONE production caller, RefineFrom, applied to its return value, and
// no prompt builder in this package reads a Digest's prose at all. The only two channels by
// which a stored report still reaches a prompt are bounded independently, by their own
// constants: Identifiers(prev) by retainListMaxCount/retainListMaxTotal (boundRetainList) and
// the open-item accounting by DefaultListCap + promptOpenItemCap (priorOpenItems). So prose
// clipping bought no prompt room, and TestRefinePromptIsInsensitiveToStoredProseLength pins
// that: prose an order of magnitude past the old caps moves neither the budget nor the window
// floor.
//
// PRODUCT: cutting the last words off a finished paragraph is not a length instruction, it is
// damage — clipProse's own doc records what it cost, a real digest reading "...saved as
// `worktree-cleanup-blocke". Length guidance belongs in the prompt instead, where a writer can
// weigh it.
//
// ⚠️ But putting it there was MEASURED AND REVERTED, so digestSections says nothing about length
// beyond the synopsis's original "Three or four sentences". Two wordings were run over both arms
// of the full sweep. The first (a note after the section list, plus per-section sentence guides)
// LOST TWO DIGESTS in each arm to "unresolved is empty" through all 5 retries — the note was the
// last thing read before a required list. Moving it ahead of the list fixed that and made T9
// (current-describes-completion) worse instead: 1 flagged report at baseline against 6 (anchor
// ON) and 9 (OFF), real items reading "is complete" in `current`, plausibly because that
// section's guide competes with its own "nothing in progress" instruction. Nothing about a
// section's LENGTH needed fixing either: without any cap the largest section measured is 1,253
// runes. See Part 8 of
// docs/superpowers/plans/2026-08-07-conversational-dimensions-findings.md before adding length
// wording here again — and leave `current` out of it.
//
// What the caps' own measurements do and do not establish. Lowering them was tried and rejected:
// 500/900 cut visible truncation from 5/20 to 2/20 but dropped fact retention 100% -> 83.3%.
// That is a real measurement of a scheme that no longer exists (embedded prose + the no-shrink
// rule), and the MECHANISM it was attributed to here — "a shorter cap clips exactly the named
// specifics the retain-list preserves" — is the hypothesis task-8 refuted: 161 of 240 specifics
// were named in the retain-list and dropped anyway, and neither retain-list cap ever bound.
// Removing the caps entirely is the other end of that same experiment, and its effect on
// retention is measured, not argued — see Part 8 of
// docs/superpowers/plans/2026-08-07-conversational-dimensions-findings.md.
//
// Unbounded prose is a real cost, and it is a cost to the READER, not to the context: nothing
// stops four refinements under an EXTEND instruction from growing structure or happened
// without limit. That is measured per section by the sweep (REPORT LENGTH in
// TestDigestRefineQuality) rather than prevented here.
func CapSections(d Digest, maxList int) Digest {
	// Count first, then length: tailN already keeps only the most recent maxList entries,
	// so capEntryLength only has to clip the entries that actually survive into the digest.
	d.Insights = capEntryLength(tailN(d.Insights, maxList), DefaultListEntryCap)
	d.Unresolved = capEntryLength(tailN(d.Unresolved, maxList), DefaultListEntryCap)
	return d
}

// capEntryLength clips every list entry to n runes via clipProse — see DefaultListEntryCap.
// Independent of the entry COUNT bound (maxList/tailN): this bounds each entry that
// survives, not how many of them do.
func capEntryLength(v []string, n int) []string {
	if len(v) == 0 {
		return v
	}
	out := make([]string, len(v))
	for i, s := range v {
		out[i] = clipProse(s, n)
	}
	return out
}

// tailN keeps the last n entries — the most recent, since older insights have already
// survived several refinements while newer ones have not been seen at all.
func tailN(v []string, n int) []string {
	if n <= 0 || len(v) <= n {
		return v
	}
	return v[len(v)-n:]
}
