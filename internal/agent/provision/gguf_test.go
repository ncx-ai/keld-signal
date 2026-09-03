package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

type writeFetcher struct{ name, content string }

func (w writeFetcher) Fetch(_ context.Context, dest string) error {
	return os.WriteFile(filepath.Join(dest, w.name), []byte(w.content), 0o644)
}

// AC-10: EnsureFile verifies by SHA over the named sentinel and is a no-op
// when the file already matches.
func TestEnsureFileGGUF(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gemma-4-e2b")
	content := "fake-gguf-bytes"
	sum := sha256.Sum256([]byte(content))
	sha := hex.EncodeToString(sum[:])

	f := writeFetcher{"model.gguf", content}
	if err := EnsureFile(context.Background(), dir, "model.gguf", sha, f); err != nil {
		t.Fatal(err)
	}
	// second call: no re-fetch needed — corrupt-proof by replacing fetcher with a failer
	fail := failFetcher{}
	if err := EnsureFile(context.Background(), dir, "model.gguf", sha, fail); err != nil {
		t.Fatalf("EnsureFile re-fetched despite matching SHA: %v", err)
	}
	if err := EnsureFile(context.Background(), dir, "model.gguf", "0000", f); err == nil {
		t.Fatal("wrong SHA must fail, not accept")
	}
}

type failFetcher struct{}

func (failFetcher) Fetch(context.Context, string) error {
	panic("must not fetch when sentinel matches")
}
