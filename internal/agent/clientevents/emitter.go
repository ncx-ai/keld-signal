package clientevents

import (
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

// defaultCapacity is used when NewEmitter is given a non-positive capacity.
const defaultCapacity = 256

// Gate governs whether/how events are kept, as configured by the settings
// poll (Task 7). The zero value (Enabled: false) disables emission entirely,
// so an Emitter with no SetGate call yet drops everything safely.
type Gate struct {
	Enabled     bool
	MinSeverity Severity
	SampleRate  float64
}

// Emitter buffers structured client events in memory for a reporter (Task 5)
// to drain and POST. Emit/EmitGauge are called from hot daemon paths and MUST
// be non-blocking and bounded: a short mutex-guarded ring append is the only
// synchronization, with no channels, I/O, or unbounded growth.
type Emitter struct {
	base Corr

	mu       sync.Mutex
	ring     []Event
	capacity int

	gate atomic.Value // Gate

	clock     func() time.Time
	randFloat func() float64
}

// NewEmitter creates an Emitter with the given base correlation metadata
// (stamped onto every event) and ring capacity. A non-positive capacity is
// replaced with a sane default. The gate starts disabled (zero Gate{}) until
// SetGate is called.
func NewEmitter(base Corr, capacity int) *Emitter {
	if capacity <= 0 {
		capacity = defaultCapacity
	}
	e := &Emitter{
		base:      base,
		ring:      make([]Event, 0, capacity),
		capacity:  capacity,
		clock:     time.Now,
		randFloat: rand.Float64,
	}
	e.gate.Store(Gate{})
	return e
}

// SetGate atomically replaces the current gate. Safe for concurrent use with
// Emit/EmitGauge; called by the settings poll whenever remote config changes.
func (e *Emitter) SetGate(g Gate) {
	e.gate.Store(g)
}

// Emit records an event of the given code/severity, subject to the current
// Gate: dropped if disabled, below the severity floor, or sampled out.
// Never blocks.
func (e *Emitter) Emit(code string, sev Severity, fields map[string]any) {
	e.record(code, sev, fields, e.base, true)
}

// EmitExempt records an event exempt from the MinSeverity floor (like
// EmitGauge) but preserving the caller's severity — for low-volume lifecycle
// events (e.g. daemon.start/daemon.stop) that must always surface for
// narrative reconstruction even under a warn-and-above gate. Still subject to
// Enabled and SampleRate. Never blocks.
func (e *Emitter) EmitExempt(code string, sev Severity, fields map[string]any) {
	e.record(code, sev, fields, e.base, false)
}

// EmitGauge records an info-severity gauge snapshot, exempt from the
// MinSeverity floor (so a warn-and-above gate still lets gauges through) but
// still subject to Enabled and SampleRate. Never blocks.
func (e *Emitter) EmitGauge(code string, fields map[string]any) {
	e.record(code, SevInfo, fields, e.base, false)
}

// sampledIn reports whether this event survives sampling for the given gate:
// keep iff randFloat() < SampleRate, so SampleRate 1.0 always keeps (rand is
// in [0,1)) and 0.0 always drops.
func (e *Emitter) sampledIn(gate Gate) bool {
	return e.randFloat() < gate.SampleRate
}

// record is the shared gate+enqueue path for Emit/EmitExempt/EmitGauge and
// JobEmitter.Emit. It always honors Enabled and SampleRate; it applies the
// MinSeverity floor only when applyFloor is true (Emit / JobEmitter.Emit) and
// skips it otherwise (EmitExempt / EmitGauge). On acceptance it redacts fields
// and inserts an Event stamped with corr (the base Corr, or a job-augmented
// copy) and the current clock. Never blocks.
func (e *Emitter) record(code string, sev Severity, fields map[string]any, corr Corr, applyFloor bool) {
	gate := e.gate.Load().(Gate)
	if !gate.Enabled {
		return
	}
	if applyFloor && !sev.AtLeast(gate.MinSeverity) {
		return
	}
	if !e.sampledIn(gate) {
		return
	}
	e.insert(Event{
		Code:     code,
		Severity: sev,
		Fields:   redactFields(fields),
		Corr:     corr,
		TS:       e.clock(),
	})
}

// insert appends evt to the ring under the lock — coalescing into the
// previous event when it shares the same code+severity, and dropping the
// oldest entry on overflow. This is the only synchronized section; it does
// no I/O and never blocks.
func (e *Emitter) insert(evt Event) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if n := len(e.ring); n > 0 {
		last := &e.ring[n-1]
		if last.Code == evt.Code && last.Severity == evt.Severity {
			if last.Fields == nil {
				last.Fields = make(map[string]any, 1)
			}
			count, _ := last.Fields["count"].(int)
			if count == 0 {
				count = 1
			}
			last.Fields["count"] = count + 1
			return
		}
	}

	e.ring = append(e.ring, evt)
	if len(e.ring) > e.capacity {
		e.ring = e.ring[1:]
	}
}

// WithJob returns a JobEmitter that stamps the given session/prompt ids onto
// every event it emits, in addition to the parent's base Corr.
func (e *Emitter) WithJob(session, prompt string) *JobEmitter {
	return &JobEmitter{parent: e, session: session, prompt: prompt}
}

// Drain returns the buffered batch and resets the ring to empty. The
// returned slice is a fresh backing array from the caller's perspective
// (Drain never hands out the internal buffer it goes on to mutate).
func (e *Emitter) Drain() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()

	if len(e.ring) == 0 {
		return nil
	}
	batch := e.ring
	e.ring = make([]Event, 0, e.capacity)
	return batch
}

// JobEmitter stamps a fixed session/prompt id pair onto every event it emits,
// on top of the parent Emitter's base Corr, gate, and ring.
type JobEmitter struct {
	parent  *Emitter
	session string
	prompt  string
}

// Emit records an event via the parent Emitter, with SessionID/PromptID
// stamped onto the Corr. Subject to the same gate as Emitter.Emit (including
// the MinSeverity floor).
func (j *JobEmitter) Emit(code string, sev Severity, fields map[string]any) {
	e := j.parent
	corr := e.base
	corr.SessionID = j.session
	corr.PromptID = j.prompt
	e.record(code, sev, fields, corr, true)
}
