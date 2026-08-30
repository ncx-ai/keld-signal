package service

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"
	"unicode/utf16"
)

// These tests deliberately carry NO build tag. The code under test is only ever
// CALLED on Windows, but CI runs on Linux — tagging them windows would mean the
// document that every Windows install depends on is never exercised anywhere.

// The registration must NOT use `/SC ONLOGON`'s any-user trigger. That is the
// whole defect: it needs elevation, the installer never has it, and the task was
// silently never created. The trigger has to name a user.
func TestLogonTriggerIsScopedToAUser(t *testing.T) {
	got := taskXMLFor("KingR", `C:\Users\KingR\AppData\Local\Programs\keld\keld-agent.exe`)
	if !strings.Contains(got, "<LogonTrigger>") {
		t.Fatal("no LogonTrigger: the task would not start at logon")
	}
	if !strings.Contains(got, "<UserId>KingR</UserId>") {
		t.Fatalf("LogonTrigger is not scoped to a user — an any-user trigger needs admin:\n%s", got)
	}
}

// A malformed document is rejected by schtasks with a parse error naming nothing
// useful, so an account name or install path containing & must be escaped.
func TestOperandsAreEscapedSoTheDocumentStaysWellFormed(t *testing.T) {
	got := taskXMLFor(`R&D\bob`, `C:\Program Files\A & B\keld-agent.exe`)
	if strings.Contains(got, "A & B") {
		t.Error("raw & reached the document; schtasks would reject it as malformed")
	}
	// A CharsetReader passthrough is required, not a workaround: the document
	// declares UTF-16 (which is what schtasks needs on disk) while `got` is
	// already decoded runes, and Go's decoder refuses that declaration outright
	// without one. Nothing here transcodes; the bytes-match-declaration check
	// lives in TestBytesMatchTheDeclaredEncoding.
	d := xml.NewDecoder(strings.NewReader(got))
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	for {
		_, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("document is not well-formed XML: %v\n%s", err, got)
		}
	}
}

// The daemon has no single-instance guard of its own (its ingress binds an
// ephemeral port), so the scheduler must not be a source of second instances.
func TestSchedulerWillNotStartASecondInstance(t *testing.T) {
	got := taskXMLFor("u", `C:\keld-agent.exe`)
	if !strings.Contains(got, "<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>") {
		t.Error("missing IgnoreNew: a logon while the daemon runs would start a second one")
	}
}

// The default execution time limit is 72 hours, which would terminate a healthy
// daemon. PT0S is "no limit".
func TestDaemonIsNotKilledAfterThreeDays(t *testing.T) {
	if !strings.Contains(taskXMLFor("u", "x"), "<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>") {
		t.Error("missing PT0S: Task Scheduler would stop the daemon after 72 hours")
	}
}

// The document declares encoding="UTF-16", so the bytes must actually be UTF-16
// with a BOM. Writing UTF-8 under that declaration is a file that lies about
// itself, and schtasks rejects it.
func TestBytesMatchTheDeclaredEncoding(t *testing.T) {
	b := utf16LEWithBOM("<Task/>")
	if len(b) < 2 || b[0] != 0xFF || b[1] != 0xFE {
		t.Fatalf("missing little-endian BOM, got % x", b[:min(2, len(b))])
	}
	if (len(b)-2)%2 != 0 {
		t.Fatal("body is not a whole number of UTF-16 code units")
	}
	units := make([]uint16, 0, (len(b)-2)/2)
	for i := 2; i < len(b); i += 2 {
		units = append(units, uint16(b[i])|uint16(b[i+1])<<8)
	}
	if got := string(utf16.Decode(units)); got != "<Task/>" {
		t.Fatalf("round-trip = %q, want %q", got, "<Task/>")
	}
}
