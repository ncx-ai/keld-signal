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
//   - A DELIBERATE STRIDE OVERLAP. The newest beatOverlapPct of the PREVIOUS span is carried
//     into the next window — the spec's own wording — so consecutive beats share ground and a
//     change of subject is distinguishable from merely being shown different material.
//     RESERVED out of the budget rather than taken from what happens to be left over: on a
//     stride larger than the window the leftover is always zero, so an opportunistic overlap
//     would exist only on the sessions that need it least. It is additionally capped at
//     beatOverlapPct of the whole window, or a huge previous span would crowd out the new
//     material the beat is actually about.
//
//     Note the two percentages this produces are different quantities and both are reported:
//     the overlap is ~beatOverlapPct of the PREVIOUS window (the dial), which for two spans of
//     similar size is ~22% of the NEW window (what the model re-reads).
//
//   - BOUNDED BY CHARACTERS, not turn count, since turn sizes here vary by two orders of
//     magnitude. Oldest whole TURNS are dropped, which is the delimiter granularity AGENTS.md
//     requires, and the hole is MARKED where it falls.
//
// ⚠️ 100% turn coverage and a bounded window are MUTUALLY EXCLUSIVE on this corpus, and the
// spec's "must be 100%" cannot be met at `ctx` 8192. The arithmetic is not close: the largest
// stride is 52,148 runes, and 20,000 runes of real transcript measures 5,433 tokens
// (llama-server /tokenize, worst of four chunks — 3.68 chars/token), so a whole-stride window
// would need ~14,200 tokens against a context of 8,192 that also has to hold the record, the
// instructions and the generation. Coverage is therefore MEASURED and reported rather than
// asserted; it rises from a fifth of the transcript to most of it, and the shortfall is visible
// in the window itself rather than silent.
//
// This does not weaken the no-chain invariant. The overlap is TRANSCRIPT, not model output, so
// two beats sharing source turns cannot compound drift the way re-reading a previous summary
// does — which is exactly why the report-paraphrase design was removed and this is safe.
const (
	// BeatWindowChars bounds one beat window, and it is chosen against ctx rather than taste.
	// At 16,000 runes the window is ~4,350 tokens at the measured worst rate; with the record
	// block (bounded, <=~1,400 runes) and BeatPrompt's own ~1,900 runes of instructions that
	// is ~5,250 of 8,192, leaving room for the generation with margin. 20,000 was measured as
	// affordable too (~6,100 tokens) but it buys 11 percentage points of coverage for a 25%
	// larger prefill on every beat, and beat inference is already the sweep's dominant cost.
	BeatWindowChars = 16000
	// beatOverlapPct is how much of the previous span's tail is carried forward, and the cap
	// on what that may cost the window. The spec asks for ~25-30%; 28 sits in the middle of
	// that rather than on an edge.
	beatOverlapPct = 28
)

// beatOmittedNotice marks the hole a bounded window leaves in an otherwise contiguous span. It
// sits BETWEEN the overlap and the kept turns — where the missing material actually is — rather
// than at the top, because a leading notice reads as "the session started earlier" while this
// one means "these particular turns are missing".
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
	// Rendered is what the prompt gets: the overlap, the notice if there is a hole, then the
	// kept turns. Built here rather than by Render because the notice is not a turn and
	// inventing a fourth Role for it would ripple through mergeToolCounts and Observe.
	Rendered string

	SpanTurns    int // turns in the contiguous stretch since the previous beat
	KeptTurns    int // of those, how many the character bound left in
	OverlapTurns int // turns carried from the previous beat's span
	OverlapRunes int
	// PrevSpanRunes is the previous beat's whole stride — the quantity the dial is SET in.
	PrevSpanRunes int
	// PrevWindowRunes is what the previous beat actually READ, which is a different and smaller
	// number whenever the bound dropped turns from its stride. It is carried because it is the
	// spec's own denominator: "the last ~25-30% of the previous WINDOW carried into the next".
	// Reporting the carry against the stride instead understates it by however much of that
	// stride no window ever read — measured 16.6% of stride against 24.7% of window on the same
	// 28 pairs, i.e. the difference between missing the spec's band and sitting inside it.
	PrevWindowRunes int
	TotalRunes      int
}

// Dropped is how many turns of this beat's own stride the bound discarded. Those turns are
// covered by NO window — the old geometry's silent hole, now counted.
func (b BeatWindow) Dropped() int { return b.SpanTurns - b.KeptTurns }

