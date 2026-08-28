package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/retry"
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
	f.Policy = fastPolicy() // 500 is transient; keep the test fast

	dest := t.TempDir()
	err := f.Fetch(context.Background(), dest)
	if err == nil {
		t.Fatal("expected error for 500 resolve, got nil")
	}
}

// TestHFFetcherRejectsPathTraversal verifies that Fetch rejects malicious
// rfilename values like "../evil.txt" that attempt to write outside destDir.
func TestHFFetcherRejectsPathTraversal(t *testing.T) {
	const repo = "owner/repo"
	const rev = "abc123"

	// Stub returns a siblings list containing a path-traversal attempt.
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/api/models/%s/revision/%s", repo, rev),
		func(w http.ResponseWriter, r *http.Request) {
			type sibling struct {
				Rfilename string `json:"rfilename"`
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"siblings": []sibling{
					{Rfilename: "safe.txt"},
					{Rfilename: "../evil.txt"},
				},
			})
		})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("malicious content"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	f := NewHFFetcher(repo, rev)
	f.baseURL = srv.URL

	dest := t.TempDir()
	parent := filepath.Dir(dest)

	// Fetch should return an error.
	err := f.Fetch(context.Background(), dest)
	if err == nil {
		t.Fatal("expected error for path-traversal filename, got nil")
	}

	// Ensure no evil.txt was written to the parent directory.
	evilPath := filepath.Join(parent, "evil.txt")
	if _, err := os.Stat(evilPath); err == nil {
		t.Fatalf("evil.txt was written outside destDir at %s", evilPath)
	}
}

func fastPolicy() retry.Policy {
	return retry.Policy{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond, Multiplier: 2}
}

// TestHFFetcherRetriesTransient verifies that Fetch retries a transient 503 from
// the revision endpoint and eventually succeeds once the upstream recovers.
func TestHFFetcherRetriesTransient(t *testing.T) {
	var hits int32
	// Fail the revision endpoint twice with 503, then serve normally.
	real := hfStub(t, "owner/repo", "rev1", map[string][]byte{"config.json": []byte("{}")})
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/revision/") && atomic.AddInt32(&hits, 1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		http.Redirect(w, r, real.URL+r.URL.Path, http.StatusTemporaryRedirect)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	f := NewHFFetcher("owner/repo", "rev1")
	f.baseURL = srv.URL
	f.Policy = fastPolicy()
	if err := f.Fetch(context.Background(), t.TempDir()); err != nil {
		t.Fatalf("Fetch after transient 503s: %v", err)
	}
	if hits < 3 {
		t.Fatalf("expected retries, hits=%d", hits)
	}
}

// TestHFFetcherPermanentFailsFast verifies that a 404 from the revision endpoint
// is not retried at all.
func TestHFFetcherPermanentFailsFast(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	f := NewHFFetcher("owner/repo", "rev1")
	f.baseURL = srv.URL
	f.Policy = fastPolicy()
	if err := f.Fetch(context.Background(), t.TempDir()); err == nil {
		t.Fatal("want error on 404")
	}
	if hits != 1 {
		t.Fatalf("404 must not retry, hits=%d", hits)
	}
}

