package gate

import (
	"math"
	"strings"
	"testing"
)

// fixture is a minimal but REAL-SHAPED sweep summary — every line copied from an actual run's
// log, with the figures replaced. Hand-built rather than a committed 200 KB log because what
// is being tested is the parse of the summary block, and a fixture a reader can see in one
// screen is the only way a "the regex still matches the log" test is honest about what it
// matched.
const fixture = `=== RUN   TestDigestRefineQuality
    digest_eval_test.go:835: ARM: anchor ON (SubjectShifted stand-in — known to fire)
    digest_eval_test.go:836: sessions=14 attempted=56 produced=56 failed=0
    digest_eval_test.go:840: T1 usable digests        100.0% of 56 attempts  (want 100%)
    digest_eval_test.go:841: T2 unverified identifiers 0.9% of 762  (want <=2%)
    digest_eval_test.go:842: T3 rubberstamped          0.0% of 12 correction-bearing  (want <=10%)
    digest_eval_test.go:843: T4 retention to final     58.2% of 79  (want >=90%)
    digest_eval_test.go:844:     split: identifier-shaped specifics 47.6% of 42; bare capitalised words 70.3% of 37 — only the first
    digest_eval_test.go:847: T7 fabricated unresolved  4.5% of 44 clean runs  (want <=10%)
    digest_eval_test.go:848: T8 stale open items       0.0% of 74 open items  (want <=2%)
    digest_eval_test.go:849: T9 current-is-completed   1.8% of 56  (want <=5%)
    digest_eval_test.go:850: T10 synopsis restates      0.0% of 56  (want <=5%)
    digest_eval_test.go:856: T11 synopsis lags          0.0% of 30 JUDGED refinements  (want <=10%; 5 of 35 abstained)
    digest_eval_test.go:882: T12 beat-vs-record         25.0% of 40 CHECKED beats  (want <=5%; 0 abstained on a thin record, 40 generated in total)
    digest_eval_test.go:886: T13 fabricated next        3.3% of 90 next-only identifiers  (want <=5%)
    digest_eval_test.go:887:    prompt leaks 0; sentinel used 8/56
    digest_eval_test.go:893: RECOVERED PANICS          0 total (create 0, refine 0, beat 0)  — the bar is zero
    digest_eval_test.go:904: BEATS  asked 42, generated 40, kept 40, discarded as restatement 0 (0.0% of generated), errors 2, cadence every 5 user turns
    digest_eval_test.go:908:    of the kept beats, 36 changed the subject (90.0%)
    digest_eval_test.go:928: RETAIN-LIST  offered 164, evicted by the cap 0, already gone from the prior report 73 (of 237 pairs)
    digest_eval_test.go:954: PROMPT  largest 13929 runes of 14000 budget (refine s1 i8 signal)
    digest_eval_test.go:955:    tightest window margin over the 1600-rune floor: refine +0 (s1 i8 signal), create never clipped on any step (no conversation exceeded its room)
--- PASS: TestDigestRefineQuality (900.00s)
`

func parseFixture(t *testing.T, log string) *Sweep {
	t.Helper()
	s, err := Parse(strings.NewReader(log))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return s
}

