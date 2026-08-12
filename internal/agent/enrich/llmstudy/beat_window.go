package llmstudy

import (
	"fmt"
	"strings"
)

// Beat windows have their own geometry, because K was inherited from classification.
//
// `Mine` emits one window per user prompt holding the last K = 12 TURNS, where a turn is a
// user message, an assistant message or a tool invocation. Beats fire every
// BeatTurnsFromEnv() = 5 USER PROMPTS, and five user prompts span far more than 12 turns:
// measured on this corpus, the contiguous stretch between two beats runs min 2,940 / median
// 13,956 / p90 29,120 / max 52,148 runes across min 20 / median 94 / max 378 records, while a
// K=12 mined window is median 2,578 and max 5,815 runes. So consecutive beat windows were
// DISJOINT — about a fifth of each stride was read and the rest was read by nothing — and
// ChangedSubject was being asked whether the subject had changed while shown material that
// shared no ground with the previous beat's at all.
//
// The geometry here is the one the design spec asks for:
//
//   - CONTIGUOUS. A beat's span is every record since the previous beat's span ended, taken
//     from the disjoint per-user-prompt deltas whose union is the whole record stream. Nothing
//     is skipped by construction, which is a stronger property than the old design had at any K.
//
//   - DISJOINT: STRIDE EQUALS WINDOW, no carried overlap. Each beat owns turns N through M and
//     no other beat reads them. Three reasons, all measured:
//
//     1. The overlap's justification is retired. It existed to give ChangedSubject shared
//     ground, and ChangedSubject fired on 41 of 42 refinements and then on 0 of 46 packets
//     because it measured window ADJACENCY rather than subject change. Nothing in the
//     production design compares a beat to its predecessor.
//     2. Overlap costs coverage, which is the one measured deficit. 23.7% of every window was
//     material the previous beat had already read, while 6,228 of 14,154 spanned turns were
//     read by no beat at all. The re-read quarter is spent on turns nothing has seen.
//     3. Duplication produces restatement, and the suppression for it was inert: beatsRestate
//     discarded 0 of 70.
//
//   - BOUNDED BY CHARACTERS, not turn count, since turn sizes here vary by two orders of
//     magnitude. Oldest whole TURNS are dropped, which is the delimiter granularity AGENTS.md
//     requires, and the hole is MARKED — never silent — and CHARGED to the same bound (see
//     beatOmittedNotice and Next).
//
// ⚠️ 100% turn coverage and a bounded window are MUTUALLY EXCLUSIVE on this corpus, and the
// spec's "must be 100%" cannot be met at `ctx` 8192. The arithmetic is not close: the largest
// stride is 52,148 runes, and 20,000 runes of real transcript measures 5,433 tokens
// (llama-server /tokenize, worst of four chunks — 3.68 chars/token), so a whole-stride window
// would need ~14,200 tokens against a context of 8,192 that also has to hold the record, the
// instructions and the generation. Firing an extra beat whenever a stride reaches the bound
// would reach 100% at 2.1x the inference (measured: 267 beats against 128 over 20 sessions) and
// is deliberately NOT taken. Coverage is therefore MEASURED and reported rather than asserted,
// and the shortfall is visible in the window itself rather than silent.
//
// The cost of dropping the overlap is a SEAM MISREAD: an event whose antecedent fell on the
// other side of a boundary. That is a reviewable quantity — a bullet anchored only to the
// record and not to its own window — and the first production round reports it. If it is high
// the fix is a small marked context prefix, not a general overlap.
const (
	// BeatWindowChars bounds one beat window, and it is chosen against ctx rather than taste.
	// At 16,000 runes the window is ~4,350 tokens at the measured worst rate; with the record
	// block (bounded, <=~1,400 runes) and BeatPrompt's own ~1,900 runes of instructions that
	// is ~5,250 of 8,192, leaving room for the generation with margin. 20,000 was measured as
	// affordable too (~6,100 tokens) but it buys 11 percentage points of coverage for a 25%
	// larger prefill on every beat, and beat inference is already the sweep's dominant cost.
	BeatWindowChars = 16000
)

// beatOmittedNotice marks the hole a bounded window leaves in an otherwise contiguous span. It
// leads the window, because with stride equal to window the hole is always at the oldest end —
// the dropped turns are the head of this beat's own stride. Its WORDING is what stops it reading
// as "the session started earlier": it says these particular turns are missing and that no later
// window covers them either.
//
// It is charged to the window's own bound (see Next): it is not a turn, but it is in the window.
const beatOmittedNotice = "[turns since the previous update omitted to fit the context — " +
	"they are not covered by any later window either]\n"

