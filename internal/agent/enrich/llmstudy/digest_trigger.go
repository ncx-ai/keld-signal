package llmstudy

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
type TriggerPolicy struct {
	MinTurns       int     // never refresh sooner than this many new user turns
	MaxTurns       int     // refresh regardless once this many have accumulated
	UnsettledBelow float64 // concentration under this means the work is still moving
	StaleTurns     int     // treated as a finalisation point when work stops
}

// DefaultTriggerPolicy is tuned for the observed shape of real sessions: p50 22 user
// turns, p90 59, max 91. MinTurns 3 keeps short acknowledgement runs from firing;
// MaxTurns 15 bounds how stale a digest can get on steady work.
func DefaultTriggerPolicy() TriggerPolicy {
	return TriggerPolicy{MinTurns: 3, MaxTurns: 15, UnsettledBelow: 0.5, StaleTurns: 40}
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
}

// ShouldRefresh decides whether to regenerate, and says why.
//
// Order matters: a change of subject is the most valuable moment to re-read, and
// friction is the second, because a digest written before a correction will claim
// things went well. Volume is the fallback so a steady session still gets refreshed.
func (p TriggerPolicy) ShouldRefresh(s TriggerState) (bool, TriggerReason) {
	if !s.HasDigest {
		return true, TriggerFirst
	}
	// A long quiet stretch is a finalisation point, and it bypasses MinTurns because
	// the session may simply have ended.
	if p.StaleTurns > 0 && s.TurnsSince >= p.StaleTurns {
		return true, TriggerStale
	}
	if s.TurnsSince < p.MinTurns {
		return false, TriggerNone
	}
	// The subject of the work changed — the case a fixed cadence reports late.
	if (s.FocusDomain != "" && s.FocusDomain != s.PrevFocusDomain) ||
		(s.FocusFunc != "" && s.FocusFunc != s.PrevFocusFunc) {
		return true, TriggerFocusShift
	}
	// Corrections since the last digest mean the previous one is now misleading:
	// it was written before the work went wrong.
	if s.CorrectionsSince > 0 {
		return true, TriggerFriction
	}
	// A diffuse focus means the work is moving even if the argmax has not flipped.
	if s.Concentration > 0 && s.Concentration < p.UnsettledBelow {
		return true, TriggerUnsettled
	}
	if p.MaxTurns > 0 && s.TurnsSince >= p.MaxTurns {
		return true, TriggerVolume
	}
	return false, TriggerNone
}
