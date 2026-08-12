package gate

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Verdict is one metric's movement between two sweeps.
type Verdict int

const (
	// Unchanged means the value did not move by more than the log's own resolution.
	Unchanged Verdict = iota
	// Improved means it moved in the metric's better direction.
	Improved
	// Regressed means it moved in the metric's worse direction. This is the loud case.
	Regressed
	// Appeared means the current run reports a metric the baseline did not have — a new
	// measurement, not a result.
	Appeared
	// Vanished means the baseline had a verdict and the current run does not. Treated as
	// LOUD, and deliberately not as "unchanged": an instrument that stopped returning a
	// verdict is how T11 and T12 both spent a whole round reporting a reassuring 0.0% that
	// was really an abstention. Losing a measurement is a regression in the measurement.
	Vanished
	// Artifact means the RATE moved but the flagged COUNT did not, across a denominator that
	// did. "T13 3.3% of 90 -> 3.4% of 88" is the same three items over two fewer candidates:
	// arithmetic, not behaviour. It is printed with both numbers and excluded from the revert
	// class — the first five gated steps on this branch all reported REGRESSED, and a gate
	// whose loud rows are mostly arithmetic is a gate that gets skimmed.
	Artifact
	// Unattributable means the rate and the count moved in OPPOSITE directions across a moved
	// denominator, so the movement cannot be assigned to behaviour or to sample size from the
	// log alone. Loud, and deliberately NOT a member of the revert class: reverting a change
	// because a denominator moved is the mistake in the other direction.
	Unattributable
	// Moot means the metric is informational — reported, never judged.
	Moot
)

func (v Verdict) String() string {
	switch v {
	case Improved:
		return "improved"
	case Regressed:
		return "REGRESSED"
	case Appeared:
		return "new"
	case Vanished:
		return "MEASUREMENT LOST"
	case Artifact:
		return "rate artifact"
	case Unattributable:
		return "UNATTRIBUTABLE"
	case Moot:
		return "info"
	}
	return "unchanged"
}

// Row is one metric compared across two sweeps.
type Row struct {
	Metric  Metric
	Base    Value
	Cur     Value
	Verdict Verdict
	// Notes carry the things a bare verdict hides: a denominator that moved, a numerator
	// that could not be recovered, a threshold still being failed.
	Notes []string
}

// epsilon is the smallest movement worth calling a movement. The sweep prints rates to one
// decimal place, so anything under half of that resolution is a rounding artifact rather
// than a result.
const epsilon = 0.05

// Compare judges one arm.
func Compare(base, cur *Sweep) []Row {
	out := make([]Row, 0, len(Metrics))
	for _, m := range Metrics {
		b, haveB := base.Values[m.Key]
		c, haveC := cur.Values[m.Key]
		r := Row{Metric: m, Base: b, Cur: c}
		switch {
		case (!haveB || b.Missing) && (!haveC || c.Missing):
			r.Verdict = Unchanged
			r.Notes = append(r.Notes, "absent in both runs")
		case !haveB || b.Missing:
			r.Verdict = Appeared
			// An absent baseline value is not zero, and printing it as one is how a new
			// measurement reads as "0 -> 59.5%, improved".
			r.Base = Value{Missing: true, Num: -1, Den: -1}
		case !haveC || c.Missing:
			r.Verdict = Vanished
			r.Cur = Value{Missing: true, Num: -1, Den: -1}
			r.Notes = append(r.Notes, "the baseline had a verdict here and this run does not; "+
				"an abstention scored as a pass is how T11/T12 reported a clean 0.0% for a round")
		case m.Dir == Info:
			r.Verdict = Moot
			// An informational row still reports a BREACH of its own absolute invariant, so
			// making prompt geometry informational cannot hide a prompt over budget or a window
			// under the floor — see the note on those metrics in sweep.go.
		default:
			r.Verdict = judge(m, b, c)
		}
		r.Notes = append(r.Notes, notesFor(m, b, c, haveB, haveC, r.Verdict)...)
		out = append(out, r)
	}
	return abstentionNotes(base, cur, out)
}