// TestParseReadsTheWholeSummary pins that every judged metric is actually recovered from a
// real-shaped log. Its value is as a canary: the sweep writes these lines with t.Logf, so a
// reworded log line is a silent loss of the gate unless something asserts the pairing.
func TestParseReadsTheWholeSummary(t *testing.T) {
	s := parseFixture(t, fixture)
	if s.Arm != "on" {
		t.Errorf("arm = %q, want on", s.Arm)
	}
	if s.Sessions != 14 || s.Attempted != 56 || s.Produced != 56 || s.Failed != 0 {
		t.Errorf("shape = %d/%d/%d/%d", s.Sessions, s.Attempted, s.Produced, s.Failed)
	}
	for _, tc := range []struct {
		key  string
		rate float64
		den  int
		num  int
	}{
		{"t1_usable", 100.0, 56, 56},
		{"t2_unverified", 0.9, 762, 7},
		{"t3_rubberstamped", 0.0, 12, 0},
		{"t4_retention", 58.2, 79, 46},
		{"t4_retention_strong", 47.6, 42, 20},
		{"t4_retention_weak", 70.3, 37, 26},
		{"t7_fabricated_unresolved", 4.5, 44, 2},
		{"t8_stale_open_items", 0.0, 74, 0},
		{"t9_current_is_done", 1.8, 56, 1},
		{"t10_synopsis_restates", 0.0, 56, 0},
		{"t11_synopsis_lags", 0.0, 30, 0},
		{"t12_beat_vs_record", 25.0, 40, 10},
		{"t13_fabricated_next", 3.3, 90, 3},
	} {
		v, ok := s.Values[tc.key]
		if !ok {
			t.Errorf("%s: not parsed", tc.key)
			continue
		}
		if v.Rate != tc.rate || v.Den != tc.den || v.Num != tc.num {
			t.Errorf("%s = %.1f%% of %d (num %d), want %.1f%% of %d (num %d)",
				tc.key, v.Rate, v.Den, v.Num, tc.rate, tc.den, tc.num)
		}
	}
	for _, tc := range []struct {
		key string
		n   int
	}{
		{"leaks", 0}, {"panics", 0}, {"beats_asked", 42}, {"beats_generated", 40},
		{"beats_kept", 40}, {"beats_discarded", 0}, {"beat_errors", 2},
		{"beats_subject_changed", 36}, {"largest_prompt", 13929}, {"retain_evicted", 0},
		// The number nobody subtracted for a whole round.
		{"beats_lost", 2},
		{"window_margin_refine", 0},
	} {
		if v := s.Values[tc.key]; v.Num != tc.n {
			t.Errorf("%s = %d, want %d", tc.key, v.Num, tc.n)
		}
	}
	// "never clipped" is not a margin. Recording it as a number would report an unclipped
	// corpus as comfortable headroom, which is precisely what marginReport's own doc in the
	// sweep refuses to do.
	if v := s.Values["window_margin_create"]; !v.Missing {
		t.Errorf("create margin = %+v, want Missing for a never-clipped run", v)
	}
}

// TestParseRejectsAnIncompleteLog is the property that makes a PASS meaningful. A sweep that
// died halfway leaves a log with no summary; parsed leniently it would come back as a Sweep
// full of zeroes, and zeroes compare as a sweeping improvement on every lower-better metric.
func TestParseRejectsAnIncompleteLog(t *testing.T) {
	head := fixture[:strings.Index(fixture, "T4 retention")]
	if _, err := Parse(strings.NewReader(head)); err == nil {
		t.Fatal("a truncated log parsed without error; zeroes would read as an improvement")
	}
	if _, err := Parse(strings.NewReader("no arm line here\n")); err == nil {
		t.Fatal("a log with no ARM line parsed without error")
	}
}

// TestNoVerdictIsNotZero pins the distinction the T11 and T12 denominators were corrected for
// once already: an instrument that abstained did not measure 0%.
func TestNoVerdictIsNotZero(t *testing.T) {
	log := strings.ReplaceAll(fixture,
		"T12 beat-vs-record         25.0% of 40 CHECKED beats  (want <=5%; 0 abstained on a thin record, 40 generated in total)",
		"T12 beat-vs-record         NO VERDICT — 40 beats generated, the record never held 3 subjects")
	s := parseFixture(t, log)
	v := s.Values["t12_beat_vs_record"]
	if !v.Missing {
		t.Fatalf("NO VERDICT parsed as %+v, want Missing", v)
	}
	// And losing a verdict the baseline had is LOUD, not silent.
	base := parseFixture(t, fixture)
	rows := Compare(base, s)
	for _, r := range rows {
		if r.Metric.Key != "t12_beat_vs_record" {
			continue
		}
		if r.Verdict != Vanished {
			t.Errorf("verdict = %s, want %s", r.Verdict, Vanished)
		}
	}
}