// BeatWindower walks a session's deltas, handing out one contiguous window per beat.
//
// Stateful because the overlap and the "since the previous beat" boundary are both properties
// of the previous call. Zero value is ready to use.
type BeatWindower struct {
	// next is the first delta index no beat has consumed yet.
	next int
	// prevKept is what the previous beat actually READ — its bounded window's turns, not its
	// whole stride. The overlap is taken from here rather than from the stride so that "shared
	// ground" is true by construction: material the bound dropped was never on the previous
	// beat's screen, and carrying it forward would be presenting new text as shared.
	//
	// It used to hold the whole stride, and the tail of a stride IS the tail of its window in
	// every case this corpus produces (per-turn clipping bounds a turn at PerTurnChars = 1,200
	// runes, so a window that dropped anything still kept >10,000 runes, far more than the
	// <=4,480-rune overlap can ask for). That made the field's own comment — "the turns the bound
	// dropped are deliberately NOT in it" — false as written and true only by an argument about
	// two other constants. Holding the kept turns costs nothing and needs no argument.
	prevKept []Turn
	// prevSpanRunes is the previous beat's whole stride, which is what the dial is set in, and
	// prevWindowRunes is what that beat read. Both are reported; they are different quantities.
	prevSpanRunes   int
	prevWindowRunes int
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

	// The overlap budget is beatOverlapPct of the PREVIOUS span, capped at the same share of
	// the whole window. A small previous span therefore contributes its own tail rather than
	// all of itself — carrying a whole short span forward would make consecutive beats mostly
	// re-reads, which is the opposite of the "shared ground" the overlap is for.
	budget := b.prevSpanRunes * beatOverlapPct / 100
	if cap := BeatWindowChars * beatOverlapPct / 100; budget > cap {
		budget = cap
	}
	overlap := tailTurnsWithin(b.prevKept, budget)
	overlapRunes := turnsRuneLen(overlap)
	// The window pays for its own hole marker. The notice is not a turn, but it IS in the
	// rendered window, so a fit computed over turns alone overruns the bound by the notice's
	// length on any span that filled it — measured at 16,111 against 16,000 on a fixture sized to
	// land on the boundary. Charged in RUNES, the unit tailTurnsWithin and BeatWindowChars are
	// both in: the rune-versus-byte mismatch this package already paid for once (a reserve made in
	// runes against an assembly charged in bytes) starved the floor and panicked on real
	// multi-byte transcripts.
	//
	// Two-pass rather than one: whether the notice is needed at all is only known after fitting,
	// and re-fitting inside a smaller budget can only drop MORE turns, so the hole — and the
	// notice — remain. One iteration is therefore a fixed point, not the first step of a loop.
	kept := tailTurnsWithin(span, BeatWindowChars-overlapRunes)
	if len(kept) < len(span) {
		kept = tailTurnsWithin(span, BeatWindowChars-overlapRunes-runeLen(beatOmittedNotice))
	}

	w := deltas[upto]
	w.Turns = append(append([]Turn{}, overlap...), kept...)
	out := BeatWindow{
		Window:          w,
		Rendered:        renderBeatWindow(overlap, len(kept) < len(span), kept),
		SpanTurns:       len(span),
		KeptTurns:       len(kept),
		OverlapTurns:    len(overlap),
		OverlapRunes:    overlapRunes,
		PrevSpanRunes:   b.prevSpanRunes,
		PrevWindowRunes: b.prevWindowRunes,
	}
	out.TotalRunes = runeLen(out.Rendered)
	b.prevKept, b.prevSpanRunes, b.prevWindowRunes = kept, turnsRuneLen(span), out.TotalRunes
	b.next = upto + 1
	return out
}

// renderBeatWindow lays out overlap, hole marker, kept turns.
func renderBeatWindow(overlap []Turn, holed bool, kept []Turn) string {
	var b strings.Builder
	write := func(turns []Turn) {
		for _, t := range turns {
			b.WriteString(string(t.Role))
			b.WriteString(": ")
			b.WriteString(t.Text)
			b.WriteString("\n")
		}
	}
	write(overlap)
	if holed {
		b.WriteString(beatOmittedNotice)
	}
	write(kept)
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

// BeatCoverage accumulates turn coverage and window overlap across a session's beats — the two
// numbers the geometry is judged on, and neither was measured before.
//
// Coverage is over the turns the beats' strides SPAN, which is every turn in the walked prefix:
// the strides are contiguous and disjoint by construction, so their union is the transcript and
// anything the character bound drops is covered by nothing.
type BeatCoverage struct {
	SpanTurns       int
	KeptTurns       int
	OverlapRunes    int
	PrevSpanRunes   int
	PrevWindowRunes int
	WindowRunes     int
	Windows         int
	LargestRunes    int
}

// Add folds one beat window in.
func (c *BeatCoverage) Add(b BeatWindow) {
	if b.SpanTurns == 0 && b.Rendered == "" {
		return
	}
	c.Windows++
	c.SpanTurns += b.SpanTurns
	c.KeptTurns += b.KeptTurns
	c.OverlapRunes += b.OverlapRunes
	c.PrevSpanRunes += b.PrevSpanRunes
	c.PrevWindowRunes += b.PrevWindowRunes
	c.WindowRunes += b.TotalRunes
	if b.TotalRunes > c.LargestRunes {
		c.LargestRunes = b.TotalRunes
	}
}

// TurnCoverage is the fraction of spanned turns that appear in some beat window, as a
// percentage.
func (c BeatCoverage) TurnCoverage() float64 {
	if c.SpanTurns == 0 {
		return 0
	}
	return 100 * float64(c.KeptTurns) / float64(c.SpanTurns)
}

// OverlapPct is how much of the average beat window is material the previous beat also read.
func (c BeatCoverage) OverlapPct() float64 {
	if c.WindowRunes == 0 {
		return 0
	}
	return 100 * float64(c.OverlapRunes) / float64(c.WindowRunes)
}

// OverlapOfPrevSpanPct is the carry-forward against the quantity the dial is SET in: the previous
// beat's whole stride, including the part of it the character bound dropped.
//
// It is NOT the spec's number, and this was mislabelled as "share of the previous window" until it
// was measured against both: 16.6% of stride versus 24.7% of window over the same 28 pairs. The
// gap is entirely the material no window read, so on a session whose strides fit the bound the two
// coincide and on a long-stride session they do not. Reporting the wrong one made a design sitting
// inside the spec's 25-30% band look like it was missing the band by 8 points.
func (c BeatCoverage) OverlapOfPrevSpanPct() float64 {
	if c.PrevSpanRunes == 0 {
		return 0
	}
	return 100 * float64(c.OverlapRunes) / float64(c.PrevSpanRunes)
}

// OverlapOfPrevWindowPct is the spec's quantity: what share of what the previous beat actually
// READ the next beat re-reads.
func (c BeatCoverage) OverlapOfPrevWindowPct() float64 {
	if c.PrevWindowRunes == 0 {
		return 0
	}
	return 100 * float64(c.OverlapRunes) / float64(c.PrevWindowRunes)
}
