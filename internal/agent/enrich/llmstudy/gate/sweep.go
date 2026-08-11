// Package gate turns a sweep's log into numbers and compares them against a committed
// baseline, so a change that fixes one threshold and costs another is impossible to miss.
//
// Why this exists. Every measured configuration on this branch was compared against the
// PREVIOUS report by hand, by the next reviewer, after the change had already landed — and
// four separate regressions were rationalised individually that way: a recency anchor that
// halved synopsis lag while costing 6 points of fact retention and doubling two other
// thresholds; two attempts at prompt length-guidance that each cost a threshold (2 digests
// lost, then T9 from 1 flagged report to 6 of 9); and the beat work, which gained ~8 points
// of retention while silently losing two beats per run and moving T12 from 15.7% to 25.0%.
// Nothing enforced "no net regression", so regressions accumulated.
//
// The sweep (TestDigestRefineQuality, -tags llmstudy) reports through t.Logf, which is the
// right place for it — a reader needs the flagged items beside the rates. So this package
// reads the log rather than asking the sweep to emit a second machine format: one source of
// truth, and every historical log in the SDD directory is comparable without re-running it.
//
// Rates are stored WITH their denominators, and the comparison prints the denominator delta
// on every row, because a rate can improve purely by losing its denominator. That is not
// hypothetical here: T12 moved 15.7% -> 25.0% while its denominator moved 70 -> 40, and no
// artifact on the branch recorded which of the two had happened.
package gate

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Direction says which way is better for a metric.
type Direction int

const (
	// Lower means a smaller value is better (a flag rate, a panic count).
	Lower Direction = iota
	// Higher means a larger value is better (retention, coverage, a window margin).
	Higher
	// Info means the value is reported but never judged — a denominator-like quantity
	// whose movement is context for another row rather than a result of its own.
	Info
)

func (d Direction) String() string {
	switch d {
	case Lower:
		return "lower-better"
	case Higher:
		return "higher-better"
	}
	return "informational"
}

// Value is one measured quantity.
//
// Num is carried alongside Rate deliberately. A rate alone cannot distinguish "the model
// stopped doing the bad thing" from "the population that could exhibit it shrank", and this
// study has already published one number of each kind without being able to tell them apart.
// The sweep prints only the percentage and the denominator, so Num is recovered by inverting
// the printed percentage (see invertRate); when the inversion is not unique it stays -1 and
// the comparison says so rather than guessing.
type Value struct {
	Num  int     `json:"num"`
	Den  int     `json:"den"`
	Rate float64 `json:"rate"`
	// HasRate distinguishes a rate metric from a bare count. A count's Rate is meaningless
	// and must not be compared as though it were a percentage.
	HasRate bool `json:"has_rate"`
	// Missing marks a metric the log did not report at all — a NO VERDICT line, or a
	// measurement that did not exist yet when the baseline was taken. Compared as
	// "absent", never as zero: reading a missing measurement as 0.0% is exactly the
	// false-confidence failure the T11/T12 abstention denominators were corrected for.
	Missing bool `json:"missing,omitempty"`
}

// Sweep is one arm's measured state.
type Sweep struct {
	// Arm is "on" or "off" — whether the recency anchor was offered.
	Arm string `json:"arm"`
	// Label is free text identifying the run (commit, task, log file).
	Label string `json:"label,omitempty"`
	// Sessions/Attempted/Produced/Failed are the run's shape. A rate measured over a
	// different number of sessions is not the same measurement, so the shape travels with
	// the numbers.
	Sessions  int `json:"sessions"`
	Attempted int `json:"attempted"`
	Produced  int `json:"produced"`
	Failed    int `json:"failed"`

	Values map[string]Value `json:"values"`
}

// Baseline is the committed pair of arms.
type Baseline struct {
	// Comment is why this baseline is what it is, for a reader who finds the file first.
	Comment string `json:"comment"`
	// Commit is the tree the baseline was measured on.
	Commit string `json:"commit"`
	// Corpus records what was fed in, since a rate is meaningless without it.
	Corpus string `json:"corpus"`
	On     *Sweep `json:"on"`
	Off    *Sweep `json:"off"`
}

