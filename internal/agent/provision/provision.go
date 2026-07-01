// Package provision fetches + verifies the GLiNER2 model into a local dir.
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
	tmp, err := os.MkdirTemp(filepath.Dir(dir), ".gliner2-dl-*")
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
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, dir)
}
