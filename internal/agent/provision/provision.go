// Package provision fetches + verifies a model into a local dir. Two callers:
// the GLiNER2 inference model (daemon/model_on_demand.go) and the text encoder
// (daemon/encoder_on_demand.go). Both are Hugging Face snapshots whose sentinel
// is model.safetensors, so one EnsureModel serves both — deliberately, rather
// than a second fetch-and-verify with its own staging and atomicity bugs.
package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const sentinel = "model.safetensors"

type Fetcher interface {
	Fetch(ctx context.Context, destDir string) error
}

// fileSHA streams the file at path through SHA-256 without loading it into
// memory — safe for multi-GB model files.
func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// EnsureFile makes dir contain a verified file at sentinelName. If already
// present and it matches wantSHA, it's a no-op. Otherwise it fetches into a
// temp dir, verifies, and atomically renames into place. On mismatch nothing
// is installed.
//
// This is EnsureModel's body with the sentinel threaded through as a
// parameter rather than the hardcoded "model.safetensors" — EnsureModel is
// now a two-line wrapper over this, and a third caller whose sentinel isn't a
// safetensors file at all (the attribution verifier's GGUF: "model.gguf")
// gets the same staging/atomicity/SHA-verification logic instead of a forked
// copy of it.
func EnsureFile(ctx context.Context, dir, sentinelName, wantSHA string, f Fetcher) error {
	if got, err := fileSHA(filepath.Join(dir, sentinelName)); err == nil && got == wantSHA {
		return nil
	}
	// The staging temp dir is created next to the final dir; os.MkdirTemp requires
	// that parent to already exist — on a fresh machine (~/.keld/models) it does not.
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	// Staging prefix names the model being fetched rather than hardcoding
	// ".gliner2-", so an abandoned temp dir under ~/.keld/models says which of
	// the two downloads left it. MkdirTemp already makes the name unique, so
	// this is legibility, not collision avoidance.
	tmp, err := os.MkdirTemp(parent, "."+filepath.Base(dir)+"-dl-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := f.Fetch(ctx, tmp); err != nil {
		return err
	}
	got, err := fileSHA(filepath.Join(tmp, sentinelName))
	if err != nil {
		return fmt.Errorf("fetched model missing %s: %w", sentinelName, err)
	}
	if got != wantSHA {
		return fmt.Errorf("model sha mismatch: got %s want %s", got, wantSHA)
	}
	_ = os.RemoveAll(dir)
	return os.Rename(tmp, dir)
}

// EnsureModel is EnsureFile specialised to the model.safetensors sentinel —
// the shape every Hugging-Face-snapshot caller (GLiNER2, the text encoder)
// uses.
func EnsureModel(ctx context.Context, dir, wantSHA string, f Fetcher) error {
	return EnsureFile(ctx, dir, sentinel, wantSHA, f)
}
