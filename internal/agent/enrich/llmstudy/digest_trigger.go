package llmstudy

import (
	"os"
	"time"
)

// TriggerReason records why a refresh fired, so the decision is auditable rather
// than a mystery in a log.
type TriggerReason string

const (
	TriggerNone       TriggerReason = ""
	TriggerFirst      TriggerReason = "first"       // no digest exists yet
	TriggerFocusShift TriggerReason = "focus_shift" // the subject of the work changed
	TriggerUnsettled  TriggerReason = "unsettled"   // focus is diffuse — the work is moving
	TriggerFriction   TriggerReason = "friction"    // corrections appeared since last time
	TriggerVolume     TriggerReason = "volume"      // enough new turns to be worth re-reading
	TriggerStale      TriggerReason = "stale"       // a long quiet stretch; finalise
)

// TriggerPolicy decides when a digest is worth regenerating.
//
// A fixed "every N turns" cadence is wrong in both directions: it burns an inference
// on ten turns of "sure / yes / go on", and it lets a genuine change of subject sit
// unreported for ten turns. So the primary signal is how fast the work is CHANGING,
// read off the same EWMA focus the classification pipeline already maintains — no
// extra inference to decide, and none wasted deciding.
//
// MinTurns is a floor, not a cadence: it stops a shift being re-reported every turn
// while the focus is still settling.
//
// ⚠️ NOT VALIDATED BY ANY MEASUREMENT, and no doc may describe it as such. There is no
// non-test caller of TriggerPolicy or ShouldRefresh anywhere in the repository: the sweep
// (digest_eval_test.go) computes its trigger reason from SubjectShifted directly and never
// consults this policy, and production has not been wired to it. So the whole cascade —
// including the wall-clock MinInterval floor and the deferral of a suppressed reason — is
// exercised only by its own unit tests. Those tests are real (one of them derives
// PendingReason from a prior suppressed call's return value and found a contradiction in the
// design brief), but "the trigger policy was measured" would be false.
type TriggerPolicy struct {
	MinTurns       int     // never refresh sooner than this many new user turns
	MaxTurns       int     // refresh regardless once this many have accumulated
	UnsettledBelow float64 // concentration under this means the work is still moving
	StaleTurns     int     // treated as a finalisation point when work stops

	// MinInterval is a wall-clock floor on the only expensive operation here.
	//
	// Turn-based triggers alone do not bound cost: a burst of activity satisfies MaxTurns
	// repeatedly within minutes, and each firing is a full multi-section generation. The floor
	// applies to every reason, finalisation included — a session that has stopped is not going
	// anywhere, so producing its final account an hour later costs nothing.
	//
	// The cheap path is unaffected: the session record is deterministic and recomputed every
	// window, so a reader sees current counts beside an older narrative.
	MinInterval time.Duration
}

// DefaultTriggerPolicy is tuned for the observed shape of real sessions: p50 22 user
// turns, p90 59, max 91. MinTurns 3 keeps short acknowledgement runs from firing;
// MaxTurns 15 bounds how stale a digest can get on steady work.
func DefaultTriggerPolicy() TriggerPolicy {
	return TriggerPolicy{
		MinTurns: 3, MaxTurns: 15, UnsettledBelow: 0.5, StaleTurns: 40,
		MinInterval: MinIntervalFromEnv(),
	}
}

// TriggerState is what the caller carries between decisions. All of it is cheap to
// persist alongside the digest snapshot.
type TriggerState struct {
	HasDigest        bool
	TurnsSince       int     // user turns since the last digest
	PrevFocusDomain  string  // focus argmax when the last digest was written
	PrevFocusFunc    string  //
	CorrectionsSince int     // corrections observed since the last digest
	Concentration    float64 // current focus concentration, in [0,1]
	FocusDomain      string  // current focus argmax
	FocusFunc        string  //

	// Now and LastDigestAt drive the floor. Passed in rather than read from the clock so the
	// policy stays pure and testable.
	Now          time.Time
	LastDigestAt time.Time

	// PendingReason is the strongest reason the floor has already suppressed. Deferring
	// rather than dropping is what stops a focus shift shortly after a digest from being
	// reported later as mere volume.
	PendingReason TriggerReason
}

