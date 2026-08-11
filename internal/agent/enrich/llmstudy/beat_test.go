package llmstudy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

// The register the prompt must ask for, and the one it must not.
//
// The monotony was caused by the instruction itself: "State what the work is about" produced 46
// of 47 beats opening with the identical four words, "The work is about". A prompt that dictates
// a subject and a verb dictates a sentence. So it now asks the question a colleague asks —
// which is what the answer should sound like — and names the stock openers as forbidden rather
// than hoping variety emerges.
func TestBeatPromptAsksTheStandupQuestionAndForbidsAFormula(t *testing.T) {
	p := BeatPrompt("record", "window")
	low := strings.ToLower(p)
	for _, want := range []string{"standup", "two or three sentences"} {
		if !strings.Contains(low, want) {
			t.Errorf("beat prompt omits %q — it must ask for a spoken answer, not a statement", want)
		}
	}
	// The instruction that caused the template must be gone as an INSTRUCTION.
	if strings.Contains(p, "State what the work is about") {
		t.Error("beat prompt still dictates the sentence it wants")
	}
	// And the formula must be named and forbidden.
	i := strings.Index(p, `"The work is about"`)
	if i < 0 {
		t.Fatal("beat prompt does not forbid the opener 46 of 47 beats used")
	}
	if !strings.Contains(strings.ToLower(p[:i]), "do not begin with") {
		t.Errorf("the opener is mentioned but not forbidden: %q", p[:i])
	}
	// Constraints that must survive the rewrite.
	for _, want := range []string{
		"Every noun must come from the conversation or the record",
		"Not a list of actions",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("beat prompt dropped the constraint %q", want)
		}
	}
}

func TestBeatSchemaIsASingleRequiredString(t *testing.T) {
	sc := BeatSchema()
	props := sc["properties"].(map[string]any)
	if len(props) != 1 || props["beat"] == nil {
		t.Fatalf("a beat is one field, got %v", props)
	}
	if req := sc["required"].([]string); len(req) != 1 || req[0] != "beat" {
		t.Errorf("beat must be required, got %v", req)
	}
}

// A run of acknowledgements must not pad the series and bury the moments that matter.
func TestBeatSayingNothingNewIsDropped(t *testing.T) {
	prev := []Beat{{Ordinal: 1, Text: "The work is reconciling the March ledger for Meridian."}}
	if !BeatSaysNothingNew("Work continues reconciling Meridian's March ledger.", prev) {
		t.Error("a restatement was not detected")
	}
	if BeatSaysNothingNew("The work has moved to the AR ageing provision policy.", prev) {
		t.Error("a genuinely new beat was dropped")
	}
	if BeatSaysNothingNew("anything", nil) {
		t.Error("the first beat can never be a restatement")
	}
}