// Metric is one row of the comparison: what it is called, which way is better, and the
// threshold it is judged against.
type Metric struct {
	Key   string
	Label string
	Dir   Direction
	// Want is the threshold as the sweep states it, carried so the comparison can say
	// "still failing" as well as "regressed".
	Want string
	// WantMax/WantMin are the machine form of Want. Only one is set; NaN means no threshold.
	WantMax float64
	WantMin float64
}

// Metrics is the judged panel, in report order.
//
// The T-numbers and their thresholds are the sweep's own (see TestDigestRefineQuality's
// summary block); this table adds only the DIRECTION, which the log states in prose
// ("want <=2%") and a comparison needs mechanically.
//
// beats_lost is derived rather than printed: the sweep reports asked and generated
// separately, and the gap between them is a beat the series never got — silent data loss in
// a user-facing surface, and the defect that went unrecorded for a whole round because no
// single log line named it.
var Metrics = []Metric{
	{Key: "t1_usable", Label: "T1 usable digests", Dir: Higher, Want: "100%", WantMin: 100, WantMax: nan()},
	{Key: "t2_unverified", Label: "T2 unverified identifiers", Dir: Lower, Want: "<=2%", WantMax: 2, WantMin: nan()},
	{Key: "t3_rubberstamped", Label: "T3 rubberstamped", Dir: Lower, Want: "<=10%", WantMax: 10, WantMin: nan()},
	{Key: "t4_retention", Label: "T4 retention to final", Dir: Higher, Want: ">=90%", WantMin: 90, WantMax: nan()},
	{Key: "t4_retention_strong", Label: "  T4 split: identifier-shaped", Dir: Higher, Want: "—", WantMin: nan(), WantMax: nan()},
	{Key: "t4_retention_weak", Label: "  T4 split: bare capitalised", Dir: Higher, Want: "—", WantMin: nan(), WantMax: nan()},
	{Key: "t7_fabricated_unresolved", Label: "T7 fabricated blockers", Dir: Lower, Want: "<=10%", WantMax: 10, WantMin: nan()},
	{Key: "t8_stale_open_items", Label: "T8 stale open items", Dir: Lower, Want: "<=2%", WantMax: 2, WantMin: nan()},
	{Key: "t9_current_is_done", Label: "T9 current-is-completed", Dir: Lower, Want: "<=5%", WantMax: 5, WantMin: nan()},
	{Key: "t10_synopsis_restates", Label: "T10 synopsis restates", Dir: Lower, Want: "<=5%", WantMax: 5, WantMin: nan()},
	{Key: "t11_synopsis_lags", Label: "T11 synopsis lags", Dir: Lower, Want: "<=10%", WantMax: 10, WantMin: nan()},
	{Key: "t12_beat_vs_record", Label: "T12 beat-vs-record", Dir: Lower, Want: "<=5%", WantMax: 5, WantMin: nan()},
	{Key: "t13_fabricated_next", Label: "T13 fabricated next", Dir: Lower, Want: "<=5%", WantMax: 5, WantMin: nan()},
	{Key: "leaks", Label: "instruction leakage", Dir: Lower, Want: "0", WantMax: 0, WantMin: nan()},
	{Key: "panics", Label: "recovered panics", Dir: Lower, Want: "0", WantMax: 0, WantMin: nan()},
	{Key: "beats_lost", Label: "beats LOST (asked but never generated)", Dir: Lower, Want: "0", WantMax: 0, WantMin: nan()},
	{Key: "beat_errors", Label: "beat generation errors", Dir: Lower, Want: "0", WantMax: 0, WantMin: nan()},
	{Key: "beats_asked", Label: "beats asked", Dir: Info, Want: "—", WantMin: nan(), WantMax: nan()},
	{Key: "beats_generated", Label: "beats generated", Dir: Info, Want: "—", WantMin: nan(), WantMax: nan()},
	{Key: "beats_kept", Label: "beats kept", Dir: Info, Want: "—", WantMin: nan(), WantMax: nan()},
	{Key: "beats_discarded", Label: "beats discarded as restatement", Dir: Info, Want: "—", WantMin: nan(), WantMax: nan()},
	{Key: "beats_subject_changed", Label: "beats marked subject-changed", Dir: Info, Want: "—", WantMin: nan(), WantMax: nan()},
	{Key: "beat_turn_coverage", Label: "beat window turn coverage", Dir: Higher, Want: "100%", WantMin: 100, WantMax: nan()},
	{Key: "beat_window_overlap", Label: "consecutive beat window overlap", Dir: Info, Want: "~25-30%", WantMin: nan(), WantMax: nan()},
	{Key: "window_margin_refine", Label: "tightest refine window margin", Dir: Higher, Want: ">=0", WantMin: 0, WantMax: nan()},
	{Key: "window_margin_create", Label: "tightest create window margin", Dir: Higher, Want: ">=0", WantMin: 0, WantMax: nan()},
	{Key: "largest_prompt", Label: "largest prompt (runes)", Dir: Lower, Want: "<=14000", WantMax: 14000, WantMin: nan()},
	{Key: "retain_evicted", Label: "retain-list entries evicted by a cap", Dir: Info, Want: "—", WantMin: nan(), WantMax: nan()},
}

