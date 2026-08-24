//go:build sidecar

// Facet-value study (2026-08-24): does each of the seven MODEL-BACKED
// classification facets beat the majority-class baseline on the gold set?
//
// Pre-registration: docs/superpowers/specs/2026-08-24-facet-value-preregistration.md
// Results:          docs/superpowers/specs/2026-08-24-facet-value-results.md
//
// This is a MEASUREMENT, not a gate: it asserts nothing and fails nothing. The
// gate for these facets is sidecar_eval_test.go's floors. It reuses
// LoadGold/RunModel/RunModelWithContext/Score/Calibration rather than
// re-implementing scoring, and writes two artifacts: a per-row NDJSON dump (one
// object per gold row, both arms) and a summary table.
//
//	FACET_STUDY_OUT=~/keld/refseries-context/facets \
//	SIDECAR_URL=http://127.0.0.1:8412 \
//	go test -tags sidecar -timeout 0 ./internal/agent/enrich/eval/ -run FacetStudy -v
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
)

// studyFacets are the facets that call ctx.Model. sensitivity is excluded on
// purpose: SensitivityExtractor is ModelFree, so it is not what the model is
// provisioned for. `speech_act` was one of the seven this study measured and is
// no longer here: the study's own verdict was DROP and the pass was removed at
// schema v9, so there is nothing left to score. Its gold labels remain in
// gold.jsonl for a re-measured replacement.
var studyFacets = []string{
	"task_type", "domain", "activity_type",
	"function_guess", "subcategory", "personal",
}

// chunk is the slice size between progress lines. RunModel over 165 rows issues
// ~1300 single-flight inferences, so a run is minutes long and silent progress
// is indistinguishable from a wedge.
const chunk = 15

// runChunked calls fn over successive slices of gold, printing progress. The
// per-row results concatenate exactly as a single call would produce them:
// RunModel/RunModelWithContext are per-row independent (one enrich.Run each).
func runChunked(t *testing.T, label string, gold []GoldRow, fn func([]GoldRow) []Pred) []Pred {
	t.Helper()
	out := make([]Pred, 0, len(gold))
	start := time.Now()
	for i := 0; i < len(gold); i += chunk {
		j := min(i+chunk, len(gold))
		out = append(out, fn(gold[i:j])...)
		fmt.Fprintf(os.Stderr, "[%s] %d/%d rows  elapsed %s\n", label, j, len(gold), time.Since(start).Round(time.Second))
	}
	return out
}

// dist counts values over the rows in idx.
func dist(vals []string, idx []int) map[string]int {
	d := map[string]int{}
	for _, i := range idx {
		v := vals[i]
		if v == "" {
			v = "(abstain)"
		}
		d[v]++
	}
	return d
}

// topOf returns the most frequent key and its share of the total.
func topOf(d map[string]int) (string, int, float64) {
	tot, best, bestN := 0, "", -1
	keys := make([]string, 0, len(d))
	for k, n := range d {
		tot += n
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic tie-break
	for _, k := range keys {
		if d[k] > bestN {
			best, bestN = k, d[k]
		}
	}
	if tot == 0 {
		return "", 0, 0
	}
	return best, bestN, float64(bestN) / float64(tot)
}

// fmtDist renders a distribution descending by count.
func fmtDist(d map[string]int) string {
	type kv struct {
		k string
		n int
	}
	xs := make([]kv, 0, len(d))
	tot := 0
	for k, n := range d {
		xs = append(xs, kv{k, n})
		tot += n
	}
	sort.Slice(xs, func(a, b int) bool {
		if xs[a].n != xs[b].n {
			return xs[a].n > xs[b].n
		}
		return xs[a].k < xs[b].k
	})
	parts := make([]string, 0, len(xs))
	for _, x := range xs {
		parts = append(parts, fmt.Sprintf("%s %d (%.0f%%)", x.k, x.n, 100*float64(x.n)/float64(tot)))
	}
	return strings.Join(parts, "  ")
}

// binomTail is the exact one-sided P(X >= k) for X~Binomial(n, p): the chance a
// constant-guesser with success rate p reaches the model's hit count by luck.
// Computed in log space (lgamma) so n=165 does not overflow a float64 factorial.
func binomTail(k, n int, p float64) float64 {
	if k <= 0 {
		return 1
	}
	if p <= 0 {
		return 0
	}
	if p >= 1 {
		return 1
	}
	lg := func(x float64) float64 { v, _ := math.Lgamma(x); return v }
	sum := 0.0
	for i := k; i <= n; i++ {
		lc := lg(float64(n)+1) - lg(float64(i)+1) - lg(float64(n-i)+1)
		sum += math.Exp(lc + float64(i)*math.Log(p) + float64(n-i)*math.Log1p(-p))
	}
	return math.Min(sum, 1)
}

// trim shortens a prompt for the report at a logical delimiter (sentence end,
// else line break, else word boundary) — never mid-word, and the drop is marked.
func trim(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) <= maxRunes {
		return s
	}
	head := string([]rune(s)[:maxRunes])
	if i := strings.LastIndexAny(head, ".?!"); i > maxRunes/3 {
		return head[:i+1] + " […]"
	}
	if i := strings.LastIndex(head, " "); i > 0 {
		return head[:i] + " […]"
	}
	return head + " […]"
}

