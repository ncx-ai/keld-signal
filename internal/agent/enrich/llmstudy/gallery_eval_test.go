//go:build llmstudy

// Live gallery eval against the hand-authored gold set. Requires a llama-server.
//
//	GALLERY_URL=http://127.0.0.1:8090 go test -tags llmstudy \
//	  ./internal/agent/enrich/llmstudy/ -run GalleryEval -v -timeout 60m
package llmstudy

import (
	"os"
	"sort"
	"testing"
)

func TestGalleryEval(t *testing.T) {
	url := os.Getenv("GALLERY_URL")
	if url == "" {
		url = "http://127.0.0.1:8090"
	}
	gold, err := LoadGalleryGold("testdata/gallery_gold.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if p := ValidateGalleryGold(gold); len(p) > 0 {
		t.Fatalf("gold is invalid, refusing to score against it: %v", p)
	}
	l := NewLlama(url)
	answers := make(map[string]GalleryAnswer, len(gold))
	for _, g := range gold {
		answers[g.ID] = l.RunGallery(g)
	}
	scores := ScoreGallery(gold, answers)

	names := make([]string, 0, len(scores))
	for n := range scores {
		names = append(names, n)
	}
	sort.Strings(names)

	t.Logf("%-24s %-12s %5s %5s %5s  %5s %5s %5s  %6s %5s", "template", "kind",
		"TP", "FP", "FN", "P", "R", "F1", "exact", "halluc")
	var tp, fp, fn, exact, rows, halluc, invalid int
	for _, n := range names {
		s := scores[n]
		tpl, _ := GalleryByID(n)
		t.Logf("%-24s %-12s %5d %5d %5d  %.2f  %.2f  %.2f  %2d/%-3d %5d",
			n, tpl.Kind, s.TP, s.FP, s.FN, s.Precision(), s.Recall(), s.F1(),
			s.Exact, s.Rows, s.Hallucinated)
		tp += s.TP
		fp += s.FP
		fn += s.FN
		exact += s.Exact
		rows += s.Rows
		halluc += s.Hallucinated
		invalid += s.Invalid
	}
	all := Score{TP: tp, FP: fp, FN: fn, Exact: exact, Rows: rows}
	t.Logf("%-24s %-12s %5d %5d %5d  %.2f  %.2f  %.2f  %2d/%-3d %5d",
		"ALL", "", tp, fp, fn, all.Precision(), all.Recall(), all.F1(), exact, rows, halluc)
	t.Logf("invalid answers: %d   hallucinated spans dropped by the verbatim gate: %d", invalid, halluc)

	// Per-row detail for the rows that failed, so failures are diagnosable rather
	// than just counted.
	t.Logf("--- rows not exactly right ---")
	for _, g := range gold {
		a := answers[g.ID]
		one := ScoreGallery([]GalleryGold{g}, map[string]GalleryAnswer{g.ID: a})[g.Template]
		if one.Exact == 1 {
			continue
		}
		switch g.Kind {
		case "entity":
			t.Logf("  %-9s want=%v got=%v dropped=%v", g.ID, g.Entities, a.Entities, a.Dropped)
		case "structure":
			t.Logf("  %-9s want=%v got=%v", g.ID, g.Fields, a.Fields)
		case "single_label":
			t.Logf("  %-9s want=%s got=%s", g.ID, g.Label, a.Label)
		case "multi_label":
			t.Logf("  %-9s want=%v got=%v", g.ID, g.Labels, a.Labels)
		}
	}
}