func nan() float64 { return math.NaN() }

// metricByKey indexes Metrics for lookup without rebuilding the slice.
func metricByKey(key string) (Metric, bool) {
	for _, m := range Metrics {
		if m.Key == key {
			return m, true
		}
	}
	return Metric{}, false
}

// The log-line patterns. One per measured line, anchored on the literal label the sweep
// writes, so a reworded log line fails to parse LOUDLY (Parse returns an error naming the
// metric it could not find) rather than silently reporting a metric as absent — absent and
// unparsed are different problems and only one of them is a measurement.
var (
	reArm       = regexp.MustCompile(`ARM: anchor (ON|OFF)`)
	reShape     = regexp.MustCompile(`sessions=(\d+) attempted=(\d+) produced=(\d+) failed=(\d+)`)
	reT1        = regexp.MustCompile(`T1 usable digests\s+([\d.]+)% of (\d+) attempts`)
	reT2        = regexp.MustCompile(`T2 unverified identifiers\s+([\d.]+)% of (\d+)`)
	reT3        = regexp.MustCompile(`T3 rubberstamped\s+([\d.]+)% of (\d+)`)
	reT4        = regexp.MustCompile(`T4 retention to final\s+([\d.]+)% of (\d+)`)
	reT4Split   = regexp.MustCompile(`split: identifier-shaped specifics ([\d.]+)% of (\d+); bare capitalised words ([\d.]+)% of (\d+)`)
	reT7        = regexp.MustCompile(`T7 fabricated unresolved\s+([\d.]+)% of (\d+)`)
	reT8        = regexp.MustCompile(`T8 stale open items\s+([\d.]+)% of (\d+)`)
	reT9        = regexp.MustCompile(`T9 current-is-completed\s+([\d.]+)% of (\d+)`)
	reT10       = regexp.MustCompile(`T10 synopsis restates\s+([\d.]+)% of (\d+)`)
	reT11       = regexp.MustCompile(`T11 synopsis lags\s+([\d.]+)% of (\d+) JUDGED`)
	reT11None   = regexp.MustCompile(`T11 synopsis lags\s+NO VERDICT`)
	reT12       = regexp.MustCompile(`T12 beat-vs-record\s+([\d.]+)% of (\d+) CHECKED`)
	reT12None   = regexp.MustCompile(`T12 beat-vs-record\s+NO VERDICT`)
	reT13       = regexp.MustCompile(`T13 fabricated next\s+([\d.]+)% of (\d+)`)
	reLeaks     = regexp.MustCompile(`prompt leaks (\d+); sentinel used (\d+)/(\d+)`)
	rePanics    = regexp.MustCompile(`RECOVERED PANICS\s+(\d+) total`)
	reBeats     = regexp.MustCompile(`BEATS\s+asked (\d+), generated (\d+), kept (\d+), discarded as restatement (\d+) \([\d.]+% of generated\), errors (\d+)`)
	reChanged   = regexp.MustCompile(`of the kept beats, (\d+) changed the subject`)
	reCoverage  = regexp.MustCompile(`BEAT WINDOWS\s+turn coverage ([\d.]+)% of (\d+) turns`)
	reOverlap   = regexp.MustCompile(`consecutive-window overlap: mean ([\d.]+)%`)
	rePrompt    = regexp.MustCompile(`PROMPT\s+largest (\d+) runes of (\d+) budget`)
	reMargins   = regexp.MustCompile(`tightest window margin over the \d+-rune floor: refine (.*), create (.*)$`)
	reMarginNum = regexp.MustCompile(`^([+-]?\d+) `)
	reRetain    = regexp.MustCompile(`RETAIN-LIST\s+offered (\d+), evicted by the cap (\d+)`)
)

