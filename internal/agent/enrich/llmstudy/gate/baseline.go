package gate

import (
	"embed"
	"encoding/json"
	"fmt"
)

// baselineFS carries the committed baseline into the binary, so the gate cannot be run
// against a baseline someone edited on disk and forgot to commit — the comparison is only
// worth anything if the thing being compared against is in version control.
//
//go:embed baseline.json
var baselineFS embed.FS

// LoadBaseline returns the committed baseline.
func LoadBaseline() (*Baseline, error) {
	b, err := baselineFS.ReadFile("baseline.json")
	if err != nil {
		return nil, err
	}
	var out Baseline
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("baseline.json: %w", err)
	}
	if out.On == nil || out.Off == nil {
		return nil, fmt.Errorf("baseline.json must carry BOTH arms: an ablation with one arm " +
			"measured is what made the published anchor cost unattributable")
	}
	return &out, nil
}

// Marshal renders a baseline as the committed file's own formatting, so writing a new
// baseline produces a diff a reader can read rather than a reordered blob.
func (b *Baseline) Marshal() ([]byte, error) {
	out, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}