// judge is the movement test for a judged metric.
//
// A rate is judged on its rate AND on the count behind it, because those two answer different
// questions and this study has published one number of each kind without being able to tell
// them apart. When the denominator held still the two agree by construction. When it moved,
// the count is the more direct evidence about behaviour and decides how the rate movement is
// classified — see Artifact and Unattributable.
func judge(m Metric, b, c Value) Verdict {
	v := move(m, primary(b), primary(c))
	if v == Unchanged || !b.HasRate || !c.HasRate || b.Den < 0 || c.Den < 0 || b.Den == c.Den {
		return v
	}
	if b.Num < 0 || c.Num < 0 {
		// The count could not be recovered from the printed percentage, so nothing here can
		// separate behaviour from sample size. Stay with the rate verdict, which keeps an
		// adverse move loud; notesFor says why it cannot be attributed.
		return v
	}
	switch cv := move(m, float64(b.Num), float64(c.Num)); {
	case cv == Unchanged:
		return Artifact
	case cv == v:
		return v
	default:
		return Unattributable
	}
}

// move is the direction test, shared by the rate and the count so they cannot drift apart.
func move(m Metric, from, to float64) Verdict {
	if math.Abs(to-from) <= epsilon {
		return Unchanged
	}
	better := to > from
	if m.Dir == Lower {
		better = to < from
	}
	if better {
		return Improved
	}
	return Regressed
}

// abstentionNotes attaches T11's and T12's abstention counts to the rows they damage.
//
// A metric that stops returning a verdict altogether is already loud (Vanished). A metric that
// keeps returning one over half as many judged cases looked exactly like a healthy run, and
// that is the shape both of this study's published false 0.0%s actually had.
func abstentionNotes(base, cur *Sweep, rows []Row) []Row {
	for i := range rows {
		var key string
		switch rows[i].Metric.Key {
		case "t11_synopsis_lags":
			key = "t11_abstained"
		case "t12_beat_vs_record":
			key = "t12_abstained"
		default:
			continue
		}
		b, okB := base.Values[key]
		c, okC := cur.Values[key]
		if !okB || !okC || b.Num < 0 || c.Num < 0 || c.Num <= b.Num {
			continue
		}
		rows[i].Notes = append(rows[i].Notes, fmt.Sprintf("ABSTENTIONS ROSE %d -> %d: this rate is "+
			"judged on fewer cases than the baseline's, which is a weaker instrument rather than a "+
			"better result", b.Num, c.Num))
	}
	return rows
}

// primary is the quantity a metric is judged on: its rate when it has one, its count
// otherwise.
func primary(v Value) float64 {
	if v.HasRate {
		return v.Rate
	}
	return float64(v.Num)
}

