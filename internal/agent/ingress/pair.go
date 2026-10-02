package ingress

import (
	"strings"
	"sync/atomic"

	"github.com/ncx-ai/keld-signal/internal/api"
	"github.com/ncx-ai/keld-signal/internal/auth"
	"github.com/ncx-ai/keld-signal/internal/config"
)

// pairStage names which step of pair() failed, so the browser sign-in's
// return can say which one in its log line and pick its fixed page.
type pairStage int

const (
	pairLogin      pairStage = iota // Atlas refused or never answered the redeem
	pairOnboarding                  // the redeem worked; fetching the ingest token did not
	pairHookWrite                   // hook.json could not be written
)

// String is the stage's name in a log line.
func (s pairStage) String() string {
	switch s {
	case pairLogin:
		return "login"
	case pairOnboarding:
		return "onboarding"
	default:
		return "hook_write"
	}
}

type pairError struct {
	stage pairStage
	err   error
	// secrets are what this pairing handled — the code, the verifier, the
	// tokens Atlas handed back — for redacted() to scrub.
	secrets []string
}

func (e *pairError) Error() string { return e.err.Error() }
func (e *pairError) Unwrap() error { return e.err }

// redacted is the error as ONE log line with every secret pair() handled, and
// any the caller adds, replaced. An Atlas error body is quoted into the error
// (api.checkStatus keeps 200 bytes of it), and Atlas may echo what it was sent.
func (e *pairError) redacted(more ...string) string {
	msg := strings.ReplaceAll(e.err.Error(), "\n", "; ")
	for _, sec := range append(append([]string(nil), e.secrets...), more...) {
		if sec != "" {
			msg = strings.ReplaceAll(msg, sec, "[redacted]")
		}
	}
	return msg
}

// pair is THE ONE WAY this daemon pairs itself with an Atlas: redeem the code
// at base (with the PKCE verifier when the code came from a browser sign-in),
// fetch the onboarding hand-off, and write auth.json + hook.json. The browser
// sign-in's GET /auth/callback calls it; the page's setup-code box (POST
// /v1/config) was its other caller until it was removed with that box. A setup
// code still pairs from a terminal, through auth.LoginWithCode in `keld login
// --code` and `keld-agent install --code`. Every error is a *pairError.
func pair(base, code, verifier string) (*auth.AuthData, error) {
	a, err := auth.LoginWithCodeVerifier(api.NewClient(base, ""), code, verifier)
	if err != nil {
		return nil, &pairError{stage: pairLogin, err: err, secrets: []string{code, verifier}}
	}
	ob, err := api.NewClient(a.APIURL, a.AccessToken).Onboarding()
	if err != nil {
		return nil, &pairError{stage: pairOnboarding, err: err, secrets: []string{code, verifier, a.AccessToken}}
	}
	if err := config.SaveHookConfig(ob.Endpoint, ob.IngestToken); err != nil {
		return nil, &pairError{stage: pairHookWrite, err: err, secrets: []string{code, verifier, a.AccessToken, ob.IngestToken}}
	}
	if f := onPaired.Load(); f != nil {
		(*f)()
	}
	return a, nil
}

// onPaired is told each time pair() has written hook.json, so the daemon's
// pairing watcher adopts it now rather than on its next poll — the page reads
// the health strip right after "Signed in", and a watcher still asleep left it
// saying "Atlas not paired". It must not block: pair() runs inside a request.
var onPaired atomic.Pointer[func()]

// SetOnPaired installs the pairing announcement (nil removes it).
func SetOnPaired(f func()) {
	if f == nil {
		onPaired.Store(nil)
		return
	}
	onPaired.Store(&f)
}
