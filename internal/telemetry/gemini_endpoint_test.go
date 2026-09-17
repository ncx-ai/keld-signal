package telemetry

import "testing"

// ⚠️ **THE TOKEN USED TO RIDE THE QUERY STRING, AND EVERY GEMINI EXPORT FAILED.**
// The SDK composes its signal URL by PLAIN STRING CONCATENATION over a base it
// first normalises through `new URL(...).href`:
//
//	"http://127.0.0.1:14318?token=SECRET"  ->  href adds the root slash
//	"http://127.0.0.1:14318/?token=SECRET" + "/v1/logs"
//	= "http://127.0.0.1:14318/?token=SECRET/v1/logs"
//
// — path "/", token "SECRET/v1/logs". Measured on gemini-cli 0.37.1 against a
// live proxy: alternating 404 and 401 on every export, printed as raw stack
// traces in the user's terminal.
//
// This test asserts the property that matters, not the string: whatever we
// write, APPENDING "/v1/logs" to it must yield a URL whose path ends in
// "/v1/logs" and whose token is still readable.
func TestGeminiEndpointSurvivesTheSDKsPathAppend(t *testing.T) {
	const tok = "SECRET"
	for _, base := range []string{
		"http://127.0.0.1:14318",
		"http://127.0.0.1:14318/",
		"https://atlas.keld.co",
		// An endpoint already carrying the broken query form must not keep it.
		"http://127.0.0.1:14318?token=stale",
	} {
		got := endpointWithToken(base, tok)
		if want := "/t/" + tok; got[len(got)-len(want):] != want {
			t.Errorf("endpointWithToken(%q) = %q, want it to end in %q", base, got, want)
		}
		if containsStr(got, "?") {
			t.Errorf("endpointWithToken(%q) = %q — a query string does not survive the append", base, got)
		}
		// What the SDK actually builds.
		if composed := got + "/v1/logs"; composed[len(composed)-len("/t/SECRET/v1/logs"):] != "/t/SECRET/v1/logs" {
			t.Errorf("composed = %q, want the token intact before the signal path", composed)
		}
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
