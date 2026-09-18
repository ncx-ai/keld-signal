package integrations

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// ReaderAvailable is a claim about a FILE, so assert it against the file.
//
// ⚠️ It was left false for codex after readers/codex.py landed, and the failure
// is the quiet kind: with the reader lane not expected, a Codex machine whose
// reader had broken would read `idle` rather than `broken · reader` — the state
// rule cannot report a lane it was told not to expect. Found by a survey reading
// the two sources side by side, not by any test.
//
// This keeps the flag and the file one fact. A reader deleted or renamed fails
// here; a reader added with the flag left false fails here too.
func TestReaderAvailableMatchesTheSidecarReadersOnDisk(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("no caller info")
	}
	root := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	readers := filepath.Join(root, "sidecar", "app", "analysis", "readers")
	if _, err := os.Stat(readers); err != nil {
		t.Skipf("no readers directory at %s", readers)
	}

	// A reader file is named for the SOURCE it reads. cowork is read by the
	// claude reader (it is Claude Code in a sandbox) and has no file of its own.
	readBy := map[string]string{"claude_code": "claude.py", "cowork": "claude.py", "codex": "codex.py"}

	for _, e := range Catalogue {
		file, named := readBy[e.ID]
		onDisk := false
		if named {
			_, err := os.Stat(filepath.Join(readers, file))
			onDisk = err == nil
		}
		if e.ReaderAvailable != onDisk {
			t.Errorf("%s: ReaderAvailable=%v but reader on disk=%v (%s). "+
				"A false flag hides a broken reader as `idle`; a true flag with no reader reports `broken` forever.",
				e.ID, e.ReaderAvailable, onDisk, file)
		}
	}
}
