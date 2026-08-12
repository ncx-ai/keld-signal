package llmstudy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// splitFake is a llama-server stand-in for the three-pass path: it answers the entity pass, then
// the event pass, then the composition pass, and records the prompt each one was sent.
func splitFake(t *testing.T, entities, events, beat string) (*Llama, *[]string) {
	t.Helper()
	var prompts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		if err := json.Unmarshal(b, &body); err != nil || len(body.Messages) == 0 {
			t.Errorf("unreadable request body: %v", err)
		}
		p := body.Messages[0].Content
		prompts = append(prompts, p)
		// Dispatched on WHICH pass is asking, not on call order: a fixture that stopped
		// producing entity candidates would otherwise silently feed the entity answer to the
		// event pass and the test would fail somewhere else entirely.
		switch {
		case strings.Contains(p, "KINDS:"):
			io.WriteString(w, chatReply(entities))
		case strings.Contains(p, "List what this conversation shows happening"):
			io.WriteString(w, chatReply(events))
		default:
			io.WriteString(w, chatReply(beat))
		}
	}))
	t.Cleanup(srv.Close)
	return NewLlama(srv.URL), &prompts
}

// splitFixture is a record and a window sharing one subject, with a sentinel phrase that exists
// ONLY in the window.
//
// The sentinel is lowercase and multi-word on purpose, following the ruling this package already
// made once: an all-caps or single-token sentinel is extracted by Identifiers/distinctiveToken
// and legitimately reaches the retain list or Subjects, so it would prove nothing about leakage.
// A lowercase multi-word phrase has no route into any derived block, so its presence in the
// composition prompt could only mean the window itself was passed in.
const windowOnlySentinel = "the kettle boiled twice while the tests were running"

// The identifiers are strong ones (a dotted filename, an underscored constant) so the candidate
// list is non-empty without the local document-frequency table being representative: a unit test
// must not depend on how many transcripts happen to be on the machine running it.
func splitFixture() (SessionRecord, string) {
	turn := "please finish the Atlas CSV export in exporter.go for Meridian; " +
		windowOnlySentinel + " and KELD_EXPORT_RANGE still returns nothing for empty ranges"
	w := Window{Turns: []Turn{{Role: RoleUser, Text: turn}}}
	rec := SessionRecord{Turns: 9, UserTurns: 3}.Observe(w, Extract(w)).WithProject("keld-signal")
	return rec, "user: " + turn + "\n"
}

// TestComposePassNeverSeesTheWindow is the guarantee the split exists for. If the window ever
// reaches the composition pass, the prose writer can embroider evidence it was never shown and
// the whole reason for splitting is gone.
//
// Two independent assertions, because one of them alone could be satisfied by accident:
// the window-only sentinel must be absent, and the composition prompt must share no long span
// with the window at all — a leak that clipped the sentinel would still show up as a shared span.
func TestComposePassNeverSeesTheWindow(t *testing.T) {
	rec, window := splitFixture()
	l, prompts := splitFake(t,
		`{"entities":[{"name":"exporter.go","kind":"component"},{"name":"KELD_EXPORT_RANGE","kind":"component"}]}`,
		`{"events":["the Atlas CSV export was picked up","nothing was completed in this stretch"]}`,
		`{"beat":"The Atlas CSV export in exporter.go is the piece in hand. It was picked up in this stretch and nothing has been completed yet."}`)

	s, err := l.GenerateBeatSplit(rec, window)
	if err != nil {
		t.Fatalf("GenerateBeatSplit: %v", err)
	}
	if len(*prompts) != 3 {
		t.Fatalf("want 3 requests (entity, event, compose), got %d", len(*prompts))
	}
	compose := (*prompts)[2]
	if compose != s.ComposePrompt {
		t.Error("the recorded composition prompt is not the one that was sent")
	}
	if strings.Contains(compose, windowOnlySentinel) {
		t.Error("the composition prompt carries the window-only sentinel: the window leaked")
	}
	if span := longestSharedSpan(compose, window, 40); span != "" {
		t.Errorf("the composition prompt shares a %d-rune span with the window: %q",
			len([]rune(span)), span)
	}
	// The passes' own material must be there, or the test above would pass on an empty prompt.
	for _, want := range []string{"exporter.go — component", "KELD_EXPORT_RANGE — component",
		"nothing was completed in this stretch", rec.Block()} {
		if !strings.Contains(compose, want) {
			t.Errorf("composition prompt is missing its own input %q", want)
		}
	}
}

