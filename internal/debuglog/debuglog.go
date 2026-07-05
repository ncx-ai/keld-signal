// Package debuglog writes a finite-size, best-effort debug log under ~/.keld.
// It records otherwise-silent errors (e.g. hook->daemon POST failures) without
// ever blocking the caller. Callers must never pass prompt text to it.
package debuglog

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// MaxBytes caps the active log file. When it is reached the file rotates to
// <path>.1, bounding total on-disk usage to ~2*MaxBytes. Exported as a var so
// tests can shrink it.
var MaxBytes int64 = 1 << 20 // 1 MiB

var mu sync.Mutex

// Append writes a timestamped line. Best-effort: every error is swallowed.
func Append(format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()

	if err := os.MkdirAll(paths.KeldHome(), 0o755); err != nil {
		return
	}
	path := paths.DebugLogPath()
	if st, err := os.Stat(path); err == nil && st.Size() >= MaxBytes {
		_ = os.Rename(path, path+".1") // overwrites any previous .1
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(time.Now().UTC().Format(time.RFC3339) + " " + fmt.Sprintf(format, args...) + "\n")
}