// BeatWindow is one beat's input plus what it cost to bound it.
//
// Stats are carried rather than recomputed because coverage is a property of the SEQUENCE of
// windows, not of any one of them, and the two numbers the design is judged on — turn coverage
// and consecutive-window overlap — are only derivable while the windows are being produced.
type BeatWindow struct {
	// Window carries the turns, so GroundOf and anything else that reads a Window still work.
	Window Window
	// Rendered is what the prompt gets: the notice if there is a hole, then the kept turns.
	// Built here rather than by Render because the notice is not a turn and inventing a fourth
	// Role for it would ripple through mergeToolCounts and Observe.
	Rendered string

	SpanTurns  int // turns in the contiguous stretch since the previous beat
	KeptTurns  int // of those, how many the character bound left in
	TotalRunes int
}

// Dropped is how many turns of this beat's own stride the bound discarded. Those turns are
// covered by NO window — no later beat re-reads them, since the strides are disjoint — which is
// why the drop is marked in the window and counted here.
func (b BeatWindow) Dropped() int { return b.SpanTurns - b.KeptTurns }

// Holed reports whether this window carries the hole marker, i.e. whether the bound could not
// take the whole stride. It is the same predicate Next marks the window with, exposed so a
// coverage report does not have to substring-search the rendered text for it.
func (b BeatWindow) Holed() bool { return b.Dropped() > 0 }

// BeatWindower walks a session's deltas, handing out one contiguous window per beat.
//
// Stateful only for the "since the previous beat" boundary — one index. It used to also carry
// the previous beat's kept turns and two rune totals, all of them there to build and report the
// stride overlap; with stride equal to window there is nothing to carry. Zero value is ready to
// use.
type BeatWindower struct {
	// next is the first delta index no beat has consumed yet.
	next int
}

// Next returns the beat window ending at delta index upto (inclusive).
//
// deltas must be the disjoint per-user-prompt slices sessionDeltas produces, in order. Passing
// Mine's overlapping windows here would double-count turns, the same defect that inflated the
// session record's counts before sessionDeltas existed.
func (b *BeatWindower) Next(deltas []Window, upto int) BeatWindow {
	if upto >= len(deltas) {
		upto = len(deltas) - 1
	}
	if upto < b.next {
		// Nothing new since the last beat. Returns an empty window rather than repeating the
		// previous one: a caller asking twice for the same stride has a cadence bug, and
		// silently answering with stale material would hide it.
		return BeatWindow{}
	}
	var span []Turn
	for i := b.next; i <= upto; i++ {
		span = append(span, deltas[i].Turns...)
	}

	// The window pays for its own hole marker. The notice is not a turn, but it IS in the
	// rendered window, so a fit computed over turns alone overruns the bound by the notice's
	// length on any span that filled it — measured on the real corpus, five windows came out
	// over BeatWindowChars by 10, 24, 79, 84 and 38 runes, and the drop test that asserted the
	// bound passed anyway because its fixture left hundreds of runes of slack. Charged in RUNES,
	// the unit tailTurnsWithin and BeatWindowChars are both in: the rune-versus-byte mismatch
	// this package already paid for once (a reserve made in runes against an assembly charged in
	// bytes) starved the floor and panicked on 6 of 293 real steps.
	//
	// Two-pass rather than one: whether the notice is needed at all is only known after fitting,
	// and re-fitting inside a smaller budget can only drop MORE turns, so the hole — and the
	// notice — remain. One iteration is therefore a fixed point, not the first step of a loop.
	kept := tailTurnsWithin(span, BeatWindowChars)
	if len(kept) < len(span) {
		kept = tailTurnsWithin(span, BeatWindowChars-runeLen(beatOmittedNotice))
	}

	w := deltas[upto]
	w.Turns = append([]Turn{}, kept...)
	out := BeatWindow{
		Window:    w,
		Rendered:  renderBeatWindow(len(kept) < len(span), kept),
		SpanTurns: len(span),
		KeptTurns: len(kept),
	}
	out.TotalRunes = runeLen(out.Rendered)
	b.next = upto + 1
	return out
}

// renderBeatWindow lays out the hole marker, if there is a hole, then the kept turns.
func renderBeatWindow(holed bool, kept []Turn) string {
	var b strings.Builder
	if holed {
		b.WriteString(beatOmittedNotice)
	}
	for _, t := range kept {
		b.WriteString(string(t.Role))
		b.WriteString(": ")
		b.WriteString(t.Text)
		b.WriteString("\n")
	}
	return b.String()
}