// TestRegressionIsTheLoudCase covers the gate's actual job on the four movements this branch
// made and rationalised one at a time.
func TestRegressionIsTheLoudCase(t *testing.T) {
	base := parseFixture(t, fixture)
	// T9 1.8% -> 10.7% is the length-guidance experiment; T4 58.2 -> 51.2 came with it.
	cur := parseFixture(t, strings.NewReplacer(
		"T9 current-is-completed   1.8% of 56", "T9 current-is-completed   10.7% of 56",
		"T11 synopsis lags          0.0% of 30", "T11 synopsis lags          0.0% of 30",
	).Replace(fixture))
	rows := Compare(base, cur)
	got := map[string]Verdict{}
	for _, r := range rows {
		got[r.Metric.Key] = r.Verdict
	}
	if got["t9_current_is_done"] != Regressed {
		t.Errorf("T9 verdict = %s, want REGRESSED", got["t9_current_is_done"])
	}
	if got["t1_usable"] != Unchanged {
		t.Errorf("T1 verdict = %s, want unchanged", got["t1_usable"])
	}
	res := &Result{Rows: map[string][]Row{"on": rows}}
	if regs := res.Regressions(); len(regs) != 1 || !strings.Contains(regs[0], "T9") {
		t.Fatalf("regressions = %v, want exactly the T9 row", regs)
	}
	if out := res.Render(); !strings.Contains(out, "GATE: REGRESSED") {
		t.Errorf("render did not lead with the verdict:\n%s", out)
	}
}

// TestDenominatorMoveIsNeverHidden is the T12 case stated as a test. 15.7% of 70 -> 25.0% of
// 40 is a WORSE rate over a smaller sample, and the reverse direction — a rate that improves
// while its denominator collapses — is the one that reads as a win and is not one.
//
// CORRECTION to this test's first form, which asserted plain REGRESSED on the 70 -> 40 case.
// The report that accompanied it had already worked out that "the sample shrank; the behaviour
// did not worsen" — 11 flagged beats became 10 — so the gate and the prose disagreed and the
// gate was wrong. A rate that worsens while the COUNT improves is not a behavioural regression
// and must not be one of the rows that triggers the revert rule; it is UNATTRIBUTABLE, which is
// loud in its own right and demands the denominator be explained.
func TestDenominatorMoveIsNeverHidden(t *testing.T) {
	base := parseFixture(t, strings.Replace(fixture,
		"T12 beat-vs-record         25.0% of 40 CHECKED",
		"T12 beat-vs-record         15.7% of 70 CHECKED", 1))
	cur := parseFixture(t, fixture)
	for _, r := range Compare(base, cur) {
		if r.Metric.Key != "t12_beat_vs_record" {
			continue
		}
		if r.Verdict != Unattributable {
			t.Errorf("verdict = %s, want %s", r.Verdict, Unattributable)
		}
		joined := strings.Join(r.Notes, " | ")
		if !strings.Contains(joined, "DENOMINATOR MOVED 70 -> 40") {
			t.Errorf("notes did not surface the denominator move: %s", joined)
		}
		if !strings.Contains(joined, "count 11 -> 10") {
			t.Errorf("notes did not surface the count, which FELL while the rate rose: %s", joined)
		}
	}
	// Now the direction that flatters: fewer flags AND a collapsed denominator.
	shrunk := parseFixture(t, strings.Replace(fixture,
		"T12 beat-vs-record         25.0% of 40 CHECKED",
		"T12 beat-vs-record         10.0% of 10 CHECKED", 1))
	for _, r := range Compare(cur, shrunk) {
		if r.Metric.Key != "t12_beat_vs_record" {
			continue
		}
		if r.Verdict != Improved {
			t.Fatalf("verdict = %s, want improved", r.Verdict)
		}
		if joined := strings.Join(r.Notes, " | "); !strings.Contains(joined, "NOT attributable") {
			t.Errorf("an improvement over a collapsed denominator was reported as a plain win: %s", joined)
		}
	}
}