// notesFor assembles everything a verdict on its own would hide.
func notesFor(m Metric, b, c Value, haveB, haveC bool, v Verdict) []string {
	var notes []string
	if !haveB || !haveC || b.Missing || c.Missing {
		return notes
	}
	// The denominator, always — not only when it moved. "T12 25.0% of 40" against
	// "15.7% of 70" is the case this gate was written for, and the artifact that reported
	// it did not say which of the two numbers had changed.
	movedDen := b.Den >= 0 && c.Den >= 0 && b.Den != c.Den
	haveCounts := b.HasRate && b.Num >= 0 && c.Num >= 0
	if movedDen {
		note := fmt.Sprintf("DENOMINATOR MOVED %d -> %d", b.Den, c.Den)
		// The caveat belongs on the cases the count does NOT corroborate, in both
		// directions. Applied to every moved denominator it fired on the genuine T4 gain as
		// loudly as on a pure artifact, which is the same skimming failure as a gate that
		// calls arithmetic a regression.
		switch {
		case !haveCounts:
			note += " — and the count is not recoverable, so this movement cannot be " +
				"separated from the denominator change"
		case v == Artifact || v == Unattributable:
			note += " — this movement is NOT attributable to behaviour until the denominator " +
				"change is explained"
		case materialDenMove(b.Den, c.Den):
			note += fmt.Sprintf(" — a %+.0f%% change of population; the count agrees with the "+
				"rate here, but over a sample that size this is NOT attributable to behaviour "+
				"until the denominator change is explained",
				(float64(c.Den)-float64(b.Den))/float64(b.Den)*100)
		}
		notes = append(notes, note)
	}
	// The absolute count beside the rate. A rate can fall while the number of flagged
	// items rises, and vice versa; both are facts about the run.
	switch {
	case haveCounts && b.Num != c.Num:
		note := fmt.Sprintf("count %d -> %d", b.Num, c.Num)
		if movedDen && (v == Improved || v == Regressed) {
			note += fmt.Sprintf(" — the count moved the SAME way as the rate (%s), so this is a "+
				"behavioural move and not a denominator artifact", v)
		}
		notes = append(notes, note)
	case haveCounts && movedDen:
		notes = append(notes, fmt.Sprintf("count is UNCHANGED at %d — the same items over a "+
			"different population", b.Num))
	case b.HasRate && !haveCounts:
		notes = append(notes, "count not recoverable from the printed percentage at this denominator")
	}
	// Whether the metric is passing at all. A regression from failing to more-failing and a
	// regression that breaks a previously-passing threshold are different sizes of problem.
	if s := thresholdState(m, c); s != "" {
		notes = append(notes, s)
	}
	return notes
}

// materialDenMoveFrac is how much a population can change before a rate over it counts as a
// different measurement rather than the same one moving.
//
// A tenth is a judgement, and it is drawn from the two real cases on this branch rather than
// picked: T4's denominator moved 79 -> 81 (+2.5%) while the retained count moved 46 -> 55, and
// calling that unattributable would have buried the only movement the study's headline failure
// has ever shown. T12's moved 70 -> 40 (-43%) with the count all but still, and calling THAT a
// result is the mistake the gate was built for. Anything under a tenth is the first shape.
const materialDenMoveFrac = 0.10

func materialDenMove(from, to int) bool {
	if from <= 0 {
		return to != 0
	}
	return math.Abs(float64(to)-float64(from))/float64(from) > materialDenMoveFrac
}

// thresholdState reports whether the current value satisfies the metric's own threshold.
func thresholdState(m Metric, c Value) string {
	val := primary(c)
	switch {
	case !math.IsNaN(m.WantMax) && val > m.WantMax:
		return fmt.Sprintf("still FAILING its own threshold (want %s)", m.Want)
	case !math.IsNaN(m.WantMin) && val < m.WantMin:
		return fmt.Sprintf("still FAILING its own threshold (want %s)", m.Want)
	}
	return ""
}

// Result is the whole comparison, both arms.
type Result struct {
	Rows map[string][]Row // arm -> rows
}

// CompareBoth judges both arms against the baseline. An arm the caller did not supply is
// omitted rather than passed silently: half an ablation is not a measurement, and the
// summary says which arms it covered.
func CompareBoth(base *Baseline, on, off *Sweep) *Result {
	res := &Result{Rows: map[string][]Row{}}
	if on != nil && base.On != nil {
		res.Rows["on"] = Compare(base.On, on)
	}
	if off != nil && base.Off != nil {
		res.Rows["off"] = Compare(base.Off, off)
	}
	return res
}

// Regressions lists every regressed or lost metric, arm-qualified, in report order.
//
// This is the REVERT CLASS and nothing else: a behavioural move in the worse direction, or a
// verdict the baseline had and this run does not. A rate that moved only because its
// denominator did is reported by NotAttributable instead — the revert rule exists to stop
// behaviour accumulating downhill, and applying it to arithmetic both wastes a revert and
// teaches the reader to skim the loud block.
func (r *Result) Regressions() []string {
	return r.rowsWhere(Regressed, Vanished)
}

