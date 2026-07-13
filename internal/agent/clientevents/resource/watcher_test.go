package resource

import (
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
)

// recordedEvent captures one call to the injected emit func.
type recordedEvent struct {
	code   string
	sev    clientevents.Severity
	fields map[string]any
}

// fakeClock is an advanceable clock for deterministic Poll() tests.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// queueSampler returns a Sampler that pops one Sample per call from samples,
// in order; a call past the end of the queue fails the test immediately so a
// miscounted test (too many Poll() calls) is caught rather than silently
// repeating the last sample.
func queueSampler(t *testing.T, samples []Sample) Sampler {
	t.Helper()
	i := 0
	return func() Sample {
		if i >= len(samples) {
			t.Fatalf("sampler queue exhausted after %d calls", i)
		}
		s := samples[i]
		i++
		return s
	}
}

func testThresholds() Thresholds {
	return Thresholds{
		RSSMB:           1000,
		CPUPct:          80,
		SustainedWindow: 10 * time.Second,
		GaugeInterval:   30 * time.Second,
	}
}

// newTestWatcher wires a Watcher with recording emit/emitGauge doubles and
// returns it alongside the slices they append to.
func newTestWatcher(sampler Sampler, clock func() time.Time, th Thresholds) (*Watcher, *[]recordedEvent, *[]map[string]any) {
	var events []recordedEvent
	var gauges []map[string]any
	emit := func(code string, sev clientevents.Severity, fields map[string]any) {
		events = append(events, recordedEvent{code: code, sev: sev, fields: fields})
	}
	emitGauge := func(fields map[string]any) {
		gauges = append(gauges, fields)
	}
	w := NewWatcher(emit, emitGauge, th, sampler, clock)
	return w, &events, &gauges
}

func anomalyEvents(events []recordedEvent, code string) []recordedEvent {
	var out []recordedEvent
	for _, e := range events {
		if e.code == code {
			out = append(out, e)
		}
	}
	return out
}

func TestPollBelowThresholdNoAnomaly(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)}
	samples := []Sample{
		{RSSMB: 500, CPUPct: 10},
		{RSSMB: 500, CPUPct: 10},
		{RSSMB: 500, CPUPct: 10},
	}
	w, events, _ := newTestWatcher(queueSampler(t, samples), clock.now, testThresholds())

	for i := 0; i < len(samples); i++ {
		w.Poll()
		clock.advance(1 * time.Second)
	}

	if len(anomalyEvents(*events, "resource.sustained_high_rss")) != 0 {
		t.Fatalf("expected no RSS anomaly events, got %+v", *events)
	}
	if len(anomalyEvents(*events, "resource.sustained_high_cpu")) != 0 {
		t.Fatalf("expected no CPU anomaly events, got %+v", *events)
	}
}

func TestPollAboveThresholdBelowWindowNoAnomalyYet(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)}
	samples := []Sample{
		{RSSMB: 1500, CPUPct: 10},
		{RSSMB: 1500, CPUPct: 10},
		{RSSMB: 1500, CPUPct: 10},
	}
	w, events, _ := newTestWatcher(queueSampler(t, samples), clock.now, testThresholds())

	// Elevated for 0s, 3s, 6s -- all below the 10s SustainedWindow.
	w.Poll()
	clock.advance(3 * time.Second)
	w.Poll()
	clock.advance(3 * time.Second)
	w.Poll()

	if got := anomalyEvents(*events, "resource.sustained_high_rss"); len(got) != 0 {
		t.Fatalf("expected no RSS anomaly before window elapses, got %+v", got)
	}
}

