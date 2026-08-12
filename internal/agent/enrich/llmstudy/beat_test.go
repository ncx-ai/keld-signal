package llmstudy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// beatServer answers every request with the nth scripted {subject, events} pair, repeating the
// last one once the script runs out. It returns the request count so a test can assert that a
// rejection was RE-REQUESTED rather than accepted or dropped.
func beatServer(t *testing.T, script ...struct {
	Subject string
	Events  []string
}) (*Llama, *int) {
	t.Helper()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := n
		n++
		if i >= len(script) {
			i = len(script) - 1
		}
		blob, _ := json.Marshal(map[string]any{
			"subject": script[i].Subject, "events": script[i].Events,
		})
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": string(blob)}}},
		})
	}))
	t.Cleanup(srv.Close)
	l := NewLlama(srv.URL)
	l.Policy = fastPolicy()
	return l, &n
}

type beatAnswer = struct {
	Subject string
	Events  []string
}

// A beat is given the window AND the measured record. Without the record it describes a local
// action ("read three CSVs") instead of what the action was for.
func TestBeatPromptCarriesWindowAndRecord(t *testing.T) {
	rec := SessionRecord{Turns: 12}.WithProject("meridian")
	p := BeatPrompt(rec.Block(), "user: reconcile the Larkin accrual\n")
	if !strings.Contains(p, "meridian") {
		t.Error("beat prompt omits the measured record")
	}
	if !strings.Contains(p, "Larkin") {
		t.Error("beat prompt omits the window")
	}
	// The chain this design avoids: a beat must never be handed another beat.
	if strings.Contains(strings.ToLower(p), "previous beat") {
		t.Error("beat prompt refers to an earlier beat")
	}
}

// THE design decision, as a test: the prompt asks what HAPPENED and never how far along anything
// is. The fused question ("what you are working on, and where it has got to") demanded a progress
// claim on every firing, and blind judges failed 22 of 36 untampered beats on rubberstamping.
func TestBeatPromptAsksWhatHappenedAndNeverForProgress(t *testing.T) {
	p := BeatPrompt("record", "window")
	low := strings.ToLower(p)
	for _, gone := range []string{
		"where it has got to", "how far", "where it stands", "where things stand",
		"status", "progress so far", "nearly", "complete", "remaining",
	} {
		if strings.Contains(low, gone) {
			t.Errorf("the beat prompt still invites a progress claim (%q)", gone)
		}
	}
	for _, want := range []string{"past tense", "subject", "events"} {
		if !strings.Contains(low, want) {
			t.Errorf("the beat prompt omits %q", want)
		}
	}
}

// The empty answer is MODELLED, not prohibited. When the old stock-opener rule was reworded to
// NAME the phrasings it forbade, those openings went from 2 to 4: a prompt summons what it names.
// So one worked example is the nothing-was-finished shape, sitting among the others as an
// ordinary answer, and there is no forbidden-phrase list anywhere in the prompt.
func TestBeatPromptModelsTheEmptyAnswerWithoutNamingAProhibition(t *testing.T) {
	p := BeatPrompt("record", "window")
	if !strings.Contains(p, "the depreciation task was assigned") {
		t.Error("the window in which nothing was finished is not modelled as an example answer")
	}
	if !strings.Contains(p, "each of these is a normal answer") {
		t.Error("the examples are not presented as normal answers")
	}
	for _, forbidden := range []string{"do not", "never", "forbidden", "must not", "avoid"} {
		if strings.Contains(strings.ToLower(p), forbidden) {
			t.Errorf("the beat prompt names a prohibition (%q); prompts summon what they name",
				forbidden)
		}
	}
}

// The schema is the other place a status field could come back from, so it is asserted
// separately: two fields, both required, nothing else admitted.
func TestBeatSchemaHasNoStatusField(t *testing.T) {
	sc := BeatSchema()
	props := sc["properties"].(map[string]any)
	if len(props) != 2 || props["subject"] == nil || props["events"] == nil {
		t.Fatalf("a beat is a subject and its events, got %v", props)
	}
	req := sc["required"].([]string)
	if len(req) != 2 {
		t.Errorf("both fields must be required, got %v", req)
	}
	if sc["additionalProperties"] != false {
		t.Error("the schema admits fields the design does not define")
	}
}

