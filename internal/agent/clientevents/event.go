package clientevents

import "time"

const SchemaVersion = 1

type Severity string

const (
	SevInfo     Severity = "info"
	SevWarn     Severity = "warn"
	SevError    Severity = "error"
	SevCritical Severity = "critical"
)

// rank returns the ordinal rank of a severity level. info=0 < warn=1 < error=2 < critical=3;
// unknown severities rank below SevInfo (e.g., -1) so they never pass a floor check.
func (s Severity) rank() int {
	switch s {
	case SevInfo:
		return 0
	case SevWarn:
		return 1
	case SevError:
		return 2
	case SevCritical:
		return 3
	default:
		return -1
	}
}

// AtLeast returns true iff the severity s is at least the minimum severity min.
func (s Severity) AtLeast(min Severity) bool {
	return s.rank() >= min.rank()
}

// Corr holds correlation metadata for an event.
type Corr struct {
	Org       string `json:"org,omitempty"`
	Actor     string `json:"actor,omitempty"`
	InstallID string `json:"install_id"`
	RunID     string `json:"run_id"`
	SessionID string `json:"session_id,omitempty"`
	PromptID  string `json:"prompt_id,omitempty"`
	Version   string `json:"version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// Event represents a structured client event.
type Event struct {
	Code     string         `json:"code"`
	Severity Severity       `json:"severity"`
	Fields   map[string]any `json:"fields,omitempty"`
	Corr     Corr           `json:"corr"`
	TS       time.Time      `json:"ts"`
}
