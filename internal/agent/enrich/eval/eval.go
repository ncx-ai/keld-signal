// Package eval scores an enrich.Model's pipeline output against a gold set.
// Ported from inference-enrichment/services/api/app/eval.
package eval

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"strings"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

//go:embed gold.jsonl
var goldJSONL string

//go:embed confound.jsonl
var confoundJSONL string

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
	Subcategory   string   `json:"subcategory"`
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

// Meta builds the enrich.Meta an augmented run would see for this gold row.
func (r GoldRow) Meta(source string) enrich.Meta {
	return enrich.Meta{
		Repo:          r.Repo,
		Tool:          source,
		GitBranch:     r.Branch,
		Project:       r.Project,
		RecentPrompts: r.RecentPrompts,
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
func LoadGold() ([]GoldRow, error) { return parseRows(goldJSONL) }

// LoadConfound parses the embedded confound eval set (classes c1/c2/c3).
func LoadConfound() ([]GoldRow, error) { return parseRows(confoundJSONL) }

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
		}
	}
	return ""
}

// Score computes per-field accuracy and, for "sensitivity", sensitive_recall
// (recall over rows whose gold sensitivity != "none"; 1.0 when there are none).
//
// A blank gold value for a field is treated as "no label" and excluded from
// that field's accuracy denominator — this lets optional facets (e.g.
// activity_type, subcategory) coexist with older gold rows that predate them,
// without those rows counting as misses. If a field has no labeled rows at
// all, its accuracy is reported as 1.0 (vacuous), mirroring the
// sensitive_recall convention below.
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
		acc := 1.0
		if considered > 0 {
			acc = float64(correct) / float64(considered)
		}
		entry := map[string]float64{"accuracy": acc}
		if f == "sensitivity" {
			sens, hit := 0, 0
			for i := 0; i < n; i++ {
				if fieldOf(gold[i], f) != "none" {
					sens++
					if fieldOf(gold[i], f) == fieldOf(pred[i], f) {
						hit++
					}
				}
			}
			if sens > 0 {
				entry["sensitive_recall"] = float64(hit) / float64(sens)
			} else {
				entry["sensitive_recall"] = 1.0
			}
		}
		metrics[f] = entry
	}
	return metrics
}

// RunModel scores a backend by running the enrichment pipeline over each gold
// row and extracting the classified fields.
func RunModel(m enrich.Model, gold []GoldRow) []Pred {
	pred := make([]Pred, 0, len(gold))
	for _, g := range gold {
		p := enrich.Run(g.Text, "eval", enrich.Meta{}, m)
		pred = append(pred, Pred{
			TaskType:      p.TaskType.Value,
			Domain:        p.Domain.Value,
			Sensitivity:   p.Sensitivity.Value,
			Activity:      p.Activity.Value,
			FunctionGuess: p.FunctionGuess.Value,
			Subcategory:   p.Subcategory.Value,
		})
	}
	return pred
}

// RunModelWithContext is RunModel but feeds each gold row's session context
// (recent prompts, branch, project) into the classifier via GoldRow.Meta, so
// augmented classification can be scored against the no-context baseline.
func RunModelWithContext(m enrich.Model, gold []GoldRow) []Pred {
	pred := make([]Pred, 0, len(gold))
	for _, g := range gold {
		src := g.srcOr()
		p := enrich.Run(g.Text, src, g.Meta(src), m)
		pred = append(pred, Pred{
			TaskType: p.TaskType.Value, Domain: p.Domain.Value, Sensitivity: p.Sensitivity.Value,
			Activity: p.Activity.Value, FunctionGuess: p.FunctionGuess.Value, Subcategory: p.Subcategory.Value,
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