// One unanchored entry is DROPPED and the drop is MARKED. It is not a reason to lose the rest of
// the beat, and it is never silent: a guard that drops without saying so is how T1 reported 100%
// while discarding 5 of 20 digests.
func TestGenerateBeatDropsAnUnanchoredEntryAndMarksIt(t *testing.T) {
	l, n := beatServer(t, beatAnswer{
		Subject: "the fixed-asset register review",
		Events: []string{
			"fa-register.csv was opened and read against the schedule",
			"the Ganymede migration was signed off by the steering group",
		},
	})
	d, err := l.generateBeat("counts: turns=4\n", "user: open fa-register.csv and check it\n")
	if err != nil {
		t.Fatalf("one unanchored entry failed the whole beat: %v", err)
	}
	if *n != 1 {
		t.Errorf("a drop is not a rejection; want one attempt, got %d", *n)
	}
	if len(d.Events) != 1 || !strings.Contains(d.Events[0], "fa-register.csv") {
		t.Errorf("the anchored entry did not survive: %v", d.Events)
	}
	if len(d.Unanchored) != 1 || !strings.Contains(d.Unanchored[0], "Ganymede") {
		t.Errorf("the unanchored entry was not recorded: %v", d.Unanchored)
	}
	if !strings.Contains(d.Text, "1 entry dropped: no term in it occurs") {
		t.Errorf("the drop is not marked in the stored beat:\n%s", d.Text)
	}
	if strings.Contains(d.Text, "Ganymede") {
		t.Errorf("the dropped entry survived into the stored beat:\n%s", d.Text)
	}
	if d.Anchors[0].Term != "fa-register.csv" || !d.Anchors[0].InWindow {
		t.Errorf("the anchoring term is not reported: %v", d.Anchors)
	}
}

// Every entry unanchored is a generation with nothing of the evidence in it, which is the one
// case where losing the beat is the honest outcome — and it is re-requested first, at a wider
// temperature, rather than failed on the spot.
func TestGenerateBeatRetriesWhenNoEntryIsAnchored(t *testing.T) {
	l, n := beatServer(t,
		beatAnswer{
			Subject: "the fixed-asset register review",
			Events:  []string{"the Ganymede migration was signed off by the steering group"},
		},
		beatAnswer{
			Subject: "the fixed-asset register review",
			Events:  []string{"fa-register.csv was opened and read against the schedule"},
		})
	d, err := l.generateBeat("counts: turns=4\n", "user: open fa-register.csv and check it\n")
	if err != nil {
		t.Fatalf("generateBeat: %v", err)
	}
	if *n < 2 {
		t.Errorf("a wholly unanchored beat must be re-requested, got %d attempt(s)", *n)
	}
	if len(d.Events) != 1 {
		t.Errorf("want the recovered entry, got %v", d.Events)
	}
}

// The cap drops WHOLE entries and marks that too. Never a cut inside an entry: half an entry is
// the mid-clause truncation AGENTS.md forbids, and the defect BeatCap was raised to fix.
func TestGenerateBeatFitsTheCapByDroppingWholeEntries(t *testing.T) {
	long := func(i int) string {
		return "run-" + string(rune('a'+i)) + ".log was written, " +
			strings.Repeat("and the exporter wrote another row, ", 2) + "and it finished"
	}
	var events []string
	for i := 0; i < 6; i++ {
		events = append(events, long(i))
	}
	window := "user: check run-a.log run-b.log run-c.log run-d.log run-e.log run-f.log\n"
	l, _ := beatServer(t, beatAnswer{Subject: "the exporter run logs", Events: events})
	d, err := l.generateBeat("counts: turns=4\n", window)
	if err != nil {
		t.Fatalf("generateBeat: %v", err)
	}
	if runeLen(d.Text) > BeatCap {
		t.Errorf("stored beat is %d runes, over BeatCap %d", runeLen(d.Text), BeatCap)
	}
	if len(d.Overflowed) == 0 {
		t.Fatal("nothing overflowed a beat built to exceed the cap")
	}
	if !strings.Contains(d.Text, "dropped to fit the beat cap") {
		t.Errorf("the cap drop is not marked:\n%s", d.Text)
	}
	for _, e := range d.Events {
		if !strings.HasSuffix(e, "and it finished") {
			t.Errorf("an entry was cut rather than dropped: %q", e)
		}
	}
}