// TestSameCountOverAMovedDenominatorIsNotARegression is the noise floor, and the reason the
// first five gated steps on this branch all reported REGRESSED.
//
// "T13 3.3% of 90 -> 3.4% of 88" is the SAME THREE flagged items measured over two fewer
// candidates. Reported as a regression it costs the reader the same attention as T1 losing a
// digest, and a gate whose loud rows are mostly arithmetic is a gate that gets skimmed. So a
// rate that moves adversely while its count stands still, across a moved denominator, is a
// rate ARTIFACT: printed with both numbers, excluded from the revert class.
func TestSameCountOverAMovedDenominatorIsNotARegression(t *testing.T) {
	base := parseFixture(t, fixture)
	cur := parseFixture(t, strings.Replace(fixture,
		"T13 fabricated next        3.3% of 90", "T13 fabricated next        3.4% of 88", 1))
	var row Row
	for _, r := range Compare(base, cur) {
		if r.Metric.Key == "t13_fabricated_next" {
			row = r
		}
	}
	if row.Verdict != Artifact {
		t.Errorf("verdict = %s, want %s", row.Verdict, Artifact)
	}
	joined := strings.Join(row.Notes, " | ")
	for _, want := range []string{"DENOMINATOR MOVED 90 -> 88", "count is UNCHANGED at 3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes missing %q: %s", want, joined)
		}
	}
	// The revert class must not contain it, and the reader must still see it.
	res := &Result{Rows: map[string][]Row{"on": Compare(base, cur)}}
	if regs := res.Regressions(); len(regs) != 0 {
		t.Errorf("a denominator artifact entered the revert class: %v", regs)
	}
	out := res.Render()
	if !strings.Contains(out, "GATE: PASS") {
		t.Errorf("render did not pass on an artifact-only run:\n%s", out)
	}
	if !strings.Contains(out, "T13") || !strings.Contains(out, "NOT the revert class") {
		t.Errorf("render hid the artifact instead of separating it:\n%s", out)
	}
}

// TestAnImprovementTheCountAgreesWithIsAttributable is the same scepticism pointed at a WIN.
//
// T4 rising from 58.2% of 79 to 67.9% of 81 is the movement this branch's headline failure
// turns on, and the question that decides whether it is real is whether the retained COUNT rose
// too, or whether fewer facts were merely injected. Both are printed, and the caveat is
// attached to exactly one of the two cases.
func TestAnImprovementTheCountAgreesWithIsAttributable(t *testing.T) {
	base := parseFixture(t, fixture)
	real := parseFixture(t, strings.Replace(fixture,
		"T4 retention to final     58.2% of 79", "T4 retention to final     67.9% of 81", 1))
	var row Row
	for _, r := range Compare(base, real) {
		if r.Metric.Key == "t4_retention" {
			row = r
		}
	}
	if row.Verdict != Improved {
		t.Fatalf("verdict = %s, want improved", row.Verdict)
	}
	joined := strings.Join(row.Notes, " | ")
	if !strings.Contains(joined, "count 46 -> 55") {
		t.Errorf("notes did not carry the retained count: %s", joined)
	}
	if !strings.Contains(joined, "the count moved the SAME way") {
		t.Errorf("an improvement the count corroborates was not said to be attributable: %s", joined)
	}
	if strings.Contains(joined, "NOT attributable") {
		t.Errorf("a corroborated improvement was caveated as unattributable: %s", joined)
	}
	// And the shape that is not progress: the same 46 facts retained out of fewer injected.
	flattered := parseFixture(t, strings.Replace(fixture,
		"T4 retention to final     58.2% of 79", "T4 retention to final     71.9% of 64", 1))
	for _, r := range Compare(base, flattered) {
		if r.Metric.Key != "t4_retention" {
			continue
		}
		if r.Verdict != Artifact {
			t.Errorf("a rate that rose only because the denominator fell: verdict %s, want %s",
				r.Verdict, Artifact)
		}
	}
}

// TestAbstentionsTravelWithTheirMetric covers the loss the study has already published twice:
// T11 and T12 each spent a round reporting a reassuring 0.0% that was really an abstention. A
// FULL loss of the verdict is already loud (Vanished). A partial one — the same 0.0% over half
// as many judged cases — looked identical to a healthy run.
func TestAbstentionsTravelWithTheirMetric(t *testing.T) {
	base := parseFixture(t, fixture)
	if v := base.Values["t11_abstained"]; v.Num != 5 {
		t.Errorf("t11_abstained = %d, want 5", v.Num)
	}
	if v := base.Values["t12_abstained"]; v.Num != 0 {
		t.Errorf("t12_abstained = %d, want 0", v.Num)
	}
	cur := parseFixture(t, strings.Replace(fixture,
		"T11 synopsis lags          0.0% of 30 JUDGED refinements  (want <=10%; 5 of 35 abstained)",
		"T11 synopsis lags          0.0% of 18 JUDGED refinements  (want <=10%; 23 of 41 abstained)", 1))
	var row Row
	for _, r := range Compare(base, cur) {
		if r.Metric.Key == "t11_synopsis_lags" {
			row = r
		}
	}
	if joined := strings.Join(row.Notes, " | "); !strings.Contains(joined, "ABSTENTIONS ROSE 5 -> 23") {
		t.Errorf("a metric judged on 12 fewer cases said nothing about it: %s", joined)
	}
}

