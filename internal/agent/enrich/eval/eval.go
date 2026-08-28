// Package eval scores an enrich.Model's pipeline output against a gold set.
// Ported from inference-enrichment/services/api/app/eval.
package eval

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"math"
	"strings"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

//go:embed gold.jsonl
var goldJSONL string

//go:embed confound.jsonl
var confoundJSONL string

//go:embed creds.jsonl
var credsJSONL string

//go:embed agentic.jsonl
var agenticJSONL string

// GoldRow is one labeled evaluation example.
//
// Activity, FunctionGuess, and Subcategory are optional (schema-v2 job-category
// facets, Tasks 4-6): older rows leave them blank, and Score treats a blank
// gold value for a field as "not scored" rather than counting it as a miss.
type GoldRow struct {
	Text          string   `json:"text"`
	Class         string   `json:"class"`
	Source        string   `json:"source"`         // tool source for context runs; blank ⇒ claude_code
	RecentPrompts []string `json:"recent_prompts"` // optional preceding user prompts (newest-first)
	Repo          string   `json:"repo"`
	Branch        string   `json:"branch"`
	Project       string   `json:"project"`
	TaskType      string   `json:"task_type"`
	Domain        string   `json:"domain"`
	Sensitivity   string   `json:"sensitivity"`
	Activity      string   `json:"activity_type"`
	FunctionGuess string   `json:"function_guess"`
	// SpeechAct is RETAINED gold data for a facet that no longer ships. The
	// pass was dropped at schema v9 for scoring below a constant, but the
	// labels cost nothing to keep and they are the only evidence a
	// re-introduction could be judged against — the study named the label
	// WORDING as the suspect, and a re-bakeoff needs a corpus to bake off
	// against. Nothing predicts the facet, so nothing scores it: there is no
	// `speech_act` case in fieldOf and no Pred field, which is what stops the
	// harness reporting a number for a facet with no producer.
	SpeechAct   string `json:"speech_act"`
	Subcategory string `json:"subcategory"`
	// Personal is the work-vs-personal facet's gold label. NO gold row carries
	// one today (the field exists so the facet is scoreable the day labels are
	// written); until then Score reports the facet with considered==0 and NO
	// accuracy at all, so it reads as unscoreable rather than perfect.
	Personal string `json:"personal"`

	// Agentic-corpus fields (agentic.jsonl): shape ∈ {clean, raw}; the rest are
	// the agentic Meta augmentation.
	Shape       string   `json:"shape"`
	Framework   string   `json:"framework"`
	AgentRole   string   `json:"agent_role"`
	Workflow    string   `json:"workflow"`
	Step        string   `json:"step"`
	RecentSteps []string `json:"recent_steps"`
}

// srcOr returns the row's tool source, defaulting to claude_code. Confound c2
// (genuine non-eng) rows set source to a generic tool so a tool-conditioned
// rule (A4) does not force them to eng — keeping false_eng honest.
func (r GoldRow) srcOr() string {
	if r.Source != "" {
		return r.Source
	}
	return "claude_code"
}

// Meta builds the enrich.Meta an augmented run would see for this gold row,
// including agentic-framework context when present.
func (r GoldRow) Meta(source string) enrich.Meta {
	return enrich.Meta{
		Repo:          r.Repo,
		Tool:          source,
		GitBranch:     r.Branch,
		Project:       r.Project,
		RecentPrompts: r.RecentPrompts,
		Framework:     r.Framework,
		AgentRole:     r.AgentRole,
		Workflow:      r.Workflow,
		Step:          r.Step,
		RecentSteps:   r.RecentSteps,
	}
}

// Pred is one model prediction for the scored fields.
type Pred struct {
	TaskType      string
	Domain        string
	Sensitivity   string
	Activity      string
	FunctionGuess string
	Subcategory   string
	Personal      string
	Conf          map[string]float64 // facet name -> top-label confidence (for calibration)
}