func TestPollSustainedEmitsWarnOnce(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)}
	th := testThresholds()
	samples := []Sample{
		{RSSMB: 1500, CPUPct: 10, Tree: map[string]float64{"daemon": 1000, "sidecar": 500}},
		{RSSMB: 1500, CPUPct: 10},
		{RSSMB: 1500, CPUPct: 10},
	}
	w, events, _ := newTestWatcher(queueSampler(t, samples), clock.now, th)

	w.Poll() // elevatedSince = t0, elapsed = 0
	clock.advance(5 * time.Second)
	w.Poll() // elapsed = 5s, still < window
	clock.advance(6 * time.Second)
	w.Poll() // elapsed = 11s, >= window -> warn

	got := anomalyEvents(*events, "resource.sustained_high_rss")
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 RSS anomaly event, got %d: %+v", len(got), got)
	}
	ev := got[0]
	if ev.sev != clientevents.SevWarn {
		t.Fatalf("expected warn severity, got %v", ev.sev)
	}
	for _, key := range []string{"rss_mb", "threshold", "elevated_s"} {
		if _, ok := ev.fields[key]; !ok {
			t.Fatalf("expected field %q in %+v", key, ev.fields)
		}
	}
	if ev.fields["rss_mb"].(float64) != 1500 {
		t.Fatalf("expected rss_mb=1500, got %v", ev.fields["rss_mb"])
	}
	if ev.fields["threshold"].(float64) != th.RSSMB {
		t.Fatalf("expected threshold=%v, got %v", th.RSSMB, ev.fields["threshold"])
	}
}

func TestPollEscalatesWithoutReemittingWarn(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)}
	th := testThresholds() // window = 10s
	samples := []Sample{
		{RSSMB: 1500, CPUPct: 10}, // t=0
		{RSSMB: 1500, CPUPct: 10}, // t=11 -> warn
		{RSSMB: 1500, CPUPct: 10}, // t=15 -> still warn bucket, no re-emit
		{RSSMB: 1500, CPUPct: 10}, // t=21 -> still warn bucket (< 2*window=20? 21>=20 -> error)
	}
	w, events, _ := newTestWatcher(queueSampler(t, samples), clock.now, th)

	w.Poll() // t=0
	clock.advance(11 * time.Second)
	w.Poll() // t=11 -> warn (elapsed 11 >= 10)
	clock.advance(4 * time.Second)
	w.Poll() // t=15 -> elapsed 15, still < 2*window(20) -> still warn bucket, no new emit
	clock.advance(6 * time.Second)
	w.Poll() // t=21 -> elapsed 21 >= 2*window(20) -> error

	got := anomalyEvents(*events, "resource.sustained_high_rss")
	if len(got) != 2 {
		t.Fatalf("expected exactly 2 RSS anomaly events (warn, error), got %d: %+v", len(got), got)
	}
	if got[0].sev != clientevents.SevWarn {
		t.Fatalf("expected first event to be warn, got %v", got[0].sev)
	}
	if got[1].sev != clientevents.SevError {
		t.Fatalf("expected second event to be error (escalation), got %v", got[1].sev)
	}
}

func TestPollRecoversOnceThenResets(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)}
	th := testThresholds()
	samples := []Sample{
		{RSSMB: 1500, CPUPct: 10}, // t=0, elevatedSince=0
		{RSSMB: 1500, CPUPct: 10}, // t=11 -> warn
		{RSSMB: 500, CPUPct: 10},  // t=12 -> drop below threshold -> recovered
		{RSSMB: 1500, CPUPct: 10}, // t=13 -> re-elevate, fresh elevatedSince, elapsed=0, no anomaly yet
	}
	w, events, _ := newTestWatcher(queueSampler(t, samples), clock.now, th)

	w.Poll() // t=0
	clock.advance(11 * time.Second)
	w.Poll() // t=11 -> warn
	clock.advance(1 * time.Second)
	w.Poll() // t=12 -> recovered
	clock.advance(1 * time.Second)
	w.Poll() // t=13 -> re-elevated, fresh, no anomaly yet

	got := anomalyEvents(*events, "resource.sustained_high_rss")
	if len(got) != 2 {
		t.Fatalf("expected exactly 2 RSS events (warn, recovered), got %d: %+v", len(got), got)
	}
	if got[0].sev != clientevents.SevWarn {
		t.Fatalf("expected first event warn, got %v", got[0].sev)
	}
	// The recovery must carry the SAME severity as the anomaly it clears
	// (SevWarn here), not a fixed SevInfo -- otherwise, under the default
	// warn floor, the recovery would be dropped while the warn anomaly that
	// preceded it was delivered, leaving the track looking permanently
	// elevated on the dashboard.
	if got[1].sev != clientevents.SevWarn {
		t.Fatalf("expected recovered event to carry the anomaly's severity (warn), got %v", got[1].sev)
	}
	recovered, ok := got[1].fields["recovered"].(bool)
	if !ok || !recovered {
		t.Fatalf("expected recovered=true field, got %+v", got[1].fields)
	}
}

