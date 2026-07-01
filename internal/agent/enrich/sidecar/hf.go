// Package sidecar — HFFetcher downloads a Hugging Face model snapshot into a
// local directory so the GLiNER2 sidecar can load it via from_pretrained(local_dir).
package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// HFFetcher implements provision.Fetcher by downloading every file listed in a
// Hugging Face model revision's siblings manifest. It writes each file atomically
// (temp-file then rename) so a partial download never leaves a corrupt file.
type HFFetcher struct {
	repo    string
	rev     string
	baseURL string
	hc      *http.Client
}

// NewHFFetcher returns an HFFetcher targeting the given repo and revision.
// baseURL defaults to https://huggingface.co; it is exported as a field so
// tests can point it at an httptest server.
func NewHFFetcher(repo, rev string) *HFFetcher {
	return &HFFetcher{
		repo:    repo,
		rev:     rev,
		baseURL: "https://huggingface.co",
		hc:      &http.Client{Timeout: 30 * time.Minute},
	}
}

// revisionResp is the relevant portion of GET /api/models/{repo}/revision/{rev}.
type revisionResp struct {
	Siblings []struct {
		Rfilename string `json:"rfilename"`
	} `json:"siblings"`
}

// Fetch downloads the full model snapshot into destDir. It first fetches the
// revision manifest to obtain the list of files, then downloads each one
// atomically. ctx cancellation is honoured on every request.
func (f *HFFetcher) Fetch(ctx context.Context, destDir string) error {
	// 1. Fetch the siblings manifest.
	apiURL := fmt.Sprintf("%s/api/models/%s/revision/%s", f.baseURL, f.repo, f.rev)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return fmt.Errorf("hf: build revision request: %w", err)
	}
	resp, err := f.hc.Do(req)
	if err != nil {
		return fmt.Errorf("hf: revision request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("hf: revision endpoint returned %d", resp.StatusCode)
	}
	var rev revisionResp
	if err := json.NewDecoder(resp.Body).Decode(&rev); err != nil {
		return fmt.Errorf("hf: decode revision response: %w", err)
	}

	// 2. Download each file.
	for _, s := range rev.Siblings {
		if err := f.fetchFile(ctx, destDir, s.Rfilename); err != nil {
			return err
		}
	}
	return nil
}

// fetchFile downloads a single rfilename from the resolve endpoint into
// destDir/{rfilename}, writing atomically via a temp file.
func (f *HFFetcher) fetchFile(ctx context.Context, destDir, rfilename string) error {
	url := fmt.Sprintf("%s/%s/resolve/%s/%s", f.baseURL, f.repo, f.rev, rfilename)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("hf: build request for %s: %w", rfilename, err)
	}
	resp, err := f.hc.Do(req)
	if err != nil {
		return fmt.Errorf("hf: request for %s: %w", rfilename, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("hf: %s returned %d", rfilename, resp.StatusCode)
	}

	destPath := filepath.Join(destDir, rfilename)
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("hf: mkdirall for %s: %w", rfilename, err)
	}

	// Write to a temp file in the same dir, then rename for atomicity.
	tmp, err := os.CreateTemp(filepath.Dir(destPath), ".hf-dl-*")
	if err != nil {
		return fmt.Errorf("hf: create temp for %s: %w", rfilename, err)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName) // no-op if rename succeeded
	}()

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		return fmt.Errorf("hf: write %s: %w", rfilename, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("hf: close temp for %s: %w", rfilename, err)
	}
	if err := os.Rename(tmpName, destPath); err != nil {
		return fmt.Errorf("hf: rename %s: %w", rfilename, err)
	}
	return nil
}
