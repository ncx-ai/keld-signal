package gate

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
)

// baselineFS carries the committed baseline into the binary, so the gate cannot be run
// against a baseline someone edited on disk and forgot to commit — the comparison is only
// worth anything if the thing being compared against is in version control.
//
//go:embed baseline.json
var baselineFS embed.FS

// LoadBaseline returns the committed baseline, or the one named by GATE_BASELINE_FILE.
//
// The override exists for ATTRIBUTION, not for convenience. A stack of gated changes each compare
// against the committed baseline — that is the contract, and it is what catches an accumulated
// regression — but "what did THIS step change" needs the previous step as the reference, and
// deriving that by reading two logs side by side is exactly the manual comparison this package
// replaced. The committed file remains the default and the one that governs; a run using the
// override says so in its own output.
func LoadBaseline() (*Baseline, error) {
	b, err := baselineFS.ReadFile("baseline.json")
	if p := os.Getenv("GATE_BASELINE_FILE"); p != "" {
		b, err = os.ReadFile(p)
	}
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
