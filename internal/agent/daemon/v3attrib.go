package daemon

import (
	"sync/atomic"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
)

// attribQuarantineHandler is how the attribution path's terminal quarantine
// event reaches the ledger, without adding a parameter to startAttributor's
// signature — the same package-level-accessor shape sidecarProbe/
// setSidecarProbe (v3health.go) already use, and for the identical reason:
// the Attributor is built well after v3 exists, deep inside wireEnrichment's
// conditional branch, and every existing call site of startAttributor
// (attrib_test.go's fixed-arity calls included) stays unchanged.
var attribQuarantineHandler atomic.Pointer[func(sessionID string, start float64)]

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

// noteAttributionQuarantine is the nil-safe hook startAttributor wires onto
// the Attributor (attrib.Attributor.WithQuarantineHook). It forwards to
// whatever v3 registered at startup, or does nothing on a machine — or a
// test — where nothing ever called setAttribQuarantineHandler.
func noteAttributionQuarantine(sessionID string, start float64) {
	if fn := attribQuarantineHandler.Load(); fn != nil {
		(*fn)(sessionID, start)
	}
}

// recordAttributeQuarantined is v3's half of the hook above: a job that
// exhausted attrib.MaxAttempts genuine errors is a terminal, observable fact
// about this block — the deterministic attribution pass never got a chance to
// answer at all, which is a different fact from ReasonNoRuleMatched (it ran
// and found nothing) and from an absent `attributed` cell (Attribute was
// never called for this block). It reads as FAILED with ReasonAttributeFailed.
func (v *v3) recordAttributeQuarantined(sessionID string, start float64) {
	if v == nil || v.ledger == nil {
		return
	}
	k := ledger.BlockKey{Session: sessionID, Start: int64(start)}
	v.ledger.Failed(k, ledger.StageAttributed, ledger.ReasonAttributeFailed, 0, time.Now().UTC())
}
