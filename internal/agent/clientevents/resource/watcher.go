// Package resource watches the keld-signal process tree (daemon + sidecar +
// worker child) for sustained high RSS/CPU, emitting escalating anomaly
// events plus low-frequency gauge snapshots via callbacks supplied by the
// daemon (Task 7 wires these to a clientevents.Emitter).
package resource

import (
	"context"
	"math"
	"sync/atomic"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/shirou/gopsutil/v4/process"
)

// Escalation multipliers: an anomaly track's severity bucket is a function of
// how long it has stayed continuously elevated, in multiples of
// Thresholds.SustainedWindow.
const (
	escalateErrorMultiplier    = 2
	escalateCriticalMultiplier = 4
)

// Sample is one point-in-time resource reading for the watched process tree.
type Sample struct {
	RSSMB  float64
	CPUPct float64
	Tree   map[string]float64 // per-process contribution, e.g. {"daemon": .., "sidecar": .., "worker": ..}
}

// Sampler produces a fresh Sample on demand. The production implementation
// (NewProcessTreeSampler) walks the real process tree; tests inject a
// deterministic scripted Sampler so Poll's state machine is exercised without
// real I/O.
type Sampler func() Sample

// Thresholds configures when a track is considered elevated, how long it must
// stay elevated before it's "sustained" (worth alerting on), and how often a
// baseline gauge snapshot is emitted regardless of anomaly state.
type Thresholds struct {
	RSSMB           float64
	CPUPct          float64
	SustainedWindow time.Duration
	GaugeInterval   time.Duration
}

// trackState is the per-track (RSS or CPU) elevated/anomaly bookkeeping.
type trackState struct {
	elevatedSince time.Time
	lastSeverity  clientevents.Severity
}

// acc is a running (constant-memory) accumulator over the sub-samples taken
// within one gauge interval, producing a min/max/mean/population-std/last
// distribution without retaining the individual samples.
type acc struct {
	n                          int
	sum, sumsq, min, max, last float64
}

// add folds one more sample into the accumulator.
func (a *acc) add(v float64) {
	if a.n == 0 || v < a.min {
		a.min = v
	}
	if a.n == 0 || v > a.max {
		a.max = v
	}
	a.n++
	a.sum += v
	a.sumsq += v * v
	a.last = v
}

// stats returns the distribution accumulated so far as a map[string]any (not
// map[string]float64) so it survives clientevents' redaction gate -- see
// treeAsAny below for the same rationale.
func (a *acc) stats() map[string]any {
	mean := 0.0
	if a.n > 0 {
		mean = a.sum / float64(a.n)
	}
	varp := 0.0
	if a.n > 1 {
		varp = a.sumsq/float64(a.n) - mean*mean
		if varp < 0 {
			varp = 0
		}
	}
	return map[string]any{
		"min": a.min, "max": a.max, "mean": mean,
		"std": math.Sqrt(varp), "last": a.last,
	}
}

// reset zeroes the accumulator for the next gauge interval.
func (a *acc) reset() { *a = acc{} }

// Watcher polls a Sampler on a timer and drives two independent
// hysteresis/escalation state machines (RSS, CPU) plus a low-frequency gauge
// snapshot, invoking the injected emit/emitGauge callbacks. Poll is pure-ish
// (no real I/O of its own — the sampler and clock are injected) so it's fully
// deterministic under test.
type Watcher struct {
	emit      func(code string, sev clientevents.Severity, fields map[string]any)
	emitGauge func(fields map[string]any)
	th        atomic.Value // Thresholds
	sampler   Sampler
	clock     func() time.Time

	lastGaugeAt  time.Time
	gaugeStartAt time.Time
	rssAcc       acc
	cpuAcc       acc
	rss          trackState
	cpu          trackState
}

// NewWatcher builds a Watcher. emit is called for each anomaly transition
// (sustained-high crossing an escalation bucket, or recovery); emitGauge is
// called on the configured cadence with a baseline resource snapshot.
func NewWatcher(
	emit func(code string, sev clientevents.Severity, fields map[string]any),
	emitGauge func(fields map[string]any),
	th Thresholds,
	sampler Sampler,
	clock func() time.Time,
) *Watcher {
	w := &Watcher{
		emit:      emit,
		emitGauge: emitGauge,
		sampler:   sampler,
		clock:     clock,
	}
	w.th.Store(th)
	return w
}

// SetThresholds atomically replaces the thresholds used by subsequent Poll
// calls. Safe for concurrent use with Poll/Run — called by the daemon's
// settings-poll goroutine while Run's own goroutine is independently polling,
// so both sides must never race on a plain (unsynchronized) struct field.
func (w *Watcher) SetThresholds(th Thresholds) {
	w.th.Store(th)
}