// TestInvertRateIsExactOrAbsent pins the numerator recovery. The sweep prints only the
// percentage, so the count that distinguishes behaviour from sample size has to be recovered
// — and a guess would be worse than nothing, so ambiguity must return -1.
func TestInvertRateIsExactOrAbsent(t *testing.T) {
	for _, tc := range []struct {
		pct  string
		den  int
		want int
	}{
		{"0.9", 762, 7}, {"0.8", 762, 6}, {"25.0", 40, 10}, {"100.0", 56, 56},
		{"0.0", 74, 0}, {"58.2", 79, 46}, {"1.8", 56, 1},
		// A denominator large enough that one decimal place no longer determines the
		// integer: 0.1% resolution cannot separate adjacent counts past ~1000.
		{"0.1", 100000, -1},
		{"0.0", 0, 0},
	} {
		if got := invertRate(tc.pct, tc.den); got != tc.want {
			t.Errorf("invertRate(%q, %d) = %d, want %d", tc.pct, tc.den, got, tc.want)
		}
	}
}

// TestStillFailingIsDistinctFromRegressed keeps the two sizes of problem apart: T4 at 58.2%
// against a >=90% threshold is failing whether or not it moved, and a reader needs to know
// which of the two a row is telling them.
func TestStillFailingIsDistinctFromRegressed(t *testing.T) {
	base := parseFixture(t, fixture)
	cur := parseFixture(t, fixture)
	for _, r := range Compare(base, cur) {
		joined := strings.Join(r.Notes, " | ")
		switch r.Metric.Key {
		case "t4_retention":
			if r.Verdict != Unchanged || !strings.Contains(joined, "still FAILING") {
				t.Errorf("T4: verdict %s, notes %q — want unchanged AND still failing", r.Verdict, joined)
			}
		case "t12_beat_vs_record":
			if !strings.Contains(joined, "still FAILING") {
				t.Errorf("T12 at 25.0%% against <=5%% should read as still failing: %q", joined)
			}
		case "t1_usable":
			if strings.Contains(joined, "still FAILING") {
				t.Errorf("T1 at 100%% must not read as failing: %q", joined)
			}
		}
	}
}

// TestCommittedBaselineIsUsable is the guard on the file itself: an embedded baseline that
// does not load, or that carries one arm, makes every later gate run vacuous.
func TestCommittedBaselineIsUsable(t *testing.T) {
	b, err := LoadBaseline()
	if err != nil {
		t.Fatal(err)
	}
	for arm, s := range map[string]*Sweep{"on": b.On, "off": b.Off} {
		if s.Arm != arm {
			t.Errorf("baseline %s arm is labelled %q", arm, s.Arm)
		}
		if s.Sessions == 0 {
			t.Errorf("baseline %s reports 0 sessions", arm)
		}
		for _, m := range Metrics {
			if m.Dir == Info {
				continue
			}
			if _, ok := s.Values[m.Key]; !ok && m.Key != "beat_turn_coverage" &&
				m.Key != "beat_window_overlap" {
				t.Errorf("baseline %s has no value for judged metric %s", arm, m.Key)
			}
		}
	}
	if b.Commit == "" || b.Corpus == "" {
		t.Error("a baseline with no commit or corpus recorded cannot be reproduced")
	}
}

// TestEveryMetricHasADirection is a wiring guard: a metric with no direction is reported and
// never judged, which is how a regression hides in a table that looks complete.
func TestEveryMetricHasADirection(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range Metrics {
		if seen[m.Key] {
			t.Errorf("duplicate metric key %s", m.Key)
		}
		seen[m.Key] = true
		if m.Dir != Info && math.IsNaN(m.WantMax) && math.IsNaN(m.WantMin) &&
			m.Key != "t4_retention_strong" && m.Key != "t4_retention_weak" {
			t.Errorf("%s is judged but names no threshold", m.Key)
		}
	}
	if _, ok := metricByKey("t4_retention"); !ok {
		t.Error("metricByKey cannot find a metric that is in the table")
	}
	if _, ok := metricByKey("nonexistent"); ok {
		t.Error("metricByKey invented a metric")
	}
}
