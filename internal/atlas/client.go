// Package atlas is THE BOUNDARY between Signal and the cloud.
//
// Everything that talks to Keld Atlas goes through the Client interface here,
// and the daemon constructs exactly one of two implementations from the
// `send_to_atlas` setting: Live (the real publisher, settings poll, workstreams
// and code redemption) or Off (nothing, ever). That is what makes
// "Signal runs with or without Atlas" a package boundary rather than a
// scattering of `if enabled` checks — and it is what lets the local half be
// open source without a fork, and lets this build iterate on the page without
// touching the integration (docs/v3/contracts.md, D4).
//
// ⚠️ **Off must make outbound traffic IMPOSSIBLE, not merely unlikely.** It
// holds no HTTP client, no token and no URL; a test drives the daemon for an
// hour with a dialer that fails the test on any connection. A boolean checked
// at each call site would have been one forgotten branch away from a machine
// that promised local-only and phoned home anyway.
package atlas

import (
	"context"
	"errors"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// ErrOffline is returned by every Off method that would otherwise have reached
// the network. Callers treat it as "not applicable", never as a failure to
// retry: the ledger records such a cell as StatusNA with ReasonAtlasOff, so the
// page shows no Atlas column at all rather than a column of crosses.
var ErrOffline = errors.New("atlas: send to atlas is off")

// Workstream is an org bucket as Atlas serves it: a classifying question with
// predefined values. A VALUE IS A PROJECT — see docs/v3/contracts.md.
type Workstream struct {
	Key      string  `json:"key"`
	Name     string  `json:"name"`
	Question string  `json:"question"`
	Template string  `json:"template_id,omitempty"`
	Values   []Value `json:"values"`
}

// Value is one project inside a workstream. Tags carry the deterministic rules
// (`repo:github.com/org/name`, `ticket:KELD`), which is what makes a "same as"
// answer teach the whole org rather than one machine.
type Value struct {
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// Paired is the outcome of redeeming a setup code.
type Paired struct {
	Host            string
	RestartRequired bool
}

// Client is everything Signal may do to Atlas. Nothing else in the daemon may
// import internal/agent/publish or the settings poll directly once D4 lands.
type Client interface {
	// Enabled reports whether this is the live connector. The page uses it to
	// decide whether Atlas columns exist at all; nothing else should branch on
	// it, because the methods below already do the right thing when off.
	Enabled() bool

	// SendBlocks publishes block enrichments. The returned status is the HTTP
	// status Atlas answered with (0 when none was reached), which is what the
	// ledger records as `received` — delivery is confirmed from the RESPONSE,
	// never from the absence of an error, because a captive portal answers 200
	// with an HTML login page.
	SendBlocks(ctx context.Context, rows []publish.BlockEnrichment) (status int, err error)

	// Settings fetches the org's enrichment settings. A nil error with a zero
	// Remote means "reached, nothing configured"; ErrOffline means "not asked".
	Settings(ctx context.Context) (settings.Remote, error)

	// Workstreams returns the org's buckets and their values — the attribution
	// vocabulary.
	Workstreams(ctx context.Context) ([]Workstream, error)

	// PatchWorkstream replaces a workstream's values. This is how a "same as"
	// or a "new project" answer reaches the org: PATCH /v1/workstreams/{key}.
	PatchWorkstream(ctx context.Context, key string, values []Value) error

	// RedeemCode runs the existing login+setup path for a setup code, which may
	// carry the host that minted it ("atlas-dev.example/ABCD-EFGH").
	RedeemCode(ctx context.Context, code string) (Paired, error)

	// LastResponse is the class of the most recent answer Atlas gave, for the
	// health strip: the HTTP status and when. Zero status means "not tried yet".
	LastResponse() (status int, at time.Time)
}

// Off is the Client for `send_to_atlas: false`. It holds no transport, no
// credential and no address.
type Off struct{}

func (Off) Enabled() bool { return false }
func (Off) SendBlocks(context.Context, []publish.BlockEnrichment) (int, error) {
	return 0, ErrOffline
}
func (Off) Settings(context.Context) (settings.Remote, error) { return settings.Remote{}, ErrOffline }
func (Off) Workstreams(context.Context) ([]Workstream, error) { return nil, ErrOffline }
func (Off) PatchWorkstream(context.Context, string, []Value) error {
	return ErrOffline
}
func (Off) RedeemCode(context.Context, string) (Paired, error) { return Paired{}, ErrOffline }
func (Off) LastResponse() (int, time.Time)                     { return 0, time.Time{} }

var _ Client = Off{}
