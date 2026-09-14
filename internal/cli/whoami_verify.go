package cli

import (
	"errors"
	"net/http"

	"github.com/ncx-ai/keld-signal/internal/api"
	"github.com/ncx-ai/keld-signal/internal/auth"
	"github.com/ncx-ai/keld-signal/internal/paths"
	"github.com/ncx-ai/keld-signal/internal/retry"
)

// ⚠️ `keld whoami` reads auth.json and prints it — it has never asked Atlas
// whether the credential still works, so a revoked token and a live one are
// indistinguishable to it. That is fine for a human reading a line of text and
// useless for a caller deciding whether a machine is set up.
//
// The macOS installer's wizard pane is such a caller: it enables Continue only
// for a machine that is genuinely connected, and the install is all-or-nothing
// — there is no "set up later". An "already paired" claim resting on a file's
// existence would let someone install with a dead credential and collect
// nothing, which is exactly the confused state the wizard exists to prevent.
//
// Verification is the SAME call `keld signal setup` makes minutes later in
// postinstall (`Onboarding()`), so a verified result predicts that step rather
// than merely correlating with it.
type identityStatus string

const (
	identityVerified     identityStatus = "verified"
	identityUnauthorized identityStatus = "unauthorized"
	identityUnreachable  identityStatus = "unreachable"
	identityNone         identityStatus = "none"
)

// identityEvent is the NDJSON line `keld whoami --verify --json` emits.
type identityEvent struct {
	Event     string         `json:"event"`
	Status    identityStatus `json:"status"`
	Principal string         `json:"principal,omitempty"`
	Org       string         `json:"org,omitempty"`
	APIURL    string         `json:"api_url,omitempty"`
	Message   string         `json:"message,omitempty"`
}

// verifyIdentity asks Atlas whether the stored credential is still good.
//
// ⚠️ THE THREE FAILURE STATES ARE NOT INTERCHANGEABLE. A 401/403 means the
// credential is dead and a new setup code is required; a transport failure or a
// 5xx means we learned nothing about the credential at all. Collapsing them
// would either nag a connected machine for a code or, worse, report a revoked
// token as connected because Atlas happened to be down.
func verifyIdentity(a *auth.AuthData) identityEvent {
	ev := identityEvent{Event: "identity"}
	if a == nil || a.AccessToken == "" {
		ev.Status = identityNone
		return ev
	}
	ev.Principal, ev.Org, ev.APIURL = a.Principal, a.Org, a.APIURL

	base := a.APIURL
	if base == "" {
		base = paths.APIBase()
	}
	if _, err := api.NewClient(base, a.AccessToken).Onboarding(); err != nil {
		var se *retry.StatusError
		if errors.As(err, &se) && (se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden) {
			ev.Status = identityUnauthorized
		} else {
			ev.Status = identityUnreachable
		}
		ev.Message = cleanErrorMessage(err)
		return ev
	}
	ev.Status = identityVerified
	return ev
}