// thresholds returns the current thresholds snapshot. Poll reads through this
// (rather than a raw field) so it never races with a concurrent SetThresholds.
func (w *Watcher) thresholds() Thresholds {
	return w.th.Load().(Thresholds)
}

// Run polls once immediately, then on every tick of interval until ctx is
// cancelled.
func (w *Watcher) Run(ctx context.Context, interval time.Duration) {
	w.Poll()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			w.Poll()
		case <-ctx.Done():
			return
		}
	}
}

// Poll takes one sample and advances both the RSS and CPU track state
// machines, plus the gauge cadence. It performs no real I/O itself — the
// sampler and clock are injected, so this is fully deterministic under test.
func (w *Watcher) Poll() {
	s := w.sampler()
	now := w.clock()
	th := w.thresholds()

	w.rssAcc.add(s.RSSMB)
	w.cpuAcc.add(s.CPUPct)

	if w.lastGaugeAt.IsZero() || now.Sub(w.lastGaugeAt) >= th.GaugeInterval {
		windowS := 0.0
		if !w.gaugeStartAt.IsZero() {
			windowS = now.Sub(w.gaugeStartAt).Seconds()
		}
		w.emitGauge(map[string]any{
			"rss": w.rssAcc.stats(), "cpu": w.cpuAcc.stats(),
			"n": w.rssAcc.n, "window_s": windowS, "proc_tree": treeAsAny(s.Tree),
		})
		w.rssAcc.reset()
		w.cpuAcc.reset()
		w.lastGaugeAt = now
		w.gaugeStartAt = now
	}

	w.pollTrack(&w.rss, s.RSSMB, th.RSSMB, now, th.SustainedWindow, "resource.sustained_high_rss", func(value, threshold, elapsedS float64) map[string]any {
		return map[string]any{
			"rss_mb":     value,
			"threshold":  threshold,
			"elevated_s": elapsedS,
			"proc_tree":  treeAsAny(s.Tree),
		}
	})

	w.pollTrack(&w.cpu, s.CPUPct, th.CPUPct, now, th.SustainedWindow, "resource.sustained_high_cpu", func(value, threshold, elapsedS float64) map[string]any {
		return map[string]any{
			"cpu_pct":    value,
			"threshold":  threshold,
			"elevated_s": elapsedS,
			"proc_tree":  treeAsAny(s.Tree),
		}
	})
}

// pollTrack advances a single track's (RSS or CPU) hysteresis/escalation
// state machine: edge-triggered anomaly emission on severity bucket change,
// a single recovered event on the drop below threshold, and a full reset so
// the next elevation starts fresh at warn. fields builds the code-specific
// field map (value/threshold/elevated_s/proc_tree) for both the anomaly and
// recovered events. sustainedWindow is passed in (rather than read off w.th)
// so the whole Poll() call uses one consistent thresholds snapshot even if
// SetThresholds races in concurrently.
func (w *Watcher) pollTrack(tr *trackState, value, threshold float64, now time.Time, sustainedWindow time.Duration, code string, fields func(value, threshold, elapsedS float64) map[string]any) {
	if value > threshold {
		if tr.elevatedSince.IsZero() {
			tr.elevatedSince = now
		}
		elapsed := now.Sub(tr.elevatedSince)
		if elapsed >= sustainedWindow {
			sev := severityFor(elapsed, sustainedWindow)
			if sev != tr.lastSeverity {
				w.emit(code, sev, fields(value, threshold, elapsed.Seconds()))
				tr.lastSeverity = sev
			}
		}
		return
	}

	if !tr.elevatedSince.IsZero() && tr.lastSeverity != "" {
		// Emit the recovery at the track's peak (last-reached) severity for
		// this episode, not a fixed SevInfo: under the default warn floor, an
		// info-severity recovery would be dropped by the very floor the
		// matching anomaly passed to be delivered, leaving the anomaly
		// looking permanently unresolved. Using the same severity means the
		// recovery is delivered iff its anomaly was (no orphan recoveries,
		// no floor bypass). Capture it before resetting the track below.
		peakSeverity := tr.lastSeverity
		f := fields(value, threshold, now.Sub(tr.elevatedSince).Seconds())
		f["recovered"] = true
		w.emit(code, peakSeverity, f)
	}
	tr.elevatedSince = time.Time{}
	tr.lastSeverity = ""
}

