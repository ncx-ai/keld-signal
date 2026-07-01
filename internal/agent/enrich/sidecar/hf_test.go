package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// hfStub returns an httptest.Server that stubs the HF API for a two-file repo.
// files maps rfilename -> body bytes. The revision endpoint returns a siblings
// list; each resolve/{rev}/{filename} endpoint returns the file bytes.
func hfStub(t *testing.T, repo, rev string, files map[string][]byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// GET /api/models/{repo}/revision/{rev}
	mux.HandleFunc(fmt.Sprintf("/api/models/%s/revision/%s", repo, rev),
		func(w http.ResponseWriter, r *http.Request) {
			type sibling struct {
				Rfilename string `json:"rfilename"`
			}
			var siblings []sibling
			for name := range files {
				siblings = append(siblings, sibling{Rfilename: name})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"siblings": siblings})
		})

	// GET /{repo}/resolve/{rev}/{filename}
	for name, body := range files {
		name, body := name, body // capture loop vars
		mux.HandleFunc(fmt.Sprintf("/%s/resolve/%s/%s", repo, rev, name),
			func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(body)
			})
	}

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestHFFetcherDownloadsAllFiles verifies that HFFetcher.Fetch writes every file
// from the siblings list into destDir with the correct contents.
func TestHFFetcherDownloadsAllFiles(t *testing.T) {
	const repo = "fastino/gliner2-large-v1"
	const rev = "b122b11eeaee4dabd32bed80412f3234c0d0e943"

	files := map[string][]byte{
		"config.json":       []byte(`{"model_type":"gliner"}`),
		"model.safetensors": []byte("fake-weight-bytes"),
	}

	srv := hfStub(t, repo, rev, files)

	f := NewHFFetcher(repo, rev)
	f.baseURL = srv.URL // point at stub

	dest := t.TempDir()
	if err := f.Fetch(context.Background(), dest); err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil {
			t.Fatalf("file %q not written: %v", name, err)
		}
		if string(got) != string(want) {
			t.Fatalf("file %q: got %q want %q", name, got, want)
		}
	}
}

// TestHFFetcherAPIErrorPropagates verifies that a non-200 from the revision
// endpoint is returned as an error.
func TestHFFetcherAPIErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	f := NewHFFetcher("owner/repo", "abc123")
	f.baseURL = srv.URL

	dest := t.TempDir()
	err := f.Fetch(context.Background(), dest)
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
}

// TestHFFetcherResolveErrorPropagates verifies that a non-200 from a resolve
// endpoint is returned as an error (the revision endpoint is fine but one file
// returns 500).
func TestHFFetcherResolveErrorPropagates(t *testing.T) {
	const repo = "owner/repo"
	const rev = "abc123"

	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/api/models/%s/revision/%s", repo, rev),
		func(w http.ResponseWriter, r *http.Request) {
			type sibling struct {
				Rfilename string `json:"rfilename"`
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"siblings": []sibling{{Rfilename: "model.safetensors"}},
			})
		})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	f := NewHFFetcher(repo, rev)
	f.baseURL = srv.URL

	dest := t.TempDir()
	err := f.Fetch(context.Background(), dest)
	if err == nil {
		t.Fatal("expected error for 500 resolve, got nil")
	}
}
