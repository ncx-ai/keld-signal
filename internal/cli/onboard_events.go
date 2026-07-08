package cli

import (
	"encoding/json"
	"fmt"

	"github.com/ncx-ai/keld-signal/internal/console"
)

// NDJSON event payloads emitted by the --json onboarding modes. Field order is
// the struct order; encoding/json preserves it.
type deviceCodeEvent struct {
	Event           string `json:"event"`
	VerificationURL string `json:"verification_url"`
	UserCode        string `json:"user_code"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type authorizedEvent struct {
	Event     string `json:"event"`
	Principal string `json:"principal"`
	Org       string `json:"org"`
}

type toolEvent struct {
	Event   string `json:"event"`
	Name    string `json:"name"`
	Display string `json:"display"`
	Action  string `json:"action"` // configured | already_configured | skipped_conflict
	Path    string `json:"path"`
	Backup  string `json:"backup,omitempty"`
}

type doneEvent struct {
	Event      string `json:"event"`
	Configured int    `json:"configured"`
	Endpoint   string `json:"endpoint,omitempty"`
}

type errorEvent struct {
	Event   string `json:"event"`
	Message string `json:"message"`
}

// SetupEvent is the wire-agnostic progress event runSetup passes to SetupOpts.Emit.
// The command layer maps it to toolEvent/doneEvent NDJSON.
type SetupEvent struct {
	Kind       string // "tool" | "done"
	Name       string
	Display    string
	Action     string
	Path       string
	Backup     string
	Configured int
	Endpoint   string
}

// emitEvent marshals v and writes it as one NDJSON line to console.Out.
func emitEvent(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Fprintln(console.Out, string(b))
}
