// Package spool is the on-disk fallback queue for enrich pointers. The hook
// writes a pointer here when the daemon is unreachable; the daemon drains it on
// startup and on a periodic sweep. Only the pointer is stored — never prompt text.
package spool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ncx-ai/keld-signal/internal/debuglog"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

type Source struct {
	ID      string `json:"id"`
	Origin  string `json:"origin"`
	Version string `json:"version,omitempty"`
}
type Correlation struct {
	Scheme    string `json:"scheme"`
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
}
type Ptr struct {
	TranscriptPath string `json:"transcript_path"`
	PromptID       string `json:"prompt_id"`
	Cwd            string `json:"cwd"`
}
type Inline struct {
	Text string `json:"text"`
}

// Pointer is the enrich payload — identical JSON shape to the /enrich body.
type Pointer struct {
	Source      Source      `json:"source"`
	Correlation Correlation `json:"correlation"`
	Pointer     *Ptr        `json:"pointer,omitempty"`
	Inline      *Inline     `json:"inline,omitempty"`
}

func maxFiles() int {
	if v := os.Getenv("KELD_SPOOL_MAX"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 500
}

var safe = strings.NewReplacer("/", "_", "\\", "_", "..", "_", string(os.PathSeparator), "_")

func fileName(p Pointer) string {
	id := safe.Replace(p.Correlation.ID)
	if id == "" {
		id = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return id + ".json"
}

// jsonFiles returns spool/*.json sorted oldest-first by mtime.
func jsonFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type fe struct {
		path string
		mod  time.Time
	}
	var fs []fe
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		fs = append(fs, fe{filepath.Join(dir, e.Name()), info.ModTime()})
	}
	sort.Slice(fs, func(i, j int) bool { return fs[i].mod.Before(fs[j].mod) })
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.path
	}
	return out
}

// Write atomically persists a pointer, enforcing the cap first.
func Write(p Pointer) error {
	dir := paths.SpoolDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Enforce cap: drop oldest so we keep at most maxFiles()-1 before adding one.
	if files := jsonFiles(dir); len(files) >= maxFiles() {
		drop := len(files) - maxFiles() + 1
		for i := 0; i < drop && i < len(files); i++ {
			os.Remove(files[i])
		}
		debuglog.Append("spool: cap %d reached, dropped %d oldest", maxFiles(), drop)
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	final := filepath.Join(dir, fileName(p))
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// Drain applies fn to each spooled pointer oldest-first. On fn success the file
// is deleted; on fn error it is left for the next sweep; on decode error it is
// quarantined to spool/bad/. Returns the number successfully drained.
func Drain(fn func(Pointer) error) (int, error) {
	dir := paths.SpoolDir()
	n := 0
	for _, path := range jsonFiles(dir) {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var p Pointer
		if err := json.Unmarshal(b, &p); err != nil {
			quarantine(dir, path)
			continue
		}
		if err := fn(p); err != nil {
			continue // leave for retry
		}
		os.Remove(path)
		n++
	}
	return n, nil
}

// Quarantine writes a pointer directly to spool/bad/ instead of the live spool,
// so it is preserved for inspection but never drained/retried again. The daemon
// uses this for a job that has repeatedly exceeded its deadline — bounding
// re-spool so one un-enrichable job can't retry forever.
func Quarantine(p Pointer) error {
	bad := filepath.Join(paths.SpoolDir(), "bad")
	if err := os.MkdirAll(bad, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	final := filepath.Join(bad, fileName(p))
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	debuglog.Append("spool: quarantined un-enrichable pointer %s", filepath.Base(final))
	return os.Rename(tmp, final)
}

func quarantine(dir, path string) {
	bad := filepath.Join(dir, "bad")
	if os.MkdirAll(bad, 0o700) == nil {
		if err := os.Rename(path, filepath.Join(bad, filepath.Base(path))); err != nil {
			os.Remove(path) // last resort: never let poison block the drain
		}
		debuglog.Append("spool: quarantined poison file %s", filepath.Base(path))
	}
}