// ParseFile reads a sweep log.
func ParseFile(path string) (*Sweep, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s.Label = path
	return s, nil
}

// Parse reads a sweep log into a Sweep.
//
// A log line the sweep did not write leaves its metric Missing; a REQUIRED line (the arm, the
// run shape, and every threshold the sweep always prints) missing is an error, because a
// truncated or crashed run must not be comparable to a complete one. That distinction is the
// whole reason this returns an error at all: a half-finished log parsed into a Sweep with a
// handful of zeroes would compare as a sweeping improvement.
func Parse(r io.Reader) (*Sweep, error) {
	s := &Sweep{Values: map[string]Value{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<22) // a sweep log line can carry a whole beat
	for sc.Scan() {
		line := sc.Text()
		switch {
		case reArm.MatchString(line):
			s.Arm = strings.ToLower(reArm.FindStringSubmatch(line)[1])
		case reShape.MatchString(line):
			m := reShape.FindStringSubmatch(line)
			s.Sessions, s.Attempted, s.Produced, s.Failed = atoi(m[1]), atoi(m[2]), atoi(m[3]), atoi(m[4])
		case reT4Split.MatchString(line):
			m := reT4Split.FindStringSubmatch(line)
			s.Values["t4_retention_strong"] = rate(m[1], m[2])
			s.Values["t4_retention_weak"] = rate(m[3], m[4])
		case reLeaks.MatchString(line):
			m := reLeaks.FindStringSubmatch(line)
			s.Values["leaks"] = count(atoi(m[1]))
		case rePanics.MatchString(line):
			s.Values["panics"] = count(atoi(rePanics.FindStringSubmatch(line)[1]))
		case reBeats.MatchString(line):
			m := reBeats.FindStringSubmatch(line)
			asked, gen := atoi(m[1]), atoi(m[2])
			s.Values["beats_asked"] = count(asked)
			s.Values["beats_generated"] = count(gen)
			s.Values["beats_kept"] = count(atoi(m[3]))
			s.Values["beats_discarded"] = count(atoi(m[4]))
			s.Values["beat_errors"] = count(atoi(m[5]))
			// Derived, and the reason this gate exists: "42 asked / 40 generated" is two
			// numbers nobody subtracted for a whole round, so a 5% loss of the history the
			// design leans on was recorded only in a concerns list.
			s.Values["beats_lost"] = count(asked - gen)
		case reChanged.MatchString(line):
			s.Values["beats_subject_changed"] = count(atoi(reChanged.FindStringSubmatch(line)[1]))
		case reCoverage.MatchString(line):
			m := reCoverage.FindStringSubmatch(line)
			s.Values["beat_turn_coverage"] = rate(m[1], m[2])
		case reOverlap.MatchString(line):
			s.Values["beat_window_overlap"] = Value{Rate: atof(reOverlap.FindStringSubmatch(line)[1]), HasRate: true, Num: -1}
		case rePrompt.MatchString(line):
			s.Values["largest_prompt"] = count(atoi(rePrompt.FindStringSubmatch(line)[1]))
		case reMargins.MatchString(line):
			m := reMargins.FindStringSubmatch(line)
			s.Values["window_margin_refine"] = margin(m[1])
			s.Values["window_margin_create"] = margin(m[2])
		case reRetain.MatchString(line):
			s.Values["retain_evicted"] = count(atoi(reRetain.FindStringSubmatch(line)[2]))
		case reT11None.MatchString(line):
			s.Values["t11_synopsis_lags"] = Value{Missing: true, Num: -1}
		case reT12None.MatchString(line):
			s.Values["t12_beat_vs_record"] = Value{Missing: true, Num: -1}
		default:
			for _, p := range []struct {
				re  *regexp.Regexp
				key string
			}{
				{reT1, "t1_usable"}, {reT2, "t2_unverified"}, {reT3, "t3_rubberstamped"},
				{reT4, "t4_retention"}, {reT7, "t7_fabricated_unresolved"},
				{reT8, "t8_stale_open_items"}, {reT9, "t9_current_is_done"},
				{reT10, "t10_synopsis_restates"}, {reT11, "t11_synopsis_lags"},
				{reT12, "t12_beat_vs_record"}, {reT13, "t13_fabricated_next"},
			} {
				if m := p.re.FindStringSubmatch(line); m != nil {
					s.Values[p.key] = rate(m[1], m[2])
					break
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if s.Arm == "" {
		return nil, fmt.Errorf("no ARM line: this is not a completed sweep log")
	}
	// Every threshold the sweep prints unconditionally. T11/T12 are excluded because a
	// NO VERDICT run legitimately prints neither a rate nor a denominator, and coverage/
	// overlap because they postdate the baseline.
	for _, k := range []string{
		"t1_usable", "t2_unverified", "t3_rubberstamped", "t4_retention",
		"t7_fabricated_unresolved", "t8_stale_open_items", "t9_current_is_done",
		"t10_synopsis_restates", "t13_fabricated_next", "leaks", "panics",
		"beats_asked", "largest_prompt",
	} {
		if _, ok := s.Values[k]; !ok {
			return nil, fmt.Errorf("log is missing %s: an incomplete run is not comparable", k)
		}
	}
	return s, nil
}

// rate builds a Value from a printed percentage and denominator, recovering the numerator.
func rate(pct, den string) Value {
	d := atoi(den)
	return Value{Num: invertRate(pct, d), Den: d, Rate: atof(pct), HasRate: true}
}

func count(n int) Value { return Value{Num: n, Den: -1, Rate: 0, HasRate: false} }

// margin parses a window-margin field, which is either "+20 (s1 i8 signal)" or the prose
// "never clipped on any step". Never-clipped is NOT a margin and must not be compared as one
// — reporting it as a number is what would make an unclipped corpus look safer than a
// measured one, which marginReport's own doc in the sweep says explicitly. It is recorded as
// Missing so the comparison prints "n/a" instead of inventing a headroom figure.
func margin(field string) Value {
	if m := reMarginNum.FindStringSubmatch(strings.TrimSpace(field)); m != nil {
		return Value{Num: atoi(m[1]), Den: -1}
	}
	return Value{Missing: true, Num: -1}
}

// invertRate recovers the numerator behind a printed one-decimal percentage.
//
// The sweep prints "0.9% of 762" and not the count, and the count is what distinguishes a
// behavioural change from a denominator change. At these denominators (the largest on this
// corpus is ~800) one decimal place determines the integer uniquely, so the inversion is
// exact rather than an estimate — but it is CHECKED rather than assumed: when more than one
// integer formats to the same percentage the result is -1, and the comparison then declines
// to report a count delta instead of picking one.
func invertRate(pct string, den int) int {
	if den <= 0 {
		return 0
	}
	found, hits := -1, 0
	for n := 0; n <= den; n++ {
		if fmt.Sprintf("%.1f", float64(n)/float64(den)*100) == pct {
			found, hits = n, hits+1
			if hits > 1 {
				return -1
			}
		}
	}
	if hits != 1 {
		return -1
	}
	return found
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(s, "+"))
	return n
}

func atof(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
