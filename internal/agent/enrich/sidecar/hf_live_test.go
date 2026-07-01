//go:build hf_live

package sidecar

import (
	"context"
	"testing"

	"github.com/ncx-ai/keld-cli/internal/agent/provision"
)

// TestHFFetcherLiveDownload performs a real download of the pinned GLiNER2
// model from Hugging Face and verifies the sentinel sha256.
// This test is excluded from normal CI via the hf_live build tag.
// Run manually with: go test -tags hf_live ./internal/agent/enrich/sidecar/ -run TestHFFetcherLiveDownload -v -timeout 60m
func TestHFFetcherLiveDownload(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live download in short mode")
	}

	dir := t.TempDir()
	f := NewHFFetcher(provision.ModelRepo, provision.ModelRevision)
	if err := provision.EnsureModel(
		context.Background(),
		dir,
		provision.ModelSHA256,
		f,
	); err != nil {
		t.Fatalf("EnsureModel live: %v", err)
	}
}
