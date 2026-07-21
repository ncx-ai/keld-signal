// Package watch is the daemon's hook-free capture trigger: it tails Claude-Code
// -format JSONL transcripts (Claude Code and Cowork) and synthesizes enrich
// pointers into the same pipeline the command hook feeds.
package watch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// CursorStore persists per-transcript byte offsets so a daemon restart does not
// reprocess already-seen lines. Concurrency-safe; callers Save() after a poll.
type CursorStore struct {
	path string
	mu   sync.Mutex
	off  map[string]int64
}

// NewCursorStore returns the production store under paths.WatchDir().
func NewCursorStore() *CursorStore {
	return newCursorStoreAt(filepath.Join(paths.WatchDir(), "cursors.json"))
}

func newCursorStoreAt(path string) *CursorStore {
	cs := &CursorStore{path: path, off: map[string]int64{}}
	cs.load()
	return cs
}

func (c *CursorStore) load() {
	b, err := os.ReadFile(c.path)
	if err != nil {
		return
	}
	var m map[string]int64
	if json.Unmarshal(b, &m) == nil && m != nil {
		c.off = m
	}
}

// Get returns the stored offset for path and whether path has been seen.
func (c *CursorStore) Get(path string) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.off[path]
	return v, ok
}

// Set records path's offset.
func (c *CursorStore) Set(path string, off int64) {
	c.mu.Lock()
	c.off[path] = off
	c.mu.Unlock()
}

// Save atomically persists all cursors.
func (c *CursorStore) Save() error {
	c.mu.Lock()
	b, err := json.Marshal(c.off)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}
