package llmstudy

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Does a per-turn SEMANTIC judgement carry signal about observed effort, where the
// structural features did not?
//
// Design: stratify. `corrected` fires on only 6.9% of turns, so a random sample gives
// too few positives. Take every corrected turn plus an equal random sample of
// uncorrected ones, and compare the model's judgement DISTRIBUTION between the two
// groups. That measures separation directly and is unaffected by the distorted base
// rate — accuracy would be meaningless here, separation is not.
func TestSemanticJudgementPredictsEffort(t *testing.T) {
	if os.Getenv("LLMSTUDY_SMOKE") == "" {
		t.Skip("gated")
	}
	url := os.Getenv("JUDGE_URL")
	if url == "" {
		url = "http://127.0.0.1:8081"
	}
	o := DefaultMineOpts()
	root := filepath.Join(os.Getenv("HOME"), ".claude", "projects")

	type row struct {
		w  Window
		oc Outcome
	}
	var corrected, clean []row
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		ws, e1 := Mine(p, o)
		ocs, e2 := Outcomes(p, o)
		if e1 != nil || e2 != nil || len(ws) != len(ocs) {
			return nil
		}
		for i := range ws {
			if len(ws[i].Turns) < 2 || ocs[i].Terminal {
				continue
			}
			r := row{ws[i], ocs[i]}
			if ocs[i].Corrected {
				corrected = append(corrected, r)
			} else {
				clean = append(clean, r)
			}
		}
		return nil
	})
	rng := rand.New(rand.NewSource(7))
	rng.Shuffle(len(clean), func(i, j int) { clean[i], clean[j] = clean[j], clean[i] })
	n := len(corrected)
	if n > 60 {
		n = 60 // bound wall-clock; still enough to see a strong effect
		corrected = corrected[:n]
	}
	if len(clean) > n {
		clean = clean[:n]
	}
	t.Logf("judging %d corrected + %d clean turns (stratified)", len(corrected), len(clean))

	l := NewLlama(url)
	judgeAll := func(rows []row) []Judgement {
		out := make([]Judgement, 0, len(rows))
		for _, r := range rows {
			out = append(out, l.Judge(r.w))
		}
		return out
	}
	jc, jn := judgeAll(corrected), judgeAll(clean)

	valid := func(js []Judgement) int {
		v := 0
		for _, j := range js {
			if j.Valid {
				v++
			}
		}
		return v
	}
	t.Logf("valid judgements: corrected %d/%d, clean %d/%d",
		valid(jc), len(jc), valid(jn), len(jn))

	// Separation: share of each group taking a given value.
	share := func(js []Judgement, get func(Judgement) string, val string) (float64, int) {
		hit, tot := 0, 0
		for _, j := range js {
			if !j.Valid {
				continue
			}
			tot++
			if get(j) == val {
				hit++
			}
		}
		if tot == 0 {
			return 0, 0
		}
		return float64(hit) / float64(tot), tot
	}
	fields := []struct {
		name string
		get  func(Judgement) string
		vals []string
	}{
		{"difficulty", func(j Judgement) string { return j.Difficulty }, difficultyVals},
		{"specificity", func(j Judgement) string { return j.Specificity }, specificityVals},
		{"scope", func(j Judgement) string { return j.Scope }, scopeVals},
		{"novelty", func(j Judgement) string { return j.Novelty }, noveltyVals},
	}
	t.Logf("%-14s %-16s %10s %10s %8s", "field", "value", "corrected", "clean", "delta")
	for _, f := range fields {
		for _, v := range f.vals {
			a, _ := share(jc, f.get, v)
			b, _ := share(jn, f.get, v)
			t.Logf("%-14s %-16s %9.0f%% %9.0f%% %+7.0f pts", f.name, v, a*100, b*100, (a-b)*100)
		}
	}
	da, _ := share(jc, func(j Judgement) string { return fmt.Sprint(j.Directive) }, "true")
	db, _ := share(jn, func(j Judgement) string { return fmt.Sprint(j.Directive) }, "true")
	t.Logf("%-14s %-16s %9.0f%% %9.0f%% %+7.0f pts", "directive", "true", da*100, db*100, (da-db)*100)

	// Does judged difficulty track observed tool volume? A monotone ordering across
	// difficulty tiers would be the cleanest possible evidence.
	byDiff := map[string][]int{}
	all := append(append([]row{}, corrected...), clean...)
	alljs := append(append([]Judgement{}, jc...), jn...)
	for i := range all {
		if alljs[i].Valid {
			byDiff[alljs[i].Difficulty] = append(byDiff[alljs[i].Difficulty], all[i].oc.ToolCalls)
		}
	}
	t.Logf("observed tool_calls by JUDGED difficulty:")
	for _, d := range difficultyVals {
		v := byDiff[d]
		if len(v) == 0 {
			continue
		}
		sort.Ints(v)
		sum := 0
		for _, x := range v {
			sum += x
		}
		t.Logf("  %-9s n=%3d  median=%4d  mean=%6.1f", d, len(v), v[len(v)/2], float64(sum)/float64(len(v)))
	}
}