// A one-word subject is structurally valid and says nothing, so it is re-requested.
func TestGenerateBeatRejectsADegenerateSubject(t *testing.T) {
	l, n := beatServer(t,
		beatAnswer{Subject: "Work", Events: []string{"fa-register.csv was opened and read"}},
		beatAnswer{
			Subject: "the fixed-asset register review",
			Events:  []string{"fa-register.csv was opened and read"},
		})
	d, err := l.generateBeat("counts: turns=4\n", "user: open fa-register.csv\n")
	if err != nil {
		t.Fatalf("generateBeat: %v", err)
	}
	if *n < 2 {
		t.Errorf("a one-word subject must be re-requested, got %d attempt(s) and %q", *n, d.Subject)
	}
}

// The stored text is the rendering everything downstream reads, so its shape is pinned here: the
// subject on its own line, one "- " entry per event, and no max_tokens on the request (a token
// limit on a schema-constrained decode cuts the string mid-value and the object never closes).
func TestGenerateBeatRendersSubjectThenBullets(t *testing.T) {
	var sawMaxTokens bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["max_tokens"]; ok {
			sawMaxTokens = true
		}
		blob, _ := json.Marshal(map[string]any{
			"subject": "the fixed-asset register review",
			"events": []string{"fa-register.csv was opened and read",
				"the schedule and fa-register.csv disagreed on three assets"},
		})
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": string(blob)}}},
		})
	}))
	defer srv.Close()
	l := NewLlama(srv.URL)
	l.Policy = fastPolicy()
	got, err := l.GenerateBeat("counts: turns=4\n", "user: open fa-register.csv\n")
	if err != nil {
		t.Fatalf("GenerateBeat: %v", err)
	}
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("want a subject line and two entries, got %q", got)
	}
	if strings.HasPrefix(lines[0], "-") {
		t.Errorf("the subject is not on its own line: %q", got)
	}
	for _, l := range lines[1:] {
		if !strings.HasPrefix(l, "- ") {
			t.Errorf("an entry is not a bullet: %q", l)
		}
	}
	if sawMaxTokens {
		t.Error("the request carried max_tokens; a schema-constrained decode must not be token-bounded")
	}
}

// BeatSaysNothingNew is retired as a signal (AppendBeat no longer calls it) but must still
// COMPUTE, because the blind review harness lists it among the heuristics under comparison and
// scoring an earlier round means running it against that round's beats.
func TestBeatSaysNothingNewStillComputesForTheReviewHarness(t *testing.T) {
	prev := []Beat{{Ordinal: 1, Text: "The work is reconciling the March ledger for Meridian."}}
	if !BeatSaysNothingNew("Work continues reconciling Meridian's March ledger.", prev) {
		t.Error("a restatement was not detected")
	}
	if BeatSaysNothingNew("The work has moved to the AR ageing provision policy.", prev) {
		t.Error("a genuinely new beat was flagged")
	}
	if BeatSaysNothingNew("anything", nil) {
		t.Error("the first beat can never be a restatement")
	}
}

// ClipBeat is off the beat path — a bulleted beat holds no sentence terminators — but the
// sentence machinery under it is clipbound.go's, so its behaviour is still pinned here.
//
// Nothing asserted the SHAPE of a clip, only its length, so 46 of 47 real prose beats ended in an
// ellipsis mid-clause and every automated threshold passed them. Length is not shape: this
// asserts that whatever comes out, at any cap, either ends a sentence or is empty.
func TestClipBeatNeverEndsMidSentence(t *testing.T) {
	const two = "Closing March for Meridian, focusing on the bank reconciliation. " +
		"The fee has been journaled and the deposit in transit still needs to clear."
	cases := []struct {
		name string
		in   string
		cap  int
	}{
		{"fits whole", two, 512},
		{"cap lands mid second sentence", two, 100},
		{"cap lands one rune short of the end", two, len([]rune(two)) - 1},
		{"model stopped mid-clause", "Closing March for Meridian, focusing on the bank " +
			"reconciliation. The fee has been journaled and the deposit", 512},
		{"already carries an ellipsis", "Closing March for Meridian. The fee has been jour…", 512},
		{"no cap at all", two, 0},
	}
	for _, c := range cases {
		got := ClipBeat(c.in, c.cap)
		if got == "" {
			continue // nothing complete fits, which is the documented alternative
		}
		if strings.HasSuffix(got, "…") {
			t.Errorf("%s: text ends in an ellipsis: %q", c.name, got)
		}
		if end := lastSentenceStop([]rune(got)); end != len([]rune(got)) {
			t.Errorf("%s: text does not end at a sentence boundary: %q", c.name, got)
		}
		if c.cap > 0 && len([]rune(got)) > c.cap {
			t.Errorf("%s: cap %d exceeded: %d runes", c.name, c.cap, len([]rune(got)))
		}
	}
}

