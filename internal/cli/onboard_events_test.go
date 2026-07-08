package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/console"
)

func TestEmitEventWritesOneNDJSONLine(t *testing.T) {
	var buf bytes.Buffer
	old := console.Out
	console.Out = &buf
	defer func() { console.Out = old }()

	emitEvent(deviceCodeEvent{
		Event:           "device_code",
		VerificationURL: "https://v",
		UserCode:        "WXYZ-1234",
		ExpiresIn:       900,
		Interval:        5,
	})
	emitEvent(authorizedEvent{Event: "authorized", Principal: "dg@keld.co", Org: "acme"})

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 NDJSON lines, got %d: %q", len(lines), buf.String())
	}
	want0 := `{"event":"device_code","verification_url":"https://v","user_code":"WXYZ-1234","expires_in":900,"interval":5}`
	if lines[0] != want0 {
		t.Fatalf("line0=\n%s\nwant\n%s", lines[0], want0)
	}
	want1 := `{"event":"authorized","principal":"dg@keld.co","org":"acme"}`
	if lines[1] != want1 {
		t.Fatalf("line1=\n%s\nwant\n%s", lines[1], want1)
	}
}
