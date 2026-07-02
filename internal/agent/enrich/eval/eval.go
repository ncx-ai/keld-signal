// Package eval scores an enrich.Model's pipeline output against a gold set.
// Ported from inference-enrichment/services/api/app/eval.
package eval

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"strings"

	"github.com/ncx-ai/keld-cli/internal/agent/enrich"
)

//go:embed gold.jsonl
var goldJSONL string

// GoldRow is one labeled evaluation example.
//
// Activity and Subcategory are optional (schema-v2 facets, Tasks 4-5): older
// rows leave them blank, and Score treats a blank gold value for a field as
// "not scored" rather than counting it as a miss.
type GoldRow struct {
	Text        string `json:"text"`
	TaskType    string `json:"task_type"`
	Domain      string `json:"domain"`
	Sensitivity string `json:"sensitivity"`
	Activity    string `json:"activity_type"`
	Subcategory string `json:"subcategory"`
}

// Pred is one model prediction for the scored fields.
type Pred struct {
	TaskType    string
	Domain      string
	Sensitivity string
	Activity    string
	Subcategory string
}

// LoadGold parses the embedded gold set.
func LoadGold() ([]GoldRow, error) {
	var out []GoldRow
	sc := bufio.NewScanner(strings.NewReader(goldJSONL))
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
			TaskType:    p.TaskType.Value,
			Domain:      p.Domain.Value,
			Sensitivity: p.Sensitivity.Value,
			Activity:    p.Activity.Value,
			Subcategory: p.Subcategory.Value,
		})
	}
	return pred
}
