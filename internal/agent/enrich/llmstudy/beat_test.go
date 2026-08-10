package llmstudy

import (
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
	for _, want := range []string{"one to three sentences", "what the work is about"} {
		if !strings.Contains(p, want) {
			t.Errorf("beat prompt omits %q", want)
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

func TestBeatIsClippedToItsCap(t *testing.T) {
	if got := len([]rune(clipProse(strings.Repeat("word ", 200), BeatCap))); got > BeatCap {
		t.Errorf("beat not clipped: %d runes", got)
	}
}

func TestBeatTurnsDefaultsToThree(t *testing.T) {
	t.Setenv("KELD_DIGEST_BEAT_TURNS", "")
	if got := BeatTurnsFromEnv(); got != 3 {
		t.Errorf("want 3, got %d", got)
	}
	t.Setenv("KELD_DIGEST_BEAT_TURNS", "7")
	if got := BeatTurnsFromEnv(); got != 7 {
		t.Errorf("want 7, got %d", got)
	}
}