// TestPollRecoveryCarriesPeakEscalatedSeverity proves the recovery event
// after an escalation ladder (warn -> error) carries the PEAK severity
// reached (error), not SevInfo and not the initial warn bucket -- a track
// that escalated all the way to error/critical must recover at that same
// severity so it passes the same floor its anomaly passed, and so a receiver
// can tell how bad the episode got even from the recovery event alone.
func TestPollRecoveryCarriesPeakEscalatedSeverity(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)}
	th := testThresholds() // window = 10s -> error at 20s
	samples := []Sample{
		{RSSMB: 1500, CPUPct: 10}, // t=0
		{RSSMB: 1500, CPUPct: 10}, // t=11 -> warn
		{RSSMB: 1500, CPUPct: 10}, // t=21 -> error
		{RSSMB: 500, CPUPct: 10},  // t=22 -> drop below threshold -> recovered at error severity
	}
	w, events, _ := newTestWatcher(queueSampler(t, samples), clock.now, th)

	w.Poll() // t=0
	clock.advance(11 * time.Second)
	w.Poll() // t=11 -> warn
	clock.advance(10 * time.Second)
	w.Poll() // t=21 -> error
	clock.advance(1 * time.Second)
	w.Poll() // t=22 -> recovered

	got := anomalyEvents(*events, "resource.sustained_high_rss")
	if len(got) != 3 {
		t.Fatalf("expected exactly 3 RSS events (warn, error, recovered), got %d: %+v", len(got), got)
	}
	if got[0].sev != clientevents.SevWarn || got[1].sev != clientevents.SevError {
		t.Fatalf("expected warn then error before recovery, got %v then %v", got[0].sev, got[1].sev)
	}
	if got[2].sev != clientevents.SevError {
		t.Fatalf("expected recovery to carry the peak severity (error), got %v", got[2].sev)
	}
	recovered, ok := got[2].fields["recovered"].(bool)
	if !ok || !recovered {
		t.Fatalf("expected recovered=true field, got %+v", got[2].fields)
	}
}

func TestPollElevatedButNeverSustainedThenClears(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)}
	th := testThresholds() // window = 10s
	samples := []Sample{
		{RSSMB: 1500, CPUPct: 10}, // t=0, elevatedSince=0
		{RSSMB: 1500, CPUPct: 10}, // t=5, elapsed=5 < window -> no anomaly
		{RSSMB: 500, CPUPct: 10},  // t=8, dropped before window -> must NOT emit recovered
	}
	w, events, _ := newTestWatcher(queueSampler(t, samples), clock.now, th)

	w.Poll() // t=0
	clock.advance(5 * time.Second)
	w.Poll() // t=5
	clock.advance(3 * time.Second)
	w.Poll() // t=8 -> below threshold, but never crossed sustained window

	if got := anomalyEvents(*events, "resource.sustained_high_rss"); len(got) != 0 {
		t.Fatalf("expected zero events when elevated but never sustained then cleared, got %+v", got)
	}
}

func TestPollFullEscalationLadderToCritical(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)}
	th := testThresholds() // window = 10s -> error at 20s, critical at 40s
	samples := []Sample{
		{RSSMB: 1500, CPUPct: 10}, // t=0
		{RSSMB: 1500, CPUPct: 10}, // t=11 -> warn
		{RSSMB: 1500, CPUPct: 10}, // t=21 -> error
		{RSSMB: 1500, CPUPct: 10}, // t=41 -> critical
	}
	w, events, _ := newTestWatcher(queueSampler(t, samples), clock.now, th)

	w.Poll() // t=0
	clock.advance(11 * time.Second)
	w.Poll() // t=11 -> warn
	clock.advance(10 * time.Second)
	w.Poll() // t=21 -> error
	clock.advance(20 * time.Second)
	w.Poll() // t=41 -> critical

	got := anomalyEvents(*events, "resource.sustained_high_rss")
	if len(got) != 3 {
		t.Fatalf("expected 3 RSS anomaly events (warn, error, critical), got %d: %+v", len(got), got)
	}
	want := []clientevents.Severity{clientevents.SevWarn, clientevents.SevError, clientevents.SevCritical}
	for i, sev := range want {
		if got[i].sev != sev {
			t.Fatalf("escalation step %d: expected %v, got %v (all: %+v)", i, sev, got[i].sev, got)
		}
	}
}