// tailTurnsWithin keeps the NEWEST whole turns fitting n runes of rendered output.
//
// Newest-first for fitTurns' reason: the recent turns are what a beat is answering about, and
// the older ones have already been described by an earlier beat. Whole turns, because a turn is
// the delimiter — half a turn is the mid-clause cut AGENTS.md forbids, and clipTurn has already
// bounded each individual turn's own text.
func tailTurnsWithin(turns []Turn, n int) []Turn {
	if n <= 0 {
		return nil
	}
	used, start := 0, len(turns)
	for i := len(turns) - 1; i >= 0; i-- {
		cost := renderedTurnLen(turns[i])
		if used+cost > n {
			break
		}
		used += cost
		start = i
	}
	if start == len(turns) {
		return nil
	}
	return turns[start:]
}

// renderedTurnLen is what one turn costs in the rendered window, role label and newline
// included, so the bound measures what the prompt actually pays.
func renderedTurnLen(t Turn) int { return runeLen(string(t.Role)) + 2 + runeLen(t.Text) + 1 }

func turnsRuneLen(turns []Turn) int {
	n := 0
	for _, t := range turns {
		n += renderedTurnLen(t)
	}
	return n
}

// BeatPromptCharBudget bounds the assembled beat prompt.
//
// BeatPrompt asserted NOTHING about its arguments before this — recorded as a deferred residual
// through four fix rounds, on the grounds that a mined window was bounded at 12,000 runes by
// trimToWindowCap so no realistic input could cross ctx. Contiguous beat windows remove that
// accident: the stride they read is up to 52,148 runes before bounding, so the bound is now the
// only thing between a beat prompt and a context overflow, and an overflow here is silent —
// llama-server truncates the prompt and the model answers about whatever survived.
//
// 24,000 runes is BeatWindowChars plus the record block and the instructions with room to
// spare: ~6,520 tokens at the measured worst rate of 3.68 chars/token, leaving ~1,670 tokens
// for a generation that measured max 733 runes (~200 tokens). Panics for the same reason
// assertPromptWithinBudget does: BeatPrompt is a pure string builder with no error channel, and
// a violation is a budgeting error in this package rather than a bad caller input.
const BeatPromptCharBudget = 24000

// assertBeatPromptWithinBudget is the beat path's counterpart to assertPromptWithinBudget.
func assertBeatPromptWithinBudget(p string) {
	if n := runeLen(p); n > BeatPromptCharBudget {
		panic(fmt.Sprintf("llmstudy: assembled beat prompt is %d runes, over the %d-rune budget "+
			"— at ctx 8192 the server would truncate it and the beat would describe whatever "+
			"survived", n, BeatPromptCharBudget))
	}
}

// BeatCoverage accumulates turn coverage across a session's beats — the number the geometry is
// judged on, and it was not measured at all before.
//
// Coverage is over the turns the beats' strides SPAN, which is every turn in the walked prefix:
// the strides are contiguous and disjoint by construction, so their union is the transcript and
// anything the character bound drops is covered by nothing. With stride equal to window there is
// no double counting to correct for: KeptTurns is turns READ, not turn-readings.
type BeatCoverage struct {
	SpanTurns    int
	KeptTurns    int
	WindowRunes  int
	Windows      int
	Holed        int // windows carrying the hole marker
	LargestRunes int
}

// Add folds one beat window in.
func (c *BeatCoverage) Add(b BeatWindow) {
	if b.SpanTurns == 0 && b.Rendered == "" {
		return
	}
	c.Windows++
	c.SpanTurns += b.SpanTurns
	c.KeptTurns += b.KeptTurns
	c.WindowRunes += b.TotalRunes
	if b.Holed() {
		c.Holed++
	}
	if b.TotalRunes > c.LargestRunes {
		c.LargestRunes = b.TotalRunes
	}
}

// TurnCoverage is the fraction of spanned turns that appear in some beat window, as a
// percentage. Counts lead everywhere this is reported: both numerator and denominator move with
// the corpus, and a rate over a moved denominator produced five unreadable verdicts on this
// branch already.
func (c BeatCoverage) TurnCoverage() float64 {
	if c.SpanTurns == 0 {
		return 0
	}
	return 100 * float64(c.KeptTurns) / float64(c.SpanTurns)
}

// UnreadTurns is the deficit in the unit it is actually paid in: turns of transcript no beat
// window ever carried. Nothing else reports it, and it is the quantity the disjoint geometry
// exists to reduce.
func (c BeatCoverage) UnreadTurns() int { return c.SpanTurns - c.KeptTurns }
