package ledger

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ⚠️ **The privacy claim is "no text, span, offset or path can enter this
// file", and the API's defence is that there is no free-text parameter.** That
// is a strong argument for the REASON fields and no argument at all for the
// identifier fields, which are strings a caller supplies: session, source,
// model and project id. This test attacks those.
//
// The point is not that a caller would deliberately put a prompt in a model
// name. It is that the ledger is written from hook points scattered across the
// daemon, and one of them handing over the wrong variable — a transcript path
// into `source`, a prompt fragment into `model` — must be caught here rather
// than discovered in a support bundle.
func TestIdentifierFieldsCannotSmuggleTextOrPaths(t *testing.T) {
	setHome(t)
	s := New()
	now := time.Now().UTC()

	const secret = "please summarise /Users/gabriel/projects/keld/secret-plan.md"
	k := BlockKey{Session: secret, Start: 1788543000}
	s.Cut(k, 1788544200, "idle", "budget", secret, now)
	s.Measure(k, Measured{Model: secret, Requests: 1}, now)
	s.Attribute(k, Attributed{ProjectID: secret, Method: MethodRepo}, ReasonNone, now)

	snap, err := s.Read(time.Time{}, 100)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	buf, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(buf)

	// A path must never survive into the file, whichever field it arrived in.
	for _, forbidden := range []string{"/Users/", "secret-plan.md", "summarise"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the ledger published %q; identifier fields must be bounded so a\n"+
				"mis-wired hook point cannot put text or a path on this surface.\nGot: %s",
				forbidden, body)
		}
	}
}

// A newline in an identifier would break any line-oriented reader of this
// surface (a support bundle, a log grep) and is never legitimate.
func TestIdentifiersRejectNewlines(t *testing.T) {
	setHome(t)
	s := New()
	now := time.Now().UTC()
	k := BlockKey{Session: "sess\nid", Start: 1}
	s.Cut(k, 2, "idle", "budget", "claude_code\nx", now)

	snap, _ := s.Read(time.Time{}, 100)
	buf, _ := json.Marshal(snap)
	if strings.Contains(string(buf), "\\n") {
		t.Errorf("a newline reached the ledger: %s", buf)
	}
}