// ShouldRefresh decides whether to regenerate, and says why.
//
// TriggerFirst is decided before anything else and is never subject to the wall-clock
// floor below it: there is no prior digest for it to be stale relative to.
//
// The second return value is NOT "the reason we are refreshing" — it is "the reason,
// whether or not we are refreshing." When the floor suppresses a refresh (ok == false),
// the reason is still returned so the caller can persist it as the next TriggerState's
// PendingReason; that is the only channel by which a suppressed cause survives to be
// the reported cause once the floor elapses (see TestSuppressedReasonSurvivesARoundTrip).
// A caller MUST key off the bool, not the presence of a reason: a non-empty reason with
// ok == false is advisory, not permission to generate.
func (p TriggerPolicy) ShouldRefresh(s TriggerState) (bool, TriggerReason) {
	if !s.HasDigest {
		return true, TriggerFirst
	}
	computed := p.reasonFor(s)
	effective := strongerReason(s.PendingReason, computed)
	if effective == TriggerNone {
		return false, TriggerNone
	}
	if p.MinInterval > 0 && !s.LastDigestAt.IsZero() &&
		s.Now.Sub(s.LastDigestAt) < p.MinInterval {
		// Suppressed, not dropped. The caller persists `effective` as PendingReason and the
		// periodic sweep re-evaluates — without a timer a session that goes quiet with a
		// pending reason would never fire.
		return false, effective
	}
	return true, effective
}

// reasonFor is the turn-based cascade, decided independently of the wall-clock floor.
//
// Order matters: a change of subject is the most valuable moment to re-read, and
// friction is the second, because a digest written before a correction will claim
// things went well. Volume is the fallback so a steady session still gets refreshed.
func (p TriggerPolicy) reasonFor(s TriggerState) TriggerReason {
	// A long quiet stretch is a finalisation point, and it bypasses MinTurns because
	// the session may simply have ended.
	if p.StaleTurns > 0 && s.TurnsSince >= p.StaleTurns {
		return TriggerStale
	}
	if s.TurnsSince < p.MinTurns {
		return TriggerNone
	}
	// The subject of the work changed — the case a fixed cadence reports late.
	if (s.FocusDomain != "" && s.FocusDomain != s.PrevFocusDomain) ||
		(s.FocusFunc != "" && s.FocusFunc != s.PrevFocusFunc) {
		return TriggerFocusShift
	}
	// Corrections since the last digest mean the previous one is now misleading:
	// it was written before the work went wrong.
	if s.CorrectionsSince > 0 {
		return TriggerFriction
	}
	// A diffuse focus means the work is moving even if the argmax has not flipped.
	if s.Concentration > 0 && s.Concentration < p.UnsettledBelow {
		return TriggerUnsettled
	}
	if p.MaxTurns > 0 && s.TurnsSince >= p.MaxTurns {
		return TriggerVolume
	}
	return TriggerNone
}

// strongerReason ranks reasons so a deferred cause is not downgraded while it waits.
func strongerReason(a, b TriggerReason) TriggerReason {
	rank := map[TriggerReason]int{
		TriggerNone: 0, TriggerVolume: 1, TriggerUnsettled: 2,
		TriggerFriction: 3, TriggerFocusShift: 4, TriggerStale: 5, TriggerFirst: 6,
	}
	if rank[a] >= rank[b] {
		return a
	}
	return b
}

// MinIntervalFromEnv reads KELD_DIGEST_MIN_INTERVAL, defaulting to an hour.
func MinIntervalFromEnv() time.Duration {
	if v := os.Getenv("KELD_DIGEST_MIN_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return d
		}
	}
	return time.Hour
}
