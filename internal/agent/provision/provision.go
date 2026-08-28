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

// EnsureModel makes dir contain a verified model. If already present and its
// sentinel matches wantSHA, it's a no-op. Otherwise it fetches into a temp dir,
// verifies, and atomically renames into place. On mismatch nothing is installed.
func EnsureModel(ctx context.Context, dir, wantSHA string, f Fetcher) error {
	if got, err := fileSHA(filepath.Join(dir, sentinel)); err == nil && got == wantSHA {
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
	got, err := fileSHA(filepath.Join(tmp, sentinel))
	if err != nil {
		return fmt.Errorf("fetched model missing %s: %w", sentinel, err)
	}
	if got != wantSHA {
		return fmt.Errorf("model sha mismatch: got %s want %s", got, wantSHA)
	}
	_ = os.RemoveAll(dir)
	return os.Rename(tmp, dir)
}