// predConf captures each facet's top-label confidence from a Profile, keyed by
// the same facet names fieldOf uses.
func predConf(p enrich.Profile) map[string]float64 {
	return map[string]float64{
		"task_type":      p.TaskType.Confidence,
		"domain":         p.Domain.Confidence,
		"sensitivity":    p.Sensitivity.Confidence,
		"activity_type":  p.Activity.Confidence,
		"function_guess": p.FunctionGuess.Confidence,
		"subcategory":    p.Subcategory.Confidence,
		"personal":       p.Personal.Confidence,
	}
}

// parseRows is a shared helper that parses JSONL rows into GoldRow objects.
func parseRows(s string) ([]GoldRow, error) {
	var out []GoldRow
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r GoldRow
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// LoadGold parses the embedded gold set.
//
// Like LoadCreds, it expands any {{SHAPE}} placeholder into a freshly generated
// synthetic credential (credsgen.go). Four of the gold set's sensitive rows are
// provider tokens, and a committed provider literal is a blocked push whichever
// fixture it sits in -- one of these four was surviving only behind a GitHub
// push-protection allowlist entry.
func LoadGold() ([]GoldRow, error) {
	rows, err := parseRows(goldJSONL)
	if err != nil {
		return nil, err
	}
	return expandCredRows(rows, GoldSeed)
}

// LoadConfound parses the embedded confound eval set (classes c1/c2/c3).
func LoadConfound() ([]GoldRow, error) { return parseRows(confoundJSONL) }

// LoadCreds parses the embedded credential-detection corpus (class "cred" =
// contains a real credential; class "decoy" = high-entropy/placeholder non-secret).
//
// The cred rows carry {{SHAPE}} placeholders rather than credential literals;
// they are expanded here into freshly generated, realistically shaped synthetic
// values from a fixed seed. See credsgen.go for why the shapes are not committed.
func LoadCreds() ([]GoldRow, error) {
	rows, err := parseRows(credsJSONL)
	if err != nil {
		return nil, err
	}
	return expandCredRows(rows, CredsSeed)
}

// LoadAgentic parses the embedded agentic-framework corpus (rows carry a Shape
// ∈ {clean, raw} and agentic Meta fields).
func LoadAgentic() ([]GoldRow, error) { return parseRows(agenticJSONL) }

// AccuracyByShape returns per-Shape [correct,total] for a facet over rows with a
// non-blank gold value for that facet. Used to compare clean-subtask vs raw-call
// classification on the agentic corpus.
func AccuracyByShape(gold []GoldRow, pred []Pred, facet string) map[string][2]int {
	n := len(gold)
	if len(pred) < n {
		n = len(pred)
	}
	out := map[string][2]int{}
	for i := 0; i < n; i++ {
		g := fieldOf(gold[i], facet)
		if g == "" {
			continue
		}
		shape := gold[i].Shape
		if shape == "" {
			shape = "(none)"
		}
		c := out[shape]
		c[1]++
		if g == fieldOf(pred[i], facet) {
			c[0]++
		}
		out[shape] = c
	}
	return out
}

func fieldOf(x any, f string) string {
	switch v := x.(type) {
	case GoldRow:
		switch f {
		case "task_type":
			return v.TaskType
		case "domain":
			return v.Domain
		case "sensitivity":
			return v.Sensitivity
		case "activity_type":
			return v.Activity
		case "function_guess":
			return v.FunctionGuess
		case "subcategory":
			return v.Subcategory
		case "personal":
			return v.Personal
		}
	case Pred:
		switch f {
		case "task_type":
			return v.TaskType
		case "domain":
			return v.Domain
		case "sensitivity":
			return v.Sensitivity
		case "activity_type":
			return v.Activity
		case "function_guess":
			return v.FunctionGuess
		case "subcategory":
			return v.Subcategory
		case "personal":
			return v.Personal
		}
	}
	return ""
}

// Score computes per-field accuracy and, for "sensitivity", sensitive_recall
// (recall over rows whose gold sensitivity != "none").
//
// A blank gold value for a field is treated as "no label" and excluded from
// that field's accuracy denominator — this lets optional facets (e.g.
// activity_type, subcategory) coexist with older gold rows that predate them,
// without those rows counting as misses.
//
// EVERY METRIC RIDES WITH ITS DENOMINATOR, AND A METRIC WITH AN EMPTY
// DENOMINATOR IS OMITTED, NOT INVENTED. Each entry always carries
// "considered" (accuracy's denominator) and, for sensitivity,
// "sensitive_considered" (recall's); "accuracy" / "sensitive_recall" are
// present only when the matching denominator is non-zero.
//
// Score used to report accuracy 1.0 for a field with NO labelled rows, and
// sensitive_recall 1.0 for a gold set with no sensitive rows — a vacuous truth
// published as a measurement. Every caller in this repo reads these maps
// directly and compares against a floor, so a facet whose labels were missing
// or misnamed passed every floor forever and a floor check would sleep through
// it. Found for real: gold.jsonl carries no `personal` label at all (0 of 165
// rows), and scoring that facet returned a silent perfect.
//
// Omitting the key is what makes the failure direction SAFE: a Go map read of
// an absent key yields 0.0, which fails any lower-bound floor, so an
// unscoreable facet now reads as a failure rather than a pass. A caller that
// wants to tell "unscoreable" from "scored badly" reads the denominator — that
// is what it is for.
//
// The sibling helpers in this file (SecretRecall, SecretFPR, LeakageRate,
// FalseEngRate, S1DownstreamBaseline) return a bare float64 and answer 0 on an
// empty denominator. For the recall-shaped ones 0 fails a lower-bound gate, so
// they are already fail-safe; for the RATE-shaped ones (SecretFPR,
// LeakageRate, FalseEngRate — all checked as upper bounds) 0 is the same
// vacuous pass in a different costume. They are left alone here because their
// single-float signature has no room for a denominator; the fixtures they read
// are embedded and non-empty, and TestCredDetectCorpusRecall pins the decoy
// count. Changing their shape is its own decision.
func Score(gold []GoldRow, pred []Pred, fields []string) map[string]map[string]float64 {
	metrics := map[string]map[string]float64{}
	n := len(gold)
	if len(pred) < n {
		n = len(pred)
	}
	for _, f := range fields {
		correct, considered := 0, 0
		for i := 0; i < n; i++ {
			g := fieldOf(gold[i], f)
			if g == "" {
				continue
			}
			considered++
			if g == fieldOf(pred[i], f) {
				correct++
			}
		}
		entry := map[string]float64{"considered": float64(considered)}
		if considered > 0 {
			entry["accuracy"] = float64(correct) / float64(considered)
		}
		if f == "sensitivity" {
			sens, hit := 0, 0
			for i := 0; i < n; i++ {
				// Recall is over rows with a LABELED sensitive class; blank gold is
				// "not scored" (as for accuracy), never a sensitive miss.
				if g := fieldOf(gold[i], f); g != "none" && g != "" {
					sens++
					if g == fieldOf(pred[i], f) {
						hit++
					}
				}
			}
			entry["sensitive_considered"] = float64(sens)
			if sens > 0 {
				entry["sensitive_recall"] = float64(hit) / float64(sens)
			}
		}
		metrics[f] = entry
	}
	return metrics
}

// RunModel scores a backend by running the enrichment pipeline over each gold
// row and extracting the classified fields.
// opts are passed through to enrich.Run. The sensitivity facet takes NO
// evidence from the Model — it reads the gitleaks credential layer and the
// PII scan (enrich.WithPIIScanner) — so a caller that scores sensitivity and
// wires no scanner is measuring credentials alone, not the facet.
func RunModel(m enrich.Model, gold []GoldRow, opts ...enrich.Option) []Pred {
	pred := make([]Pred, 0, len(gold))
	for _, g := range gold {
		p := enrich.Run(g.Text, "eval", enrich.Meta{}, m, opts...)
		pred = append(pred, Pred{
			TaskType:      p.TaskType.Value,
			Domain:        p.Domain.Value,
			Sensitivity:   p.Sensitivity.Value,
			Activity:      p.Activity.Value,
			FunctionGuess: p.FunctionGuess.Value,
			Subcategory:   p.Subcategory.Value,
			Personal:      p.Personal.Value,
			Conf:          predConf(p),
		})
	}
	return pred
}

// RunModelWithContext is RunModel but feeds each gold row's session context
// (recent prompts, branch, project) into the classifier via GoldRow.Meta, so
// augmented classification can be scored against the no-context baseline.
func RunModelWithContext(m enrich.Model, gold []GoldRow, opts ...enrich.Option) []Pred {
	pred := make([]Pred, 0, len(gold))
	for _, g := range gold {
		src := g.srcOr()
		p := enrich.Run(g.Text, src, g.Meta(src), m, opts...)
		pred = append(pred, Pred{
			TaskType: p.TaskType.Value, Domain: p.Domain.Value, Sensitivity: p.Sensitivity.Value,
			Activity: p.Activity.Value, FunctionGuess: p.FunctionGuess.Value, Subcategory: p.Subcategory.Value,
			Personal: p.Personal.Value,
			Conf:     predConf(p),
		})
	}
	return pred
}

// LeakageRate measures subject-matter leakage over c1 rows (engineering activity,
// non-eng subject): the fraction whose predicted facet != the gold eng-correct
// value. Reported for function_guess and task_type. 0 when there are no c1 rows.
func LeakageRate(gold []GoldRow, pred []Pred) map[string]float64 {
	n := len(gold)
	if len(pred) < n {
		n = len(pred)
	}
	var c1, fLeak, tLeak int
	for i := 0; i < n; i++ {
		if gold[i].Class != "c1" {
			continue
		}
		c1++
		if pred[i].FunctionGuess != gold[i].FunctionGuess {
			fLeak++
		}
		if gold[i].TaskType != "" && pred[i].TaskType != gold[i].TaskType {
			tLeak++
		}
	}
	out := map[string]float64{"function_guess": 0, "task_type": 0}
	if c1 > 0 {
		out["function_guess"] = float64(fLeak) / float64(c1)
		out["task_type"] = float64(tLeak) / float64(c1)
	}
	return out
}

// FalseEngRate measures over-correction over c2 rows (genuine non-eng work):
// the fraction wrongly predicted function_guess == "eng". 0 when no c2 rows.
func FalseEngRate(gold []GoldRow, pred []Pred) float64 {
	n := len(gold)
	if len(pred) < n {
		n = len(pred)
	}
	var c2, wrong int
	for i := 0; i < n; i++ {
		if gold[i].Class != "c2" {
			continue
		}
		c2++
		if pred[i].FunctionGuess == "eng" {
			wrong++
		}
	}
	if c2 == 0 {
		return 0
	}
	return float64(wrong) / float64(c2)
}

// S1DownstreamBaseline measures the CURRENT (unconditioned) downstream error on
// the speech-act adversarial class s1: over s1 rows, the fraction of trapped
// (row, facet) pairs where prediction != gold. The trapped facets are whichever
// of task_type / activity_type the row sets a gold value for. This is the
// headroom number the future speech-act conditioning lever must beat. 0 when no
// s1 rows.
func S1DownstreamBaseline(gold []GoldRow, pred []Pred) float64 {
	n := len(gold)
	if len(pred) < n {
		n = len(pred)
	}
	var pairs, wrong int
	for i := 0; i < n; i++ {
		if gold[i].Class != "s1" {
			continue
		}
		for _, f := range []string{"task_type", "activity_type"} {
			g := fieldOf(gold[i], f)
			if g == "" {
				continue
			}
			pairs++
			if g != fieldOf(pred[i], f) {
				wrong++
			}
		}
	}
	if pairs == 0 {
		return 0
	}
	return float64(wrong) / float64(pairs)
}

// SecretRecall: over rows whose gold sensitivity is "secrets", the fraction
// predicted "secrets". 0 when there are none.
func SecretRecall(gold []GoldRow, pred []Pred) float64 {
	n := len(gold)
	if len(pred) < n {
		n = len(pred)
	}
	var tot, hit int
	for i := 0; i < n; i++ {
		if gold[i].Sensitivity != "secrets" {
			continue
		}
		tot++
		if pred[i].Sensitivity == "secrets" {
			hit++
		}
	}
	if tot == 0 {
		return 0
	}
	return float64(hit) / float64(tot)
}

// SecretFPR: over decoy rows (class "decoy", gold sensitivity "none"), the
// fraction wrongly predicted "secrets". 0 when there are none.
func SecretFPR(gold []GoldRow, pred []Pred) float64 {
	n := len(gold)
	if len(pred) < n {
		n = len(pred)
	}
	var tot, wrong int
	for i := 0; i < n; i++ {
		if gold[i].Class != "decoy" {
			continue
		}
		tot++
		if pred[i].Sensitivity == "secrets" {
			wrong++
		}
	}
	if tot == 0 {
		return 0
	}
	return float64(wrong) / float64(tot)
}

// Bin is one confidence band's reliability stats.
type Bin struct {
	Lo, Hi   float64
	Count    int
	MeanConf float64
	Accuracy float64
}

// Reliability is a facet's confidence-stratified accuracy + calibration error.
type Reliability struct {
	Facet string
	Bins  []Bin // non-empty bins, ascending
	N     int
	ECE   float64
}

// Calibration stratifies a facet's predictions into nbins fixed-width confidence
// bins ([0,1/nbins)…[1-1/nbins,1.0], top bin closed) and computes per-bin accuracy
// + the facet's Expected Calibration Error (Σ (n_bin/N)·|acc_bin − meanConf_bin|).
// Rows with a blank gold label for the facet are excluded.
func Calibration(gold []GoldRow, pred []Pred, facet string, nbins int) Reliability {
	if nbins <= 0 {
		nbins = 10
	}
	n := len(gold)
	if len(pred) < n {
		n = len(pred)
	}
	type acc struct {
		count   int
		sumConf float64
		correct int
	}
	bins := make([]acc, nbins)
	total := 0
	for i := 0; i < n; i++ {
		g := fieldOf(gold[i], facet)
		if g == "" {
			continue
		}
		c := 0.0
		if pred[i].Conf != nil {
			c = pred[i].Conf[facet]
		}
		b := int(c * float64(nbins))
		if b >= nbins { // c == 1.0 lands in the top bin
			b = nbins - 1
		}
		if b < 0 {
			b = 0
		}
		bins[b].count++
		bins[b].sumConf += c
		if g == fieldOf(pred[i], facet) {
			bins[b].correct++
		}
		total++
	}
	r := Reliability{Facet: facet, N: total}
	if total == 0 {
		return r
	}
	width := 1.0 / float64(nbins)
	for i, a := range bins {
		if a.count == 0 {
			continue
		}
		binAcc := float64(a.correct) / float64(a.count)
		binConf := a.sumConf / float64(a.count)
		hi := float64(i+1) * width
		if i == nbins-1 {
			hi = 1.0
		}
		r.Bins = append(r.Bins, Bin{Lo: float64(i) * width, Hi: hi, Count: a.count, MeanConf: binConf, Accuracy: binAcc})
		r.ECE += float64(a.count) / float64(total) * math.Abs(binAcc-binConf)
	}
	return r
}