// TestComposePromptCannotCarryAWindowFact states the same property at the level the design is
// argued in: a fact that is in the window and in neither pass cannot appear in the prompt the
// prose is written from, so it cannot appear in the beat.
func TestComposePromptCannotCarryAWindowFact(t *testing.T) {
	rec, window := splitFixture()
	const windowFact = "empty ranges"
	if !strings.Contains(window, windowFact) {
		t.Fatalf("fixture no longer contains %q", windowFact)
	}
	p := BeatComposePrompt(rec.Block(),
		[]BeatEntity{{Name: "keld-signal", Kind: KindRepo}},
		[]string{"the Atlas CSV export was picked up"})
	if strings.Contains(p, windowFact) {
		t.Errorf("composition prompt carries a fact only the window states: %q", windowFact)
	}
}

// TestSplitPassesReadTheWindowAndTheComposeOneDoesNot is the pairing that makes the assertion
// above meaningful: the two reading passes MUST see the conversation, or they could not report
// it, and the third must not.
func TestSplitPassesReadTheWindowAndTheComposeOneDoesNot(t *testing.T) {
	rec, window := splitFixture()
	l, prompts := splitFake(t,
		`{"entities":[{"name":"exporter.go","kind":"component"}]}`,
		`{"events":["the Atlas CSV export was picked up"]}`,
		`{"beat":"The Atlas CSV export is the work in hand in exporter.go. It was picked up in this stretch."}`)
	if _, err := l.GenerateBeatSplit(rec, window); err != nil {
		t.Fatalf("GenerateBeatSplit: %v", err)
	}
	for i, name := range []string{"entity", "event"} {
		if !strings.Contains((*prompts)[i], windowOnlySentinel) {
			t.Errorf("the %s pass was not shown the conversation", name)
		}
	}
}

// TestSplitRecordsEveryPassInPlace covers what the artifact needs: three prompts, three outputs,
// and the attempt count of each pass separately.
func TestSplitRecordsEveryPassInPlace(t *testing.T) {
	rec, window := splitFixture()
	l, _ := splitFake(t,
		`{"entities":[{"name":"exporter.go","kind":"component"},{"name":"KELD_EXPORT_RANGE","kind":"noise"}]}`,
		`{"events":["the Atlas CSV export was picked up"]}`,
		`{"beat":"The Atlas CSV export is the work in hand in exporter.go. It was picked up in this stretch."}`)
	s, err := l.GenerateBeatSplit(rec, window)
	if err != nil {
		t.Fatalf("GenerateBeatSplit: %v", err)
	}
	if s.EntityPrompt == "" || s.EventPrompt == "" || s.ComposePrompt == "" {
		t.Error("a pass prompt was not recorded")
	}
	if s.EntityAttempts != 1 || s.EventAttempts != 1 || s.ComposeAttempts != 1 {
		t.Errorf("attempts per pass = %d/%d/%d, want 1/1/1",
			s.EntityAttempts, s.EventAttempts, s.ComposeAttempts)
	}
	if s.Attempts() != 3 {
		t.Errorf("total attempts = %d, want 3", s.Attempts())
	}
	if s.Failed() || s.Which() != "" {
		t.Errorf("clean run reports failure in %q", s.Which())
	}
	// A noise entity is typed, kept in the record of the pass, and dropped from the block the
	// composition pass reads.
	if strings.Contains(RenderBeatEntities(s.Entities), "KELD_EXPORT_RANGE") {
		t.Error("a noise entity reached the composition block")
	}
}

