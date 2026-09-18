package settings

import (
	"net"
	"net/url"

	"github.com/ncx-ai/keld-signal/internal/paths"
	"os"
	"strings"
)

// Env overrides for the v3 keys (docs/v3/contracts.md). Env wins over the
// file, as everywhere else in this package.
const (
	AtlasEnv     = "KELD_ATLAS"      // "0" → connector off, "1" → on
	DevBlocksEnv = "KELD_DEV_BLOCKS" // "", "prompt", "bin", "minute"
)

// DevBlocksModes is the closed set the sidecar understands. "" is the
// production cutter.
var DevBlocksModes = []string{"", "prompt", "bin", "minute"}

// AtlasEnabled is the Send to Atlas toggle resolved: env, then file, then ON.
// Absent means on because Atlas is what an installed daemon is for; the toggle
// exists so a developer — or an open-source user — can run without it.
func (s Settings) AtlasEnabled() bool {
	switch strings.TrimSpace(os.Getenv(AtlasEnv)) {
	case "0", "false", "off":
		return false
	case "1", "true", "on":
		return true
	}
	if s.SendToAtlas != nil {
		return *s.SendToAtlas
	}
	return true
}

// DevBlocksMode is the developer granularity that is SAFE to use: the
// requested mode when Atlas is off, and always "" (production) when Atlas is
// on, whatever the file or env says. Refusing here rather than at the UI means
// no code path can read an unsafe value — a minute-long block must never be
// published as a fact about someone's work. The second return says whether a
// request was refused, so the page can explain it.
func (s Settings) DevBlocksMode() (mode string, refused bool) {
	want := strings.TrimSpace(os.Getenv(DevBlocksEnv))
	if want == "" {
		want = strings.TrimSpace(s.DevBlocks)
	}
	if !validDevBlocks(want) {
		want = ""
	}
	// ⚠️ **THE RULE IS ABOUT WHERE A BLOCK LANDS, NOT WHETHER PUBLISHING IS
	// ON**, and it used to be written as the latter. A dev granularity
	// MISLABELS real work — a minute-long block is a false statement about
	// something a person actually did — so it must never reach the ORG'S
	// NUMBERS. Those live behind the real Atlas. A LOOPBACK endpoint is a mock
	// on this machine: there are no org numbers there to corrupt, and
	// publishing is precisely what a conformance run has to exercise.
	//
	// Conflating the two had a concrete cost: the conformance chain could not
	// produce a block AT ALL, because a run lasts seconds and the cutter closes
	// nothing under 20 minutes. So `publish` passed on enrichments alone and
	// the one signal Atlas actually RENDERS went untested on every tool and
	// every platform — the same silent half-working shape as the sidecar skew
	// that cost three weeks of blocks.
	if want != "" && s.AtlasEnabled() && !atlasIsLoopback() {
		return "", true
	}
	return want, false
}

// atlasIsLoopback reports whether the configured Atlas is a mock on this
// machine rather than a real one.
//
// ⚠️ Parsed as a URL and matched on the HOST, never on a substring: a hostname
// that merely CONTAINS "localhost" — say `localhost.evil.example.com` — is a
// perfectly ordinary public name, and a Contains check would hand it a
// granularity that misstates real work. A test pins that case.
func atlasIsLoopback() bool {
	u, err := url.Parse(paths.APIBase())
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validDevBlocks(m string) bool {
	for _, k := range DevBlocksModes {
		if k == m {
			return true
		}
	}
	return false
}

// WorkstreamOff reports whether a workstream key is excluded from attribution
// on this machine.
func (s Settings) WorkstreamOff(key string) bool {
	for _, k := range s.WorkstreamsOff {
		if strings.EqualFold(strings.TrimSpace(k), strings.TrimSpace(key)) {
			return true
		}
	}
	return false
}