// NotAttributable lists the rows whose movement the log cannot assign to behaviour: rate
// artifacts (the count stood still) and unattributable moves (the count went the other way).
// Loud, printed, and deliberately not grounds for a revert.
func (r *Result) NotAttributable() []string {
	return r.rowsWhere(Artifact, Unattributable)
}

func (r *Result) rowsWhere(want ...Verdict) []string {
	var out []string
	for _, arm := range r.arms() {
		for _, row := range r.Rows[arm] {
			match := false
			for _, w := range want {
				match = match || row.Verdict == w
			}
			if !match {
				continue
			}
			out = append(out, fmt.Sprintf("[%s] %s: %s -> %s (%s)%s",
				arm, row.Metric.Label, show(row.Base), show(row.Cur), row.Verdict,
				noteSuffix(row.Notes)))
		}
	}
	return out
}

func (r *Result) arms() []string {
	arms := make([]string, 0, len(r.Rows))
	for a := range r.Rows {
		arms = append(arms, a)
	}
	sort.Strings(arms)
	return arms
}

func noteSuffix(notes []string) string {
	if len(notes) == 0 {
		return ""
	}
	return " — " + strings.Join(notes, "; ")
}

// show renders a value the way the sweep prints it.
func show(v Value) string {
	switch {
	case v.Missing:
		return "n/a"
	case v.HasRate && v.Den >= 0:
		return fmt.Sprintf("%.1f%% of %d", v.Rate, v.Den)
	case v.HasRate:
		return fmt.Sprintf("%.1f%%", v.Rate)
	default:
		return fmt.Sprintf("%d", v.Num)
	}
}

// Render writes the comparison as a table plus a verdict, loud side up.
//
// The regressions come FIRST, before the table, because the failure mode this gate exists to
// prevent is a regression noticed by the next reviewer rather than by the person making the
// change — and a reader who has to scan 28 rows to find out whether anything broke will stop
// scanning.
func (r *Result) Render() string {
	var b strings.Builder
	regs := r.Regressions()
	b.WriteString("════════════════════════════════════════════════════════════════════\n")
	if len(regs) == 0 {
		b.WriteString("GATE: PASS — no threshold regressed in any arm compared.\n")
	} else {
		fmt.Fprintf(&b, "GATE: REGRESSED — %d regression(s). Revert the change or justify each\n"+
			"      one explicitly (a measurement becoming more honest is the only case that\n"+
			"      is not a behavioural loss, and it has to be shown, not asserted).\n", len(regs))
		for _, s := range regs {
			b.WriteString("  ✗ " + s + "\n")
		}
	}
	if na := r.NotAttributable(); len(na) > 0 {
		fmt.Fprintf(&b, "\n%d row(s) moved WITHOUT a behavioural signal — NOT the revert class,\n"+
			"      and not to be reverted for: the flagged count either stood still or moved the\n"+
			"      other way while the denominator changed. Explain the denominator, don't revert.\n",
			len(na))
		for _, s := range na {
			b.WriteString("  ~ " + s + "\n")
		}
	}
	b.WriteString("════════════════════════════════════════════════════════════════════\n")
	for _, arm := range r.arms() {
		fmt.Fprintf(&b, "\nARM %s\n", strings.ToUpper(arm))
		fmt.Fprintf(&b, "%-40s %-18s %-18s %-9s %s\n", "metric", "baseline", "now", "verdict", "want")
		for _, row := range r.Rows[arm] {
			fmt.Fprintf(&b, "%-40s %-18s %-18s %-9s %s\n", row.Metric.Label,
				show(row.Base), show(row.Cur), row.Verdict, row.Metric.Want)
			for _, n := range row.Notes {
				if n == "absent in both runs" {
					continue
				}
				b.WriteString("      · " + n + "\n")
			}
		}
	}
	return b.String()
}