// recordingHFStub is hfStub plus a record of every resolve path requested: the
// filter's contract is that a non-model file is never REQUESTED, not merely
// deleted after the fact.
func recordingHFStub(t *testing.T, repo, rev string, files map[string][]byte, requested *[]string, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
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
	prefix := fmt.Sprintf("/%s/resolve/%s/", repo, rev)
	mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, prefix)
		mu.Lock()
		*requested = append(*requested, name)
		mu.Unlock()
		body, ok := files[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestHFFetcherSkipsDocsAndMediaButKeepsEverythingALoaderOpens pins the
// manifest filter. A HF repo snapshot carries files no loader ever opens --
// fastino/gliner2-large-v1 ships README.md (8 KB), .gitattributes (1.5 KB) and
// image/GitHub.png (4.4 MB) -- so fetching "every sibling" downloads and
// installs documentation and a screenshot alongside the weights.
//
// The filter is a DENYLIST by shape (docs, git metadata, images), never an
// allowlist: gliner2's from_pretrained opens config.json,
// encoder_config/config.json and model.safetensors (falling back to
// pytorch_model.bin), and hands the whole dir to AutoTokenizer, which reads
// tokenizer.json / tokenizer_config.json / special_tokens_map.json /
// added_tokens.json / spm.model. An allowlist would silently break the day the
// repo adds a file it did not anticipate -- and a missing tokenizer or config
// is a runtime failure no unit test here would catch.
func TestHFFetcherSkipsDocsAndMediaButKeepsEverythingALoaderOpens(t *testing.T) {
	const repo = "fastino/gliner2-large-v1"
	const rev = "b122b11eeaee4dabd32bed80412f3234c0d0e943"

	// Exactly the real repo's file list.
	keep := []string{
		"config.json", "encoder_config/config.json", "model.safetensors",
		"tokenizer.json", "tokenizer_config.json", "special_tokens_map.json",
		"added_tokens.json", "spm.model",
	}
	skip := []string{"README.md", ".gitattributes", "image/GitHub.png"}

	files := map[string][]byte{}
	for _, n := range append(append([]string{}, keep...), skip...) {
		files[n] = []byte("body-of-" + n)
	}

	var mu sync.Mutex
	var requested []string
	srv := recordingHFStub(t, repo, rev, files, &requested, &mu)

	f := NewHFFetcher(repo, rev)
	f.baseURL = srv.URL

	dest := t.TempDir()
	if err := f.Fetch(context.Background(), dest); err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	for _, n := range keep {
		if _, err := os.Stat(filepath.Join(dest, n)); err != nil {
			t.Fatalf("%s is opened by the loader and must still be fetched: %v", n, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	asked := map[string]bool{}
	for _, n := range requested {
		asked[n] = true
	}
	for _, n := range skip {
		if asked[n] {
			t.Fatalf("%s is not a model file and must never be requested", n)
		}
		if _, err := os.Stat(filepath.Join(dest, n)); err == nil {
			t.Fatalf("%s must not be installed into the model dir", n)
		}
	}
}

// THE TEXT ENCODER'S SNAPSHOT — the second repo this fetcher installs
// (provision.EncoderRepo, fetched by daemon/encoder_on_demand.go).
//
// It exists because nonModelFile's own warning applies here with a new file
// list: the denylist judges SHAPE, so a file the loader opens and the denylist
// happens to reject is a runtime load failure at from_pretrained time, which no
// unit test upstream of this one catches. The list below is the real revision's
// siblings manifest verbatim (twelve entries, verified against
// huggingface.co/api/models/Qwen/Qwen3-Embedding-0.6B/revision/97b0c614...),
// and the keep/skip split is the one the known-good local weights directory
// shows.
//
// merges.txt is the load-bearing entry: this is a BPE tokenizer, so the obvious
// "skip the docs" extension list — which would have included .txt — deletes a
// tokenizer file and breaks AutoTokenizer.
func TestHFFetcherInstallsEverythingTheTextEncoderLoaderOpens(t *testing.T) {
	const repo = "Qwen/Qwen3-Embedding-0.6B"
	const rev = "97b0c614be4d77ee51c0cef4e5f07c00f9eb65b3"

	keep := []string{
		"config.json", "model.safetensors", "generation_config.json",
		"tokenizer.json", "tokenizer_config.json", "vocab.json", "merges.txt",
		"modules.json", "config_sentence_transformers.json", "1_Pooling/config.json",
	}
	skip := []string{"README.md", ".gitattributes"}

	files := map[string][]byte{}
	for _, n := range append(append([]string{}, keep...), skip...) {
		files[n] = []byte("body-of-" + n)
	}

	var mu sync.Mutex
	var requested []string
	srv := recordingHFStub(t, repo, rev, files, &requested, &mu)

	f := NewHFFetcher(repo, rev)
	f.baseURL = srv.URL

	dest := t.TempDir()
	if err := f.Fetch(context.Background(), dest); err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	for _, n := range keep {
		if _, err := os.Stat(filepath.Join(dest, n)); err != nil {
			t.Fatalf("%s is part of the encoder snapshot and must be fetched: %v", n, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	asked := map[string]bool{}
	for _, n := range requested {
		asked[n] = true
	}
	for _, n := range skip {
		if asked[n] {
			t.Errorf("%s is repository furniture and must never be requested", n)
		}
	}
}

// TestHFFetcherRetryClassificationHoldsForBothRepos pins that the encoder's
// fetch inherits internal/retry's classifier rather than a hand-rolled loop of
// its own: a transient fault is retried, an unknown/permanent one is not
// (internal/retry's stated default — never hammer). Table-driven over both
// pinned repos because the encoder was added second and the property must be
// true of it, not merely true of the fetcher in general.
func TestHFFetcherRetryClassificationHoldsForBothRepos(t *testing.T) {
	for _, repo := range []string{
		"fastino/gliner2-large-v1",
		"Qwen/Qwen3-Embedding-0.6B",
	} {
		for _, tc := range []struct {
			name        string
			status      int
			wantMinHits int32
			wantMaxHits int32
		}{
			{"transient 503 retries", http.StatusServiceUnavailable, 2, 1 << 20},
			{"permanent 404 does not", http.StatusNotFound, 1, 1},
			{"permanent 403 does not", http.StatusForbidden, 1, 1},
		} {
			t.Run(repo+"/"+tc.name, func(t *testing.T) {
				var hits int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					atomic.AddInt32(&hits, 1)
					w.WriteHeader(tc.status)
				}))
				t.Cleanup(srv.Close)

				f := NewHFFetcher(repo, "rev1")
				f.baseURL = srv.URL
				f.Policy = fastPolicy()
				if err := f.Fetch(context.Background(), t.TempDir()); err == nil {
					t.Fatalf("want an error for status %d", tc.status)
				}
				if got := atomic.LoadInt32(&hits); got < tc.wantMinHits || got > tc.wantMaxHits {
					t.Fatalf("hits = %d, want in [%d,%d]", got, tc.wantMinHits, tc.wantMaxHits)
				}
			})
		}
	}
}