func TestPollCPUTrackSustainedEmitsWarn(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)}
	th := testThresholds()
	samples := []Sample{
		{RSSMB: 100, CPUPct: 95},
		{RSSMB: 100, CPUPct: 95},
	}
	w, events, _ := newTestWatcher(queueSampler(t, samples), clock.now, th)

	w.Poll() // t=0
	clock.advance(11 * time.Second)
	w.Poll() // t=11 -> cpu warn

	got := anomalyEvents(*events, "resource.sustained_high_cpu")
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 CPU anomaly event, got %d: %+v", len(got), got)
	}
	if got[0].sev != clientevents.SevWarn {
		t.Fatalf("expected warn severity, got %v", got[0].sev)
	}
	for _, key := range []string{"cpu_pct", "threshold", "elevated_s"} {
		if _, ok := got[0].fields[key]; !ok {
			t.Fatalf("expected field %q in %+v", key, got[0].fields)
		}
	}
	// RSS stayed well below threshold throughout, so no RSS anomaly.
	if len(anomalyEvents(*events, "resource.sustained_high_rss")) != 0 {
		t.Fatalf("expected no RSS anomaly, got %+v", *events)
	}
}

func TestPollGaugeCadence(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)}
	th := testThresholds() // GaugeInterval = 30s
	n := 7
	samples := make([]Sample, n)
	for i := range samples {
		samples[i] = Sample{RSSMB: 500, CPUPct: 10}
	}
	w, _, gauges := newTestWatcher(queueSampler(t, samples), clock.now, th)

	// Poll at t = 0,10,20,30,40,50,60 (10s apart). Gauges fire at t=0 (baseline),
	// t=30, and t=60 -- 3 of the 7 polls.
	for i := 0; i < n; i++ {
		w.Poll()
		clock.advance(10 * time.Second)
	}

	if len(*gauges) != 3 {
		t.Fatalf("expected 3 gauge emits over %d polls, got %d: %+v", n, len(*gauges), *gauges)
	}
	for _, g := range *gauges {
		for _, key := range []string{"rss", "cpu", "n", "window_s", "proc_tree"} {
			if _, ok := g[key]; !ok {
				t.Fatalf("expected gauge field %q in %+v", key, g)
			}
		}
		if _, ok := g["rss"].(map[string]any); !ok {
			t.Fatalf("expected rss to be map[string]any, got %T", g["rss"])
		}
		if _, ok := g["cpu"].(map[string]any); !ok {
			t.Fatalf("expected cpu to be map[string]any, got %T", g["cpu"])
		}
	}
}

// TestPollGaugeFirstPollBaseline proves the very first Poll() emits a
// baseline gauge distribution over a single sample: n=1, std=0, and
// min==max==mean==last==the sample -- there is no prior interval to average
// over, so the "distribution" degenerates to the one point.
func TestPollGaugeFirstPollBaseline(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)}
	th := testThresholds()
	th.GaugeInterval = 300 * time.Second
	samples := []Sample{{RSSMB: 512, CPUPct: 42}}
	w, _, gauges := newTestWatcher(queueSampler(t, samples), clock.now, th)

	w.Poll()

	if len(*gauges) != 1 {
		t.Fatalf("expected exactly 1 gauge emit on first poll, got %d: %+v", len(*gauges), *gauges)
	}
	g := (*gauges)[0]
	if n, ok := g["n"].(int); !ok || n != 1 {
		t.Fatalf("expected n=1, got %v (%T)", g["n"], g["n"])
	}
	if ws, ok := g["window_s"].(float64); !ok || ws != 0 {
		t.Fatalf("expected window_s=0 on first poll, got %v", g["window_s"])
	}
	rss, ok := g["rss"].(map[string]any)
	if !ok {
		t.Fatalf("expected rss to be map[string]any, got %T", g["rss"])
	}
	for _, key := range []string{"min", "max", "mean", "last"} {
		if rss[key].(float64) != 512 {
			t.Fatalf("expected rss[%q]=512, got %v", key, rss[key])
		}
	}
	if rss["std"].(float64) != 0 {
		t.Fatalf("expected rss[std]=0 for a single sample, got %v", rss["std"])
	}
	cpu, ok := g["cpu"].(map[string]any)
	if !ok {
		t.Fatalf("expected cpu to be map[string]any, got %T", g["cpu"])
	}
	for _, key := range []string{"min", "max", "mean", "last"} {
		if cpu[key].(float64) != 42 {
			t.Fatalf("expected cpu[%q]=42, got %v", key, cpu[key])
		}
	}
	if cpu["std"].(float64) != 0 {
		t.Fatalf("expected cpu[std]=0 for a single sample, got %v", cpu["std"])
	}
}

