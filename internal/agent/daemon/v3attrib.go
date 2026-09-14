package daemon

import (
	"sync/atomic"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/attrib"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
)

// THE VECTORISED ATTRIBUTION PASS'S ONLY DOOR INTO THE DELIVERY LEDGER.
//
// ⚠️ **BOTH PASSES USED TO WRITE THE SAME CELL, AND THE SECOND ONE WON.**
// `recordAttributeQuarantined` called `ledger.Failed(k, StageAttributed,
// ReasonAttributeFailed, …)` — the identical cell `attributeAndRecord`
// (v3blocks.go) had already written the deterministic answer into. So a
// vectorised job that exhausted its retries did not merely fail to add a
// second opinion; it ERASED the first one. Measured on one machine: 44 rows
// reading `failed / attribute_failed`, 25 of them on a repository a declared
// rule matches exactly, every one of them caused by an encoder that could not
// get memory (55 `encoder silent for 60s, child killed` lines in the same
// window). The deterministic pass had answered correctly and its answer was
// overwritten by the failure of a pass that ships OFF by default.
//
// The rule this file now enforces is the one a person switching the toggle on
// would assume: **the vectorised pass adds an opinion and never changes the
// deterministic one.** If the encoder cannot run, the second opinion is lost
// and nothing else is.
//
// # HOW THAT IS MADE IMPOSSIBLE RATHER THAN MERELY ABSENT
//
// vectorLedger holds a `ledger.VectorRecorder`, which is an interface with
// exactly one method, which writes exactly one cell. It is NOT a
// `ledger.Recorder` and does not embed one, so no code reachable from this
// file can call `Attribute`, `Failed` or `NotApplicable` — there is no such
// method on the value it holds. The coupling is not avoided by care; it is
// unspeakable. The other direction is closed in the store: the vector column
// set is deliberately not registered in `stageColumns`, so `Failed(k,
// Stage("vector"), …)` from anywhere else reaches no column at all.

// attribQuarantineHandler is how the attribution path's terminal quarantine
// event reaches the ledger, without adding a parameter to startAttributor's
// signature — the same package-level-accessor shape sidecarProbe/
// setSidecarProbe (v3health.go) already use, and for the identical reason:
// the Attributor is built well after v3 exists, deep inside wireEnrichment's
// conditional branch, and every existing call site of startAttributor
// (attrib_test.go's fixed-arity calls included) stays unchanged.
var attribQuarantineHandler atomic.Pointer[func(sessionID string, start float64)]

// attribOutcomeHandler is its sibling for every answer the sidecar actually
// gives. Two accessors rather than one because the two report different kinds
// of fact — "the pass concluded X" and "the pass never got to conclude
// anything" — and a machine can produce either without the other.
var attribOutcomeHandler atomic.Pointer[func(attrib.Outcome)]

// setAttribQuarantineHandler mirrors setBlockAdvance's exact shape (blocks.go)
// and for the same reason: atomic.Pointer[T] where T is itself a func type
// needs an explicit nil guard, or storing &fn for a nil fn wraps it in a
// non-nil *T whose underlying value is nil — Load() would then read as "a
// handler is set" and the call in noteAttributionQuarantine would panic. A
// genuine nil clears the pointer outright, which is what a test resetting
// this between runs needs.
func setAttribQuarantineHandler(fn func(sessionID string, start float64)) {
	if fn == nil {
		attribQuarantineHandler.Store(nil)
		return
	}
	attribQuarantineHandler.Store(&fn)
}

// setAttribOutcomeHandler is setAttribQuarantineHandler's sibling, with the
// same nil guard and for the same reason.
func setAttribOutcomeHandler(fn func(attrib.Outcome)) {
	if fn == nil {
		attribOutcomeHandler.Store(nil)
		return
	}
	attribOutcomeHandler.Store(&fn)
}

// noteAttributionQuarantine is the nil-safe hook startAttributor wires onto
// the Attributor (attrib.Attributor.WithQuarantineHook). It forwards to
// whatever v3 registered at startup, or does nothing on a machine — or a
// test — where nothing ever called setAttribQuarantineHandler.
func noteAttributionQuarantine(sessionID string, start float64) {
	if fn := attribQuarantineHandler.Load(); fn != nil {
		(*fn)(sessionID, start)
	}
}

// noteAttributionOutcome is the same, for attrib.Attributor.WithOutcomeHook.
func noteAttributionOutcome(o attrib.Outcome) {
	if fn := attribOutcomeHandler.Load(); fn != nil {
		(*fn)(o)
	}
}

