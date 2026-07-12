// Package resource watches the keld-signal process tree (daemon + sidecar +
// worker child) for sustained high RSS/CPU, emitting escalating anomaly
// events plus low-frequency gauge snapshots via callbacks supplied by the
// daemon (Task 7 wires these to a clientevents.Emitter).
package resource

import (
	"context"
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

// Watcher polls a Sampler on a timer and drives two independent
// hysteresis/escalation state machines (RSS, CPU) plus a low-frequency gauge
// snapshot, invoking the injected emit/emitGauge callbacks. Poll is pure-ish
// (no real I/O of its own — the sampler and clock are injected) so it's fully
// deterministic under test.
type Watcher struct {
	daemonPID int
	emit      func(code string, sev clientevents.Severity, fields map[string]any)
	emitGauge func(fields map[string]any)
	th        Thresholds
	sampler   Sampler
	clock     func() time.Time

	lastGaugeAt time.Time
	rss         trackState
	cpu         trackState
}

// NewWatcher builds a Watcher. emit is called for each anomaly transition
// (sustained-high crossing an escalation bucket, or recovery); emitGauge is
// called on the configured cadence with a baseline resource snapshot.
func NewWatcher(
	daemonPID int,
	emit func(code string, sev clientevents.Severity, fields map[string]any),
	emitGauge func(fields map[string]any),
	th Thresholds,
	sampler Sampler,
	clock func() time.Time,
) *Watcher {
	return &Watcher{
		daemonPID: daemonPID,
		emit:      emit,
		emitGauge: emitGauge,
		th:        th,
		sampler:   sampler,
		clock:     clock,
	}
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

	if w.lastGaugeAt.IsZero() || now.Sub(w.lastGaugeAt) >= w.th.GaugeInterval {
		w.emitGauge(map[string]any{
			"rss_mb":    s.RSSMB,
			"cpu_pct":   s.CPUPct,
			"proc_tree": treeAsAny(s.Tree),
		})
		w.lastGaugeAt = now
	}

	w.pollTrack(&w.rss, s.RSSMB, w.th.RSSMB, now, "resource.sustained_high_rss", func(value, threshold, elapsedS float64) map[string]any {
		return map[string]any{
			"rss_mb":     value,
			"threshold":  threshold,
			"elevated_s": elapsedS,
			"proc_tree":  treeAsAny(s.Tree),
		}
	})

	w.pollTrack(&w.cpu, s.CPUPct, w.th.CPUPct, now, "resource.sustained_high_cpu", func(value, threshold, elapsedS float64) map[string]any {
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
// recovered events.
func (w *Watcher) pollTrack(tr *trackState, value, threshold float64, now time.Time, code string, fields func(value, threshold, elapsedS float64) map[string]any) {
	if value > threshold {
		if tr.elevatedSince.IsZero() {
			tr.elevatedSince = now
		}
		elapsed := now.Sub(tr.elevatedSince)
		if elapsed >= w.th.SustainedWindow {
			sev := severityFor(elapsed, w.th.SustainedWindow)
			if sev != tr.lastSeverity {
				w.emit(code, sev, fields(value, threshold, elapsed.Seconds()))
				tr.lastSeverity = sev
			}
		}
		return
	}

	if !tr.elevatedSince.IsZero() && tr.lastSeverity != "" {
		f := fields(value, threshold, now.Sub(tr.elevatedSince).Seconds())
		f["recovered"] = true
		w.emit(code, clientevents.SevInfo, f)
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