// TestPollGaugeIntervalDistribution feeds three sub-samples within one gauge
// interval (100, 300, 200 -- folded in that order, the last one closing the
// interval) and asserts the emitted distribution against hand-computed
// min/max/mean/std/last. Population std of {100,200,300}: mean=200,
// sumsq=10000+90000+40000=140000, var=140000/3-40000=6666.67,
// std=sqrt(6666.67)=81.65.
func TestPollGaugeIntervalDistribution(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)}
	th := testThresholds()
	th.GaugeInterval = 300 * time.Second
	samples := []Sample{
		{RSSMB: 999, CPUPct: 99}, // t=0: first-poll baseline, not part of the distribution under test
		{RSSMB: 100, CPUPct: 10}, // t=100: within interval
		{RSSMB: 300, CPUPct: 30}, // t=200: within interval
		{RSSMB: 200, CPUPct: 20}, // t=300: crosses the 300s boundary -> closes the interval
	}
	w, _, gauges := newTestWatcher(queueSampler(t, samples), clock.now, th)

	for i := 0; i < len(samples); i++ {
		w.Poll()
		clock.advance(100 * time.Second)
	}

	if len(*gauges) != 2 {
		t.Fatalf("expected exactly 2 gauge emits (baseline + interval close), got %d: %+v", len(*gauges), *gauges)
	}
	g := (*gauges)[1]
	if n, ok := g["n"].(int); !ok || n != 3 {
		t.Fatalf("expected n=3, got %v (%T)", g["n"], g["n"])
	}
	if ws, ok := g["window_s"].(float64); !ok || ws != 300 {
		t.Fatalf("expected window_s=300, got %v", g["window_s"])
	}
	rss, ok := g["rss"].(map[string]any)
	if !ok {
		t.Fatalf("expected rss to be map[string]any, got %T", g["rss"])
	}
	assertClose := func(name string, got, want float64) {
		if diff := got - want; diff > 0.01 || diff < -0.01 {
			t.Fatalf("rss[%s]: got %v, want %v", name, got, want)
		}
	}
	assertClose("min", rss["min"].(float64), 100)
	assertClose("max", rss["max"].(float64), 300)
	assertClose("mean", rss["mean"].(float64), 200)
	assertClose("last", rss["last"].(float64), 200)
	assertClose("std", rss["std"].(float64), 81.65)

	cpu, ok := g["cpu"].(map[string]any)
	if !ok {
		t.Fatalf("expected cpu to be map[string]any, got %T", g["cpu"])
	}
	assertClose("cpu.min", cpu["min"].(float64), 10)
	assertClose("cpu.max", cpu["max"].(float64), 30)
	assertClose("cpu.mean", cpu["mean"].(float64), 20)
	assertClose("cpu.last", cpu["last"].(float64), 20)
	assertClose("cpu.std", cpu["std"].(float64), 8.165)
}

