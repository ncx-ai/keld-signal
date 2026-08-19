package auth

import (
	"strings"

	"github.com/ncx-ai/keld-signal/internal/errs"
)

// ParsePairingCode splits a setup code that may carry the Keld host that minted
// it. "atlas.keld.co/ABCD-EFGH" yields ("https://atlas.keld.co", "ABCD-EFGH");
// a bare "ABCD-EFGH" yields ("", "ABCD-EFGH") so the caller falls back to its
// normal API-base resolution and codes minted by an older Atlas keep working.
//
// The split is on the LAST "/", which is unambiguous because a setup code never
// contains one — it is XXXX-XXXX. That handles a scheme ("https://host/CODE")
// and a path-bearing deploy ("https://host/keld/CODE") with no special cases.
//
// A trailing slash is deliberately NOT trimmed: trimming it would turn
// "atlas.keld.co/" into something indistinguishable from a bare code, which
// would then be uppercased and sent to the server as "ATLAS.KELD.CO". Leaving
// it produces an empty code segment and a clear error instead.
func ParsePairingCode(s string) (apiBase, code string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", errs.New("setup code is empty")
	}
	host := ""
	if i := strings.LastIndex(s, "/"); i >= 0 {
		host, code = s[:i], s[i+1:]
	} else {
		code = s
	}
	if code == "" {
		return "", "", errs.New(`invalid setup code (expected "ABCD-EFGH" or "atlas.keld.co/ABCD-EFGH")`)
	}
	// The server's alphabet is uppercase and /v1/cli/enroll looks the code up as a
	// raw Redis key, so a lowercase-typed code would 410. No lowercase code is ever
	// minted, so uppercasing cannot collide. The host is left exactly as typed.
	code = strings.ToUpper(code)
	if host == "" {
		return "", code, nil
	}
	if !strings.Contains(host, "://") {
		scheme := "https://"
		if isLoopbackHost(host) {
			scheme = "http://"
		}
		host = scheme + host
	}
	return strings.TrimRight(host, "/"), code, nil
}

// isLoopbackHost reports whether a scheme-less host[:port][/path] names the
// local machine, which is served over http in dev.
func isLoopbackHost(host string) bool {
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	return host == "localhost" || host == "127.0.0.1"
}
