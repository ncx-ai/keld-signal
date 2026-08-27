package teleproxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// StatePath is where the proxy records when telemetry last reached Atlas.
func StatePath() string { return filepath.Join(paths.StateDir(), "telemetry.json") }

// LastForwardOnDisk reads the recorded instant, and whether it is KNOWN.
//
// ⚠️ known=false is a first-class answer and must never be reported as a fault.
// The file is absent on a machine whose proxy has never run — a direct-push
// install, or one whose bind failed — and on one that has simply never forwarded
// yet. Neither is evidence that anything is broken.
func LastForwardOnDisk() (t time.Time, known bool) {
	data, err := os.ReadFile(StatePath())
	if err != nil {
		return time.Time{}, false
	}
	var s state
	if json.Unmarshal(data, &s) != nil {
		return time.Time{}, false
	}
	return s.LastForward, true
}