// THE headline defect: a beat that ends mid-sentence.
//
// Nothing asserted the SHAPE of a beat, only its length, so 46 of 47 real beats ended in an
// ellipsis mid-clause and every automated threshold passed them. Length is not shape: this
// asserts that whatever comes out of the clipper, at any cap, either ends a sentence or is
// empty. It fails against the old clipProse-based clip on every case below.
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
			continue // a failed generation, which is the documented alternative
		}
		if strings.HasSuffix(got, "…") {
			t.Errorf("%s: beat ends in an ellipsis: %q", c.name, got)
		}
		if end := lastSentenceStop([]rune(got)); end != len([]rune(got)) {
			t.Errorf("%s: beat does not end at a sentence boundary: %q", c.name, got)
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

// If nothing complete fits, that is a FAILED generation — the retry path's job — not a beat.
// Emitting the fragment is what shipped; emitting a truncated first sentence would be worse.
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

// Beats are full of identifiers, so "any period ends a sentence" cuts inside them. Every input
// here is one sentence and must survive whole.
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

// BeatCap is a measured figure, and this is the measurement: the most expensive complete
// two-sentence answer in the 47-beat corpus ends at rune 505, so the cap must keep both of its
// sentences. At the old cap of 200 this beat arrived as a fragment of its first clause.
func TestBeatCapKeepsTheMostExpensiveMeasuredTwoSentenceAnswer(t *testing.T) {
	const measured = "The work is refining the LLM prompt structure to ensure accurate, " +
		"non-echoed summaries that reflect actual task outcomes — specifically, fixing the " +
		"model from copying section labels as content and maintaining a work-centric voice. " +
		"It’s now validated across four runs, with v3 showing clean structural fidelity, " +
		"under 3GB memory use, and real-world anchoring from user actions like 'commit to " +
		"main', though one unverified detail remains — 'rate-limiting' — which is a compound " +
		"from real text, not inferred. The sentinel fix and threshold 7 are now applied, and " +
		"the feature passes structural validity at 4/4."
	got := ClipBeat(measured, BeatCap)
	if n := strings.Count(got, ". ") + 1; n < 2 {
		t.Errorf("BeatCap %d keeps %d sentences of a measured two-sentence answer: %q",
			BeatCap, n, got)
	}
	if !strings.Contains(got, "validated across four runs") {
		t.Errorf("the second sentence did not survive BeatCap=%d: %q", BeatCap, got)
	}
}

// A fragment must be RE-REQUESTED, not emitted and not dropped. GenerateBeat validates inside
// callValid's retry loop precisely so the sample is re-drawn; the bound on the request is the
// prompt asking for brevity, never max_tokens — a token limit on a schema-constrained decode
// cuts the string mid-value and the JSON object never closes.
func TestGenerateBeatRetriesAFragment(t *testing.T) {
	var n int
	var sawMaxTokens bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["max_tokens"]; ok {
			sawMaxTokens = true
		}
		beat := "Closing March for Meridian, focusing on the bank reconciliation and the"
		if n >= 2 {
			beat = "Closing March for Meridian, focusing on the bank reconciliation. " +
				"The bank fee is journaled and the deposit in transit still needs to clear."
		}
		blob, _ := json.Marshal(map[string]string{"beat": beat})
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": string(blob)}}},
		})
	}))
	defer srv.Close()

	l := NewLlama(srv.URL)
	l.Policy = fastPolicy()
	got, err := l.GenerateBeat("record", "window")
	if err != nil {
		t.Fatalf("a fragment was not re-requested: %v", err)
	}
	if n < 2 {
		t.Errorf("want a second attempt, got %d", n)
	}
	if !strings.HasSuffix(got, "clear.") {
		t.Errorf("want the complete generation, got %q", got)
	}
	if sawMaxTokens {
		t.Error("the request carried max_tokens; a schema-constrained decode must not be token-bounded")
	}
}

// A generation that only ever yields a fragment is a failure, reported as such. Silently
// emitting the fragment is the defect; silently emitting nothing would be worse.
func TestGenerateBeatFailsWhenNothingCompleteEverArrives(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blob, _ := json.Marshal(map[string]string{"beat": "Closing March for Meridian and the"})
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": string(blob)}}},
		})
	}))
	defer srv.Close()

	l := NewLlama(srv.URL)
	l.Policy = fastPolicy()
	got, err := l.GenerateBeat("record", "window")
	if err == nil {
		t.Fatalf("a beat with no complete sentence must be an error, got %q", got)
	}
	if got != "" {
		t.Errorf("a failed generation must yield no text, got %q", got)
	}
}

// A complete but degenerate sentence is not an answer to the question. "Fixed." is structurally
// valid, ends a sentence, and would sail past every shape check here.
func TestGenerateBeatRejectsADegenerateSentence(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		beat := "Fixed."
		if n >= 2 {
			beat = "Closing March for Meridian, focusing on the bank reconciliation. " +
				"The bank fee is journaled and the deposit in transit still needs to clear."
		}
		blob, _ := json.Marshal(map[string]string{"beat": beat})
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": string(blob)}}},
		})
	}))
	defer srv.Close()

	l := NewLlama(srv.URL)
	l.Policy = fastPolicy()
	got, err := l.GenerateBeat("record", "window")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n < 2 {
		t.Errorf("a one-word beat must be re-requested, got %d attempts and %q", n, got)
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