// TestPollGaugeResetsBetweenIntervals proves that once an interval closes and
// emits, the NEXT interval's stats reflect only its own samples -- the
// accumulator must be reset, not carried forward.
func TestPollGaugeResetsBetweenIntervals(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)}
	th := testThresholds()
	th.GaugeInterval = 300 * time.Second
	samples := []Sample{
		{RSSMB: 999, CPUPct: 99}, // t=0: baseline
		{RSSMB: 100, CPUPct: 10}, // t=100: interval 1
		{RSSMB: 300, CPUPct: 30}, // t=200: interval 1
		{RSSMB: 200, CPUPct: 20}, // t=300: closes interval 1
		{RSSMB: 50, CPUPct: 5},   // t=400: interval 2
		{RSSMB: 150, CPUPct: 15}, // t=500: interval 2
		{RSSMB: 100, CPUPct: 10}, // t=600: closes interval 2
	}
	w, _, gauges := newTestWatcher(queueSampler(t, samples), clock.now, th)

	for i := 0; i < len(samples); i++ {
		w.Poll()
		clock.advance(100 * time.Second)
	}

	if len(*gauges) != 3 {
		t.Fatalf("expected exactly 3 gauge emits, got %d: %+v", len(*gauges), *gauges)
	}
	g := (*gauges)[2]
	if n, ok := g["n"].(int); !ok || n != 3 {
		t.Fatalf("expected n=3 for interval 2, got %v (%T)", g["n"], g["n"])
	}
	rss := g["rss"].(map[string]any)
	// Interval 2's samples are {50,150,100}; interval 1's max (300) and min
	// (100 is shared but not the min here) must NOT leak in.
	if got := rss["max"].(float64); got != 150 {
		t.Fatalf("expected interval 2 max=150 (not carrying interval 1's 300), got %v", got)
	}
	if got := rss["min"].(float64); got != 50 {
		t.Fatalf("expected interval 2 min=50, got %v", got)
	}
	if got := rss["mean"].(float64); got != 100 {
		t.Fatalf("expected interval 2 mean=100, got %v", got)
	}
}

// TestSetThresholdsAffectsSubsequentPoll proves SetThresholds changes the
// thresholds used by later Poll calls (the settings-poll goroutine updating a
// live daemon's watcher without restarting it).
func TestSetThresholdsAffectsSubsequentPoll(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)}
	initial := testThresholds() // RSSMB: 1000
	samples := []Sample{
		{RSSMB: 800, CPUPct: 10}, // below the initial 1000 threshold -> no anomaly
		{RSSMB: 800, CPUPct: 10}, // after SetThresholds(500): now elevated, elevatedSince set
		{RSSMB: 800, CPUPct: 10}, // 11s later: sustained -> warn
	}
	w, events, _ := newTestWatcher(queueSampler(t, samples), clock.now, initial)

	w.Poll() // t=0, RSSMB=800 vs threshold=1000 -> not elevated
	if got := anomalyEvents(*events, "resource.sustained_high_rss"); len(got) != 0 {
		t.Fatalf("expected no anomaly before SetThresholds, got %+v", got)
	}

	w.SetThresholds(Thresholds{RSSMB: 500, CPUPct: 80, SustainedWindow: 10 * time.Second, GaugeInterval: 30 * time.Second})

	w.Poll() // t=0 (same instant), RSSMB=800 vs new threshold=500 -> elevatedSince set
	clock.advance(11 * time.Second)
	w.Poll() // t=11 -> elapsed 11s >= 10s window -> warn

	got := anomalyEvents(*events, "resource.sustained_high_rss")
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 RSS anomaly after threshold lowered, got %d: %+v", len(got), got)
	}
	if got[0].sev != clientevents.SevWarn {
		t.Fatalf("expected warn severity, got %v", got[0].sev)
	}
	if got[0].fields["threshold"].(float64) != 500 {
		t.Fatalf("expected updated threshold=500 reflected in fields, got %v", got[0].fields["threshold"])
	}
}

func TestPollProcTreeSurvivesAsMapAny(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)}
	th := testThresholds()
	samples := []Sample{
		{RSSMB: 1500, CPUPct: 10, Tree: map[string]float64{"daemon": 1000, "sidecar": 500}},
		{RSSMB: 1500, CPUPct: 10, Tree: map[string]float64{"daemon": 1000, "sidecar": 500}},
	}
	w, events, _ := newTestWatcher(queueSampler(t, samples), clock.now, th)

	w.Poll() // t=0
	clock.advance(11 * time.Second)
	w.Poll() // t=11 -> warn, with proc_tree

	got := anomalyEvents(*events, "resource.sustained_high_rss")
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 RSS anomaly event, got %d: %+v", len(got), got)
	}
	tree, ok := got[0].fields["proc_tree"].(map[string]any)
	if !ok {
		t.Fatalf("expected proc_tree to be map[string]any, got %T", got[0].fields["proc_tree"])
	}
	if tree["daemon"] != 1000.0 || tree["sidecar"] != 500.0 {
		t.Fatalf("expected proc_tree values preserved, got %+v", tree)
	}
}
