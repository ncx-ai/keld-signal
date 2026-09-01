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
	"strconv"
	"strings"
	"time"

	"github.com/ncx-ai/keld-signal/internal/retry"
)

// HFFetcher implements provision.Fetcher by downloading every file listed in a
// Hugging Face model revision's siblings manifest. It writes each file atomically
// (temp-file then rename) so a partial download never leaves a corrupt file.
type HFFetcher struct {
	repo    string
	rev     string
	baseURL string
	hc      *http.Client
	// Policy governs retry/backoff for the revision-manifest fetch and each
	// per-file download. Exported so tests can inject a fast policy.
	Policy retry.Policy
	// only restricts Fetch to exactly these rfilenames when non-nil (see
	// WithFiles). nil means "every sibling nonModelFile doesn't deny" — the
	// snapshot behaviour every existing caller (GLiNER2, the text encoder)
	// relies on.
	only map[string]bool
}

// WithFiles restricts Fetch to only the named files, skipping every other
// sibling in the revision manifest — including ones nonModelFile would have
// kept. It exists for a repo that ships a single file this fetcher wants (the
// attribution verifier's GGUF) rather than a whole snapshot of
// config/tokenizer siblings the way GLiNER2 and the text encoder are fetched.
// Returns f for chaining at the construction site.
func (f *HFFetcher) WithFiles(names ...string) *HFFetcher {
	f.only = make(map[string]bool, len(names))
	for _, n := range names {
		f.only[n] = true
	}
	return f
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
		Policy:  retry.DefaultPolicy(),
	}
}

// hfStatus turns a non-2xx response into a retry.StatusError carrying
// Retry-After, so retry.Do's classifier can judge transient vs. permanent.
func hfStatus(resp *http.Response) error {
	ra := time.Duration(0)
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
			ra = time.Duration(secs) * time.Second
		}
	}
	return &retry.StatusError{Code: resp.StatusCode, RetryAfter: ra}
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
	// 1. Fetch the siblings manifest, retrying transient faults.
	apiURL := fmt.Sprintf("%s/api/models/%s/revision/%s", f.baseURL, f.repo, f.rev)
	var rev revisionResp
	err := retry.Do(ctx, f.Policy, func() error {
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
			return hfStatus(resp)
		}
		if err := json.NewDecoder(resp.Body).Decode(&rev); err != nil {
			return fmt.Errorf("hf: decode revision response: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("hf: revision %s: %w", f.rev, err)
	}

	// 2. Download each file a loader could open, skipping the repo's docs and
	// media (see nonModelFile) — or, when WithFiles restricted this fetcher,
	// only the named files, regardless of what nonModelFile would say about
	// them.
	found := make(map[string]bool, len(f.only))
	for _, s := range rev.Siblings {
		// Path safety is judged BEFORE the content filter, so a hostile
		// manifest entry is REPORTED rather than quietly skipped because it
		// happens to end in .md. fetchFile re-checks; that is deliberate
		// defence in depth, not a redundancy to remove.
		if !filepath.IsLocal(s.Rfilename) {
			return fmt.Errorf("refusing unsafe model file path %q", s.Rfilename)
		}
		if f.only != nil {
			if !f.only[s.Rfilename] {
				continue
			}
			found[s.Rfilename] = true
		} else if nonModelFile(s.Rfilename) {
			continue
		}
		if err := f.fetchFile(ctx, destDir, s.Rfilename); err != nil {
			return err
		}
	}
	if f.only != nil {
		for name := range f.only {
			if !found[name] {
				return fmt.Errorf("hf: %s not found in %s revision %s siblings manifest", name, f.repo, f.rev)
			}
		}
	}
	return nil
}

// nonModelFile reports whether a manifest entry is repository furniture --
// documentation, git metadata or media -- rather than something a loader
// opens. fastino/gliner2-large-v1 ships README.md, .gitattributes and
// image/GitHub.png (4.4 MB), all of which "download every sibling" installed
// into the model dir next to the weights.
//
// It is deliberately a DENYLIST BY SHAPE, not an allowlist of known model
// files. What gliner2 opens is: config.json and encoder_config/config.json
// (from_pretrained), model.safetensors with a pytorch_model.bin fallback, and
// then the whole directory handed to AutoTokenizer, which reads
// tokenizer.json, tokenizer_config.json, special_tokens_map.json,
// added_tokens.json and the sentencepiece spm.model. An allowlist would have
// to anticipate all of that plus whatever a future revision adds, and a
// missing config or tokenizer file is a runtime load failure that no unit test
// here can catch -- so anything whose shape is not provably furniture is kept.
// Extension-less files (LICENSE, CITATION) are therefore kept too: they cost
// kilobytes and the cost of being wrong is a broken model dir.
//
// ".txt" is NOT denied, and that is the load-bearing example: vocab.txt and
// merges.txt are real tokenizer files for WordPiece/BPE models. The obvious
// "skip the docs" extension list would have deleted a tokenizer.
func nonModelFile(rfilename string) bool {
	switch strings.ToLower(filepath.Base(rfilename)) {
	case ".gitattributes", ".gitignore", ".ds_store":
		return true
	}
	switch strings.ToLower(filepath.Ext(rfilename)) {
	case ".md",
		".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp", ".bmp", ".ico":
		return true
	}
	return false
}

// fetchFile downloads a single rfilename from the resolve endpoint into
// destDir/{rfilename}, writing atomically via a temp file.
func (f *HFFetcher) fetchFile(ctx context.Context, destDir, rfilename string) error {
	// Guard against path traversal attacks. Kept outside the retry closure:
	// it's a static judgment about rfilename, not a network fault to retry.
	if !filepath.IsLocal(rfilename) {
		return fmt.Errorf("refusing unsafe model file path %q", rfilename)
	}

	destPath := filepath.Join(destDir, rfilename)
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("hf: mkdirall for %s: %w", rfilename, err)
	}

	url := fmt.Sprintf("%s/%s/resolve/%s/%s", f.baseURL, f.repo, f.rev, rfilename)
	err := retry.Do(ctx, f.Policy, func() error {
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
			return hfStatus(resp)
		}

		// Write to a temp file in the same dir, then rename for atomicity.
		// A failed attempt discards its temp file so a retry starts clean.
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
	})
	if err != nil {
		return fmt.Errorf("hf: fetch %s: %w", rfilename, err)
	}
	return nil
}