// TestSplitNamesTheFailingPass matters for the artifact: "the beat failed" is not an observation,
// "the event pass failed after 5 attempts" is.
func TestSplitNamesTheFailingPass(t *testing.T) {
	rec, window := splitFixture()
	l, _ := splitFake(t,
		`{"entities":[{"name":"exporter.go","kind":"component"}]}`,
		`{"events":["short"]}`, // under beatEventMinRunes, so every attempt is rejected
		`{"beat":"unreached"}`)
	l.Policy.MaxAttempts, l.Policy.BaseDelay = 3, 0
	s, err := l.GenerateBeatSplit(rec, window)
	if err == nil {
		t.Fatal("want an error when the event pass never validates")
	}
	if s.Which() != "event" {
		t.Errorf("failing pass = %q, want event", s.Which())
	}
	if s.EventAttempts != 3 {
		t.Errorf("event attempts = %d, want 3", s.EventAttempts)
	}
	if s.ComposePrompt != "" || s.Text != "" {
		t.Error("composition ran after a failed reading pass")
	}
}

// TestComposeKeepsTheControlPathsShapeStandard: the split arm must be judged by the same rules as
// the control, or a difference in output could be a difference in what was allowed through.
func TestComposeKeepsTheControlPathsShapeStandard(t *testing.T) {
	rec, window := splitFixture()
	l, _ := splitFake(t,
		`{"entities":[{"name":"exporter.go","kind":"component"}]}`,
		`{"events":["the Atlas CSV export was picked up"]}`,
		// A headline clause with no terminator: no complete sentence, the exact failure the
		// control path's ladder exists for.
		`{"beat":"Syncing the Atlas CSV export with the actual state of the world"}`)
	l.Policy.MaxAttempts, l.Policy.BaseDelay = 2, 0
	s, err := l.GenerateBeatSplit(rec, window)
	if err == nil {
		t.Fatal("an unpunctuated headline was accepted as a beat")
	}
	if s.Which() != "compose" {
		t.Errorf("failing pass = %q, want compose", s.Which())
	}
	if !strings.Contains(s.ComposeErr, "complete sentence") {
		t.Errorf("rejection reason = %q, want the sentence-completeness standard", s.ComposeErr)
	}
}

// TestComposeRejectsAProgressClaimTheNotesDoNotMake keeps the anti-rubberstamping standard on the
// split path, judged against the notes — which is provably everything the writer was shown.
func TestComposeRejectsAProgressClaimTheNotesDoNotMake(t *testing.T) {
	rec, window := splitFixture()
	l, _ := splitFake(t,
		`{"entities":[{"name":"exporter.go","kind":"component"}]}`,
		`{"events":["the Atlas CSV export was picked up"]}`,
		`{"beat":"The Atlas CSV export in exporter.go is what is in hand. The work is complete and only the sign-off is pending."}`)
	l.Policy.MaxAttempts, l.Policy.BaseDelay = 2, 0
	s, err := l.GenerateBeatSplit(rec, window)
	if err == nil {
		t.Fatal("a whole-of-work completion claim was accepted")
	}
	if !strings.Contains(s.ComposeErr, "overall progress") {
		t.Errorf("rejection reason = %q, want the progress-claim standard", s.ComposeErr)
	}
}

// TestUngroundedTermsCountsWhatTheNotesDidNotHold is the measurement of the constraint, and it
// must be an observation rather than a gate — a rejection would hide exactly what is worth
// counting.
func TestUngroundedTermsCountsWhatTheNotesDidNotHold(t *testing.T) {
	const notes = "  keld-signal — repo\n  - the Atlas CSV export was picked up\n"
	got := ungroundedTerms("The Atlas CSV export in keld-signal stalled on turn-row.tsx.", notes)
	if len(got) != 1 || got[0] != "turn-row.tsx" {
		t.Errorf("ungroundedTerms = %v, want [turn-row.tsx]", got)
	}
}

// longestSharedSpan returns the first span of at least n runes that a and b share, or "".
//
// A whole-window leak, a partial one and a quoted fragment all show up as a long shared span,
// which a single sentinel string would miss.
func longestSharedSpan(a, b string, n int) string {
	ar, br := []rune(a), []rune(b)
	if len(br) < n {
		return ""
	}
	hay := string(ar)
	for i := 0; i+n <= len(br); i++ {
		span := string(br[i : i+n])
		if strings.Contains(hay, span) {
			return span
		}
	}
	return ""
}
