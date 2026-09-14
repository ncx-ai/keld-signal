package settings

import (
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
	if want != "" && s.AtlasEnabled() {
		return "", true
	}
	return want, false
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