type rowDump struct {
	Index int               `json:"index"`
	Class string            `json:"class"`
	Text  string            `json:"text"`
	Gold  map[string]string `json:"gold"`
	Ctx   map[string]string `json:"pred_ctx"`
	NoCtx map[string]string `json:"pred_noctx"`
	Conf  map[string]float64
}

func TestFacetStudy(t *testing.T) {
	url := os.Getenv("SIDECAR_URL")
	if url == "" {
		url = "http://127.0.0.1:8412"
	}
	sc := sidecar.New(url, 120*time.Second)
	if !sc.Healthy(context.Background()) {
		t.Fatalf("sidecar not reachable at %s", url)
	}
	gold, err := LoadGold()
	if err != nil {
		t.Fatal(err)
	}
	outDir := os.Getenv("FACET_STUDY_OUT")
	if outDir == "" {
		outDir = t.TempDir()
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Logf("gold rows: %d   sidecar: %s   schema: v%d", len(gold), url, enrich.SchemaVersion)

	ctxPred := runChunked(t, "CTX", gold, func(g []GoldRow) []Pred { return RunModelWithContext(sc, g) })
	noPred := runChunked(t, "NOCTX", gold, func(g []GoldRow) []Pred { return RunModel(sc, g) })

	ctxScore := Score(gold, ctxPred, studyFacets)
	noScore := Score(gold, noPred, studyFacets)

	// --- per-row NDJSON dump (every row, both arms) ---
	f, err := os.Create(filepath.Join(outDir, "rows.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for i, g := range gold {
		d := rowDump{Index: i, Class: g.Class, Text: g.Text,
			Gold: map[string]string{}, Ctx: map[string]string{}, NoCtx: map[string]string{},
			Conf: ctxPred[i].Conf}
		for _, fac := range studyFacets {
			d.Gold[fac] = fieldOf(g, fac)
			d.Ctx[fac] = fieldOf(ctxPred[i], fac)
			d.NoCtx[fac] = fieldOf(noPred[i], fac)
		}
		if err := enc.Encode(d); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()

	// --- per-facet analysis ---
	var b strings.Builder
	fmt.Fprintf(&b, "# Facet study raw output — %s\n\nsidecar %s, schema v%d, %d gold rows\n\n",
		time.Now().Format(time.RFC3339), url, enrich.SchemaVersion, len(gold))
	fmt.Fprintf(&b, "| facet | n | baseline (majority) | acc CTX | acc NOCTX | lift CTX | p(one-sided) | top predicted label | ECE CTX |\n")
	fmt.Fprintf(&b, "|---|---|---|---|---|---|---|---|---|\n")

	type detail struct{ facet, body string }
	var details []detail

	for _, fac := range studyFacets {
		var idx []int // rows with a gold label for this facet
		goldVals := make([]string, len(gold))
		ctxVals := make([]string, len(gold))
		noVals := make([]string, len(gold))
		for i := range gold {
			goldVals[i] = fieldOf(gold[i], fac)
			ctxVals[i] = fieldOf(ctxPred[i], fac)
			noVals[i] = fieldOf(noPred[i], fac)
			if goldVals[i] != "" {
				idx = append(idx, i)
			}
		}
		all := make([]int, len(gold))
		for i := range all {
			all[i] = i
		}
		gd := dist(goldVals, idx)
		majLabel, majN, majRate := topOf(gd)
		n := len(idx)

		// prediction distribution: over the scored subset when there is one,
		// else over every row (the only option for an unlabelled facet).
		predIdx := idx
		predScope := "scored rows"
		if n == 0 {
			predIdx, predScope = all, "ALL rows (facet has no gold labels)"
		}
		pdCtx := dist(ctxVals, predIdx)
		pdNo := dist(noVals, predIdx)
		topLabel, _, topShare := topOf(pdCtx)

		accCtx := ctxScore[fac]["accuracy"]
		accNo := noScore[fac]["accuracy"]
		rel := Calibration(gold, ctxPred, fac, 10)

		lift, p := math.NaN(), math.NaN()
		if n > 0 {
			lift = accCtx - majRate
			p = binomTail(int(math.Round(accCtx*float64(n))), n, majRate)
		}
		nStr, accCtxStr, accNoStr, baseStr, liftStr, pStr := fmt.Sprint(n), "n/a", "n/a", "n/a", "n/a", "n/a"
		if n > 0 {
			accCtxStr = fmt.Sprintf("%.3f", accCtx)
			accNoStr = fmt.Sprintf("%.3f", accNo)
			baseStr = fmt.Sprintf("%.3f (%s %d/%d)", majRate, majLabel, majN, n)
			liftStr = fmt.Sprintf("%+.3f", lift)
			pStr = fmt.Sprintf("%.2g", p)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s %.0f%% | %.3f |\n",
			fac, nStr, baseStr, accCtxStr, accNoStr, liftStr, pStr, topLabel, 100*topShare, rel.ECE)

		// detail block: distributions, confidence split, named misses
		var d strings.Builder
		fmt.Fprintf(&d, "### %s (n=%d)\n\n", fac, n)
		fmt.Fprintf(&d, "gold dist      %s\n", fmtDist(gd))
		fmt.Fprintf(&d, "pred CTX       %s   [%s]\n", fmtDist(pdCtx), predScope)
		fmt.Fprintf(&d, "pred NOCTX     %s\n", fmtDist(pdNo))
		// confidence on hits vs misses
		var hitC, missC float64
		var hitN, missN int
		for _, i := range idx {
			c := ctxPred[i].Conf[fac]
			if ctxVals[i] == goldVals[i] {
				hitC += c
				hitN++
			} else {
				missC += c
				missN++
			}
		}
		if hitN > 0 || missN > 0 {
			mean := func(s float64, k int) float64 {
				if k == 0 {
					return math.NaN()
				}
				return s / float64(k)
			}
			fmt.Fprintf(&d, "mean conf      correct %.3f (n=%d)   wrong %.3f (n=%d)\n",
				mean(hitC, hitN), hitN, mean(missC, missN), missN)
		}
		// every miss, named, up to a cap
		shown := 0
		fmt.Fprintf(&d, "\nmisses (row index · gold → predicted @conf · prompt):\n")
		for _, i := range idx {
			if ctxVals[i] == goldVals[i] {
				continue
			}
			if shown >= 25 {
				fmt.Fprintf(&d, "  … %d further misses (see rows.ndjson)\n", missN-shown)
				break
			}
			shown++
			fmt.Fprintf(&d, "  #%-3d %-22s → %-22s @%.3f  %q\n",
				i, goldVals[i], orAbstain(ctxVals[i]), ctxPred[i].Conf[fac], trim(gold[i].Text, 110))
		}
		details = append(details, detail{fac, d.String()})
	}

	fmt.Fprintf(&b, "\n## Per-facet detail\n\n")
	for _, d := range details {
		fmt.Fprintf(&b, "```\n%s```\n\n", d.body)
	}

	raw := b.String()
	if err := os.WriteFile(filepath.Join(outDir, "RESULTS-raw.md"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + raw)
	t.Logf("artifacts: %s/{rows.ndjson,RESULTS-raw.md}", outDir)
}

func orAbstain(s string) string {
	if s == "" {
		return "(abstain)"
	}
	return s
}
