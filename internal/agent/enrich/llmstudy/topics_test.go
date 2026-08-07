package llmstudy

import "testing"

func TestVerifyTopicsKeepsOnlyRealSubstrings(t *testing.T) {
	src := "add retry to the settings poll\nThe poll lives in settings.go."
	kept, dropped := VerifyTopics([]string{"retry", "settings poll", "quantum tunnelling"}, src)
	if len(kept) != 2 {
		t.Fatalf("kept = %v, want the 2 real terms", kept)
	}
	if len(dropped) != 1 || dropped[0] != "quantum tunnelling" {
		t.Fatalf("dropped = %v, want the hallucinated term", dropped)
	}
}

func TestVerifyTopicsCaseInsensitiveButPreservesCasing(t *testing.T) {
	kept, dropped := VerifyTopics([]string{"Settings Poll"}, "add retry to the settings poll")
	if len(kept) != 1 || len(dropped) != 0 {
		t.Fatalf("kept=%v dropped=%v; matching must be case-insensitive", kept, dropped)
	}
	if kept[0] != "Settings Poll" {
		t.Errorf("kept[0] = %q, want the model's original casing", kept[0])
	}
}

func TestVerifyTopicsDropsBlanksAndDuplicates(t *testing.T) {
	kept, _ := VerifyTopics([]string{"retry", "RETRY", "", "   "}, "add retry now")
	if len(kept) != 1 {
		t.Fatalf("kept = %v, want a single deduped term", kept)
	}
}

func TestVerifyTopicsOnEmptySourceKeepsNothing(t *testing.T) {
	kept, dropped := VerifyTopics([]string{"retry"}, "")
	if len(kept) != 0 || len(dropped) != 1 {
		t.Fatalf("kept=%v dropped=%v; nothing is verifiable against empty source", kept, dropped)
	}
}

// A paraphrase is the failure mode the gate exists to catch: plausible, relevant,
// and not actually present.
func TestVerifyTopicsRejectsPlausibleParaphrase(t *testing.T) {
	src := "the retry loop keeps hammering the settings endpoint"
	kept, dropped := VerifyTopics([]string{"retry backoff strategy"}, src)
	if len(kept) != 0 {
		t.Fatalf("a paraphrase must not survive verification: %v", kept)
	}
	if len(dropped) != 1 {
		t.Fatalf("dropped = %v", dropped)
	}
}

func TestVerifyTopicsNilInputIsSafe(t *testing.T) {
	kept, dropped := VerifyTopics(nil, "anything")
	if len(kept) != 0 || len(dropped) != 0 {
		t.Fatalf("kept=%v dropped=%v, want both empty", kept, dropped)
	}
}
