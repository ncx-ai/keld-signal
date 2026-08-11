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
		case !haveC || c.Missing:
			r.Verdict = Vanished
			r.Notes = append(r.Notes, "the baseline had a verdict here and this run does not; "+
				"an abstention scored as a pass is how T11/T12 reported a clean 0.0% for a round")
		case m.Dir == Info:
			r.Verdict = Moot
		default:
			r.Verdict = judge(m, b, c)
		}
		r.Notes = append(r.Notes, notesFor(m, b, c, haveB, haveC, r.Verdict)...)
		out = append(out, r)
	}
	return out
}

// judge is the movement test for a judged metric.
func judge(m Metric, b, c Value) Verdict {
	bv, cv := primary(b), primary(c)
	if math.Abs(cv-bv) <= epsilon {
		return Unchanged
	}
	better := cv > bv
	if m.Dir == Lower {
		better = cv < bv
	}
	if better {
		return Improved
	}
	return Regressed
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
	if b.Den >= 0 && c.Den >= 0 && b.Den != c.Den {
		note := fmt.Sprintf("DENOMINATOR MOVED %d -> %d", b.Den, c.Den)
		if v == Improved {
			note += " — this rate improvement is NOT attributable to behaviour until the " +
				"denominator change is explained"
		}
		notes = append(notes, note)
	}
	// The absolute count beside the rate. A rate can fall while the number of flagged
	// items rises, and vice versa; both are facts about the run.
	if b.HasRate && b.Num >= 0 && c.Num >= 0 && b.Num != c.Num {
		notes = append(notes, fmt.Sprintf("count %d -> %d", b.Num, c.Num))
	}
	if b.HasRate && (b.Num < 0 || c.Num < 0) {
		notes = append(notes, "count not recoverable from the printed percentage at this denominator")
	}
	// Whether the metric is passing at all. A regression from failing to more-failing and a
	// regression that breaks a previously-passing threshold are different sizes of problem.
	if s := thresholdState(m, c); s != "" {
		notes = append(notes, s)
	}
	return notes
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
func (r *Result) Regressions() []string {
	var out []string
	for _, arm := range r.arms() {
		for _, row := range r.Rows[arm] {
			if row.Verdict != Regressed && row.Verdict != Vanished {
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