// vectorLedger is the vectorised pass's whole view of the ledger: one
// recorder, one method, one cell. See this file's header for why it is a
// distinct type holding a narrowed interface rather than a couple of methods
// on *v3 — a method on *v3 has `v.ledger` in scope, and `v.ledger.Failed(k,
// ledger.StageAttributed, …)` is exactly the line that cost 44 rows.
type vectorLedger struct{ rec ledger.VectorRecorder }

// vectorLedger narrows v3's store to the one interface the attribution path
// may hold. Returns a zero vectorLedger (nil recorder, every method a no-op)
// when there is no ledger at all, so no caller needs its own nil check.
func (v *v3) vectorLedger() vectorLedger {
	if v == nil || v.ledger == nil {
		return vectorLedger{}
	}
	return vectorLedger{rec: v.ledger}
}

// recordQuarantine is the terminal-failure half: a job that exhausted
// attrib.MaxAttempts genuine errors never got to answer for this block.
//
// ⚠️ **It writes the VECTOR cell, and the deterministic `attributed` cell is
// left byte-identical.** That is the whole correction. The two facts it must
// stay distinguishable from are an ABSENT vector cell (nobody asked — the
// toggle is off) and a vector cell reading `pending / weights_unavailable`
// (asked, and still waiting on a multi-gigabyte download). `failed /
// attribute_failed` means asked, tried four times, and given up on.
func (l vectorLedger) recordQuarantine(sessionID string, start float64) {
	l.write(sessionID, start, ledger.VectorAttributed{},
		ledger.StatusFailed, ledger.ReasonAttributeFailed)
}

// recordOutcome maps the sidecar's own closed status vocabulary onto the
// vector cell's. Every branch is argued, because the differences between them
// are the entire value of keeping a second cell at all:
//
//   - attributed — the pass ran and named a project. `ok`, with the id and the
//     confidence it named it with. Whether that agrees with the deterministic
//     cell is NOT decided here and is not decided anywhere: both ids are
//     stored, and a reader compares them. Choosing a winner needs data from a
//     machine running both passes, which does not exist until this ships.
//   - pending — the encoder is warming. `pending`, no reason: nothing has gone
//     wrong and a reason code would read as though something had.
//   - degraded:weights_unavailable — the weights are not provisioned yet. Also
//     `pending`, because the job is HELD and the work becomes doable when the
//     download finishes; `failed` would say a terminal thing about a
//     transient one. The reason names which wait it is.
//   - skipped:disabled — the sidecar's attribution is switched off, so nothing
//     was asked of the encoder. NOTHING IS WRITTEN: never-asked is exactly
//     what an absent cell means, and inventing a status here would put the
//     toggle-off population's blocks into a state the story says they must not
//     have. This is the same fact as the daemon-side toggle being off, reached
//     from the other side.
//   - skipped:no_projects — the pass ran and there was structurally nothing to
//     match against. `n/a`, the status this ledger already uses for "not
//     applicable rather than failed", never a failure: an org that has
//     declared no projects has not got something wrong.
//
// An unrecognised status writes nothing. The sidecar is frozen and shipped
// separately, so version skew is real; drainJob already treats an
// out-of-vocabulary status as a genuine error, and a second guess here would
// invent a cell state out of a string this side does not understand.
func (l vectorLedger) recordOutcome(o attrib.Outcome) {
	switch o.Status {
	case enrich.ProjectsAttributed:
		l.write(o.SessionID, o.Start,
			ledger.VectorAttributed{ProjectID: o.ProjectID, Confidence: o.Confidence},
			ledger.StatusOK, ledger.ReasonNone)
	case enrich.ProjectsPending:
		l.write(o.SessionID, o.Start, ledger.VectorAttributed{},
			ledger.StatusPending, ledger.ReasonNone)
	case enrich.ProjectsDegradedWeights:
		l.write(o.SessionID, o.Start, ledger.VectorAttributed{},
			ledger.StatusPending, ledger.ReasonWeightsUnavailable)
	case enrich.ProjectsSkippedNoProjects:
		l.write(o.SessionID, o.Start, ledger.VectorAttributed{},
			ledger.StatusNA, ledger.ReasonNone)
	}
}

// write is the one statement in this package that reaches the vector cell.
// Nil-safe on the recorder so a machine with no ledger costs nothing, and it
// drops a block whose start is not a whole second — the ledger keys on
// seconds and a rounded key would land the second opinion on a neighbouring
// block, which is worse than not recording it.
func (l vectorLedger) write(sessionID string, start float64, a ledger.VectorAttributed,
	status ledger.Status, r ledger.Reason) {
	if l.rec == nil || sessionID == "" {
		return
	}
	k := ledger.BlockKey{Session: sessionID, Start: int64(start)}
	l.rec.Vector(k, a, status, r, time.Now().UTC())
}
