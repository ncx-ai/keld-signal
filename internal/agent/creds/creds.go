// Package creds holds credentials that can be live-swapped while the daemon
// is running (e.g. the org ingest token, rotated after a self-heal re-auth)
// without restarting the consumers that read them.
package creds

import "sync/atomic"

// Token is a concurrency-safe holder for a single string credential. Callers
// read it through Get (typically passed around as the method value tok.Get,
// type func() string) so a later Set is observed by every consumer sharing
// the Token.
type Token struct {
	v atomic.Value // holds string
}

// NewToken returns a Token initialized to s.
func NewToken(s string) *Token {
	t := &Token{}
	t.v.Store(s)
	return t
}

// Get returns the current value.
func (t *Token) Get() string {
	v := t.v.Load()
	if v == nil {
		return ""
	}
	return v.(string)
}

// Set updates the current value.
func (t *Token) Set(s string) {
	t.v.Store(s)
}