// severityFor buckets how long a track has been continuously elevated into an
// escalating severity: warn at the window, error at escalateErrorMultiplier
// times the window, critical at escalateCriticalMultiplier times the window.
func severityFor(elapsed, window time.Duration) clientevents.Severity {
	switch {
	case elapsed >= escalateCriticalMultiplier*window:
		return clientevents.SevCritical
	case elapsed >= escalateErrorMultiplier*window:
		return clientevents.SevError
	default:
		return clientevents.SevWarn
	}
}

// treeAsAny copies a per-process float64 tree into a map[string]any so it
// survives clientevents' redaction gate: redactFields recurses into
// map[string]any and passes numeric values through, but drops any value of an
// unrecognized type (including map[string]float64) rather than risk
// publishing something unvetted.
func treeAsAny(tree map[string]float64) map[string]any {
	out := make(map[string]any, len(tree))
	for k, v := range tree {
		out[k] = v
	}
	return out
}

// NewProcessTreeSampler returns a Sampler that walks the real process tree
// rooted at daemonPID (the daemon itself plus all descendants — sidecar,
// inference worker, ...), summing RSS (MB) and CPU% across the tree.
//
// The returned closure is STATEFUL: it keeps a persistent cache of
// *process.Process handles keyed by pid across calls. This is required for
// correct CPU measurement — Percent(0) reports CPU usage as an interval delta
// since the *previous call on the same handle*, so a fresh handle created each
// poll (as gopsutil's lifetime-average CPUPercent effectively does) would
// always read ~0 and the sustained-CPU anomaly would never fire. Reusing the
// handle means the first poll on a new pid returns 0 (it caches the baseline)
// and every poll thereafter yields a true since-last-poll delta; the sustained
// window spans multiple polls, so the discarded first sample is harmless.
//
// Note: summed tree cpu_pct can exceed 100 on multi-core hosts (each process
// can independently saturate a core) — the operator sets Thresholds.CPUPct
// with that in mind.
//
// It is deliberately defensive: a handle whose process has exited (errors on
// MemoryInfo/Percent/Children) is skipped and evicted from the cache, a bad
// root pid returns a zero Sample, and it never panics. This function is not
// exercised by the deterministic unit tests — Poll's state machine is tested
// via an injected Sampler.
func NewProcessTreeSampler(daemonPID int) Sampler {
	// cache holds one *process.Process per live pid, persisted across calls so
	// Percent(0) has the per-handle baseline it needs for an interval delta.
	cache := map[int32]*process.Process{}

	return func() Sample {
		root, ok := cache[int32(daemonPID)]
		if !ok {
			p, err := process.NewProcess(int32(daemonPID))
			if err != nil {
				return Sample{}
			}
			root = p
			cache[int32(daemonPID)] = p
		}

		// Recompute the current tree pid set, reusing cached handles for pids
		// still present and creating handles for newly-seen pids.
		procs := collectTree(root)
		present := make(map[int32]bool, len(procs))
		for i, p := range procs {
			pid := p.Pid
			present[pid] = true
			if cached, ok := cache[pid]; ok {
				procs[i] = cached // reuse the handle (keeps Percent baseline)
			} else {
				cache[pid] = p
			}
		}
		// Evict handles for pids that vanished since the last poll.
		for pid := range cache {
			if !present[pid] {
				delete(cache, pid)
			}
		}

		var totalRSSMB, totalCPUPct float64
		tree := make(map[string]float64, len(procs))
		for i, p := range procs {
			role := "others"
			if i == 0 {
				role = "daemon"
			}

			mem, memErr := p.MemoryInfo()
			if memErr == nil && mem != nil {
				rssMB := float64(mem.RSS) / 1024 / 1024
				totalRSSMB += rssMB
				tree[role] += rssMB
			}
			// Percent(0): CPU% used since the previous call on this handle
			// (non-blocking). First call on a fresh handle returns 0.
			cpuPct, cpuErr := p.Percent(0)
			if cpuErr == nil {
				totalCPUPct += cpuPct
			}

			// A handle that errors on both reads has almost certainly exited;
			// drop it from the cache so a stale handle isn't reused next poll.
			if memErr != nil && cpuErr != nil {
				delete(cache, p.Pid)
			}
		}

		return Sample{RSSMB: totalRSSMB, CPUPct: totalCPUPct, Tree: tree}
	}
}

// collectTree returns root followed by all of its descendants, walked
// recursively via Children(). A Children() error (e.g. the process exited
// mid-walk) just stops descending that branch rather than aborting the whole
// walk.
func collectTree(root *process.Process) []*process.Process {
	procs := []*process.Process{root}

	children, err := root.Children()
	if err != nil {
		return procs
	}
	for _, c := range children {
		procs = append(procs, collectTree(c)...)
	}
	return procs
}
