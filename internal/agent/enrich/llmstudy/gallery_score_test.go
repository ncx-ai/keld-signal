package llmstudy

import "testing"

func TestScoreGalleryEntityCountsPrecisionAndRecall(t *testing.T) {
	gold := []GalleryGold{{
		ID: "g1", Template: "technologies_mentioned", Kind: "entity",
		Text:     "Rewrite the React component in TypeScript.",
		Entities: map[string][]string{"language": {"TypeScript"}, "framework": {"React"}, "platform": {}},
	}}
	ans := map[string]GalleryAnswer{"g1": {
		ID: "g1", Template: "technologies_mentioned", Valid: true,
		Entities: map[string][]string{"language": {"TypeScript"}, "framework": {}, "platform": {"Vercel"}},
	}}
	s := ScoreGallery(gold, ans)["technologies_mentioned"]
	if s.TP != 1 {
		t.Errorf("TP = %d, want 1 (TypeScript)", s.TP)
	}
	if s.FN != 1 {
		t.Errorf("FN = %d, want 1 (missed React)", s.FN)
	}
	if s.FP != 1 {
		t.Errorf("FP = %d, want 1 (invented Vercel)", s.FP)
	}
	if s.Exact != 0 {
		t.Errorf("Exact = %d, want 0", s.Exact)
	}
}

// A negative row answered with nothing is a success, and precision must reflect it.
func TestScoreGalleryNegativeAnsweredEmptyIsExact(t *testing.T) {
	gold := []GalleryGold{{
		ID: "n1", Template: "ticket_ids", Kind: "entity",
		Text: "Bump the version to 2.4.1.", Entities: map[string][]string{"ticket id": {}},
	}}
	ans := map[string]GalleryAnswer{"n1": {ID: "n1", Valid: true,
		Entities: map[string][]string{"ticket id": {}}}}
	s := ScoreGallery(gold, ans)["ticket_ids"]
	if s.Exact != 1 || s.FP != 0 || s.FN != 0 {
		t.Fatalf("clean negative should be exact with no errors: %+v", s)
	}
	if s.Precision() != 1 || s.Recall() != 1 {
		t.Errorf("P/R = %.2f/%.2f, want 1/1", s.Precision(), s.Recall())
	}
}

// Over-firing on a negative must be punished, or decoy rows measure nothing.
func TestScoreGalleryOverFiringOnNegativeIsFP(t *testing.T) {
	gold := []GalleryGold{{
		ID: "n2", Template: "ticket_ids", Kind: "entity",
		Text: "Bump the version to 2.4.1.", Entities: map[string][]string{"ticket id": {}},
	}}
	ans := map[string]GalleryAnswer{"n2": {ID: "n2", Valid: true,
		Entities: map[string][]string{"ticket id": {"2.4.1"}}}}
	s := ScoreGallery(gold, ans)["ticket_ids"]
	if s.FP != 1 || s.Exact != 0 {
		t.Fatalf("over-firing must count as FP and break exactness: %+v", s)
	}
}

// Boundary disagreement is a different problem from finding the wrong thing.
func TestSpanMatchToleratesBoundaryDisagreement(t *testing.T) {
	if !spanMatch("Postgres", "Postgres 16") {
		t.Error("narrower prediction should match")
	}
	if !spanMatch("Postgres 16", "Postgres") {
		t.Error("wider prediction should match")
	}
	if spanMatch("Docker", "Postgres") {
		t.Error("unrelated spans must not match")
	}
}

func TestScoreGalleryStructureAbsentFieldHandling(t *testing.T) {
	gold := []GalleryGold{{
		ID: "s1", Template: "deployment", Kind: "structure",
		Text:   "Push the web frontend to staging.",
		Fields: map[string]string{"service": "web frontend", "environment": "staging", "version": ""},
	}}
	// Correct: guesses no version.
	good := map[string]GalleryAnswer{"s1": {ID: "s1", Valid: true,
		Fields: map[string]string{"service": "web frontend", "environment": "staging", "version": ""}}}
	if s := ScoreGallery(gold, good)["deployment"]; s.Exact != 1 || s.FP != 0 {
		t.Fatalf("correct absence should be exact: %+v", s)
	}
	// Wrong: invents a version.
	bad := map[string]GalleryAnswer{"s1": {ID: "s1", Valid: true,
		Fields: map[string]string{"service": "web frontend", "environment": "staging", "version": "1.0.0"}}}
	if s := ScoreGallery(gold, bad)["deployment"]; s.FP != 1 || s.Exact != 0 {
		t.Fatalf("invented field must be FP: %+v", s)
	}
}

func TestScoreGalleryMultiLabelSetSemantics(t *testing.T) {
	gold := []GalleryGold{{
		ID: "m1", Template: "support_topics", Kind: "multi_label",
		Text: "double charged, want it back", Labels: []string{"billing", "refund"},
	}}
	ans := map[string]GalleryAnswer{"m1": {ID: "m1", Valid: true, Labels: []string{"billing", "bug"}}}
	s := ScoreGallery(gold, ans)["support_topics"]
	if s.TP != 1 || s.FP != 1 || s.FN != 1 {
		t.Fatalf("want TP1/FP1/FN1, got %+v", s)
	}
}

func TestScoreGalleryInvalidAnswerCounted(t *testing.T) {
	gold := []GalleryGold{{ID: "i1", Template: "ticket_ids", Kind: "entity",
		Entities: map[string][]string{"ticket id": {}}}}
	s := ScoreGallery(gold, map[string]GalleryAnswer{"i1": {ID: "i1", Valid: false}})["ticket_ids"]
	if s.Invalid != 1 {
		t.Fatalf("Invalid = %d, want 1", s.Invalid)
	}
}

// Separator differences are not extraction errors.
func TestNormSpanCollapsesSeparators(t *testing.T) {
	cases := [][2]string{
		{"billing-worker", "billing worker"},
		{"GPT-4o", "gpt 4o"},
		{"  Postgres  16 ", "postgres 16"},
		{"v2026-07-28", "v2026 07 28"},
	}
	for _, c := range cases {
		if got := normSpan(c[0]); got != c[1] {
			t.Errorf("normSpan(%q) = %q, want %q", c[0], got, c[1])
		}
	}
	if !spanMatch("billing-worker", "billing worker") {
		t.Error("separator difference must not count as a miss")
	}
	// But genuinely different values must still not match.
	if spanMatch("billing worker", "checkout api") {
		t.Error("unrelated values must not match")
	}
}

// List-field ordering must not be scored. A case-sensitive sort put "LinkedIn"
// before "email" and turned an order-only difference into a wrong answer.
func TestNormaliseFieldSortsCaseInsensitively(t *testing.T) {
	got := normaliseField([]byte(`["LinkedIn","email"]`))
	want := normaliseField([]byte(`["email","LinkedIn"]`))
	if got != want {
		t.Fatalf("order must not matter: %q vs %q", got, want)
	}
	if got != "email, LinkedIn" {
		t.Errorf("normaliseField = %q, want \"email, LinkedIn\"", got)
	}
}