// Clipping must drop the trailing incomplete sentence, not shorten it. The distinction is the
// whole fix: a shortened sentence still reads as a sentence and is silently wrong.
func TestClipBeatDropsTheTrailingFragment(t *testing.T) {
	in := "Fixing the ConfirmDialog nesting warning in the members table. " +
		"The remove-key-button and RemoveMember paths both went through the same trigger and"
	got := ClipBeat(in, 512)
	want := "Fixing the ConfirmDialog nesting warning in the members table."
	if got != want {
		t.Errorf("the incomplete tail was not dropped\n got: %q\nwant: %q", got, want)
	}
}

func TestClipBeatReturnsNothingWhenNoSentenceFits(t *testing.T) {
	long := "Fixing the ConfirmDialog nesting warning in the members table so the press " +
		"responder stops warning about invalid button nesting across every trigger."
	if got := ClipBeat(long, 40); got != "" {
		t.Errorf("want nothing when no sentence fits, got %q", got)
	}
	if got := ClipBeat("no terminator anywhere in this text", 512); got != "" {
		t.Errorf("want nothing when the text holds no sentence, got %q", got)
	}
	if got := ClipBeat("   ", 512); got != "" {
		t.Errorf("want nothing for blank input, got %q", got)
	}
}

// This study's text is full of identifiers, so "any period ends a sentence" cuts inside them.
// Every input here is one sentence and must survive whole.
func TestClipBeatDoesNotCutInsideAnIdentifier(t *testing.T) {
	for _, in := range []string{
		"Fixed the bottom padding on the row container in turn-row.tsx for consistent spacing.",
		"GLiNER2 uses only 2.9 GB of RAM per window on CPU.",
		"The truncation only shows on atlas.keld.co, not locally.",
		"Several components, e.g. the remove-key-button, went through the same trigger.",
		"Committed to main (d8f2628) with all tests passing.",
		"The label now reads 'Spend vs Run-rate.'",
	} {
		if got := ClipBeat(in, 512); got != in {
			t.Errorf("a single sentence was cut\n got: %q\nwant: %q", got, in)
		}
	}
}

// fitBeatText is AppendBeat's gate against a beat assembled some other way. Whole lines, marked.
func TestFitBeatTextDropsWholeLinesAndMarksIt(t *testing.T) {
	subject := "the exporter run logs"
	entry := "- " + strings.Repeat("x", 120) + "\n"
	over := subject + "\n" + strings.Repeat(entry, 6)
	got := fitBeatText(over, BeatCap)
	if runeLen(got) > BeatCap {
		t.Errorf("fit left %d runes, over BeatCap %d", runeLen(got), BeatCap)
	}
	if !strings.Contains(got, "dropped to fit the beat cap") {
		t.Errorf("the drop is not marked:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n")[1:] {
		if strings.HasPrefix(line, "- ") && runeLen(line) != runeLen(entry)-1 {
			t.Errorf("a line was cut rather than dropped: %q", line)
		}
	}
	whole := subject + "\n- one entry that fits"
	if got := fitBeatText(whole, BeatCap); got != whole {
		t.Errorf("a beat within the cap was altered:\n got %q\nwant %q", got, whole)
	}
}

func TestBeatTurnsDefaultsToFive(t *testing.T) {
	t.Setenv("KELD_DIGEST_BEAT_TURNS", "")
	if got := BeatTurnsFromEnv(); got != 5 {
		t.Errorf("want 5, got %d", got)
	}
	t.Setenv("KELD_DIGEST_BEAT_TURNS", "7")
	if got := BeatTurnsFromEnv(); got != 7 {
		t.Errorf("want 7, got %d", got)
	}
}
