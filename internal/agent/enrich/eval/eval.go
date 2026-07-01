// Package eval scores an enrich.Model's pipeline output against a gold set.
// Ported from inference-enrichment/services/api/app/eval.
package eval

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed gold.jsonl
var goldJSONL string

// GoldRow is one labeled evaluation example.
type GoldRow struct {
	Text        string `json:"text"`
	TaskType    string `json:"task_type"`
	Domain      string `json:"domain"`
	Sensitivity string `json:"sensitivity"`
}

// Pred is one model prediction for the scored fields.
type Pred struct {
	TaskType    string
	Domain      string
	Sensitivity string
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
		}
	case Pred:
		switch f {
		case "task_type":
			return v.TaskType
		case "domain":
			return v.Domain
		case "sensitivity":
			return v.Sensitivity
		}
	}
	return ""
}

// Score computes per-field accuracy and, for "sensitivity", sensitive_recall
// (recall over rows whose gold sensitivity != "none"; 1.0 when there are none).
func Score(gold []GoldRow, pred []Pred, fields []string) map[string]map[string]float64 {
	metrics := map[string]map[string]float64{}
	n := len(gold)
	if len(pred) < n {
		n = len(pred)
	}
	total := len(gold)
	if total == 0 {
		total = 1
	}
	for _, f := range fields {
		correct := 0
		for i := 0; i < n; i++ {
			if fieldOf(gold[i], f) == fieldOf(pred[i], f) {
				correct++
			}
		}
		entry := map[string]float64{"accuracy": float64(correct) / float64(total)}
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
