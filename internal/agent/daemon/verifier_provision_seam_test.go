package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
	"github.com/ncx-ai/keld-signal/internal/agent/provision"
	"github.com/ncx-ai/keld-signal/internal/retry"
)

// C1 — THE SEAM BETWEEN THE FETCHER'S NAMING AND THE SENTINEL CHECK.
//
// ⚠️ THE POINT OF THIS FILE IS THAT NEITHER HALF IS FAKED. provision/gguf_test.go
// drives EnsureFile with a fetcher that writes a file already called
// "model.gguf", and sidecar/hf_test.go pins the fetcher against the real Hugging
// Face filename. Both passed. Between them the shipped path fetched
// `gemma-4-E2B-it-Q4_K_M.gguf` and then SHA-checked `model.gguf`, so EnsureFile
// errored "fetched model missing model.gguf", `defer os.RemoveAll(tmp)` threw a
// complete ~3 GB download away, the 5-minute cooldown armed, and the next
// published block started it again — the GGUF could never be provisioned and
// the machine re-downloaded it forever.
//
// So the fixture here resembles production rather than assuming the answer:
// the fetcher is the one newVerifierFetcher builds (real repo, real revision,
// real remote filename, real rename), the manifest names the real remote file,
// and the verification is the real provision.EnsureFile against the real
// provision.VerifierSentinel. Only the origin and the file's BYTES are stubbed.

// hfStubServer serves the two Hugging Face endpoints HFFetcher uses for one
// repo/revision: the siblings manifest, and the resolve URL for each file.
// bodies is keyed by the REMOTE rfilename.
func hfStubServer(t *testing.T, repo, rev string, bodies map[string][]byte, served *int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/api/models/%s/revision/%s", repo, rev),
		func(w http.ResponseWriter, _ *http.Request) {
			type sib struct {
				Rfilename string `json:"rfilename"`
			}
			var sibs []sib
			for name := range bodies {
				sibs = append(sibs, sib{Rfilename: name})
			}
			// Repository furniture the real repo also ships, so the test proves
			// the restriction to one file as well as the rename.
			sibs = append(sibs, sib{Rfilename: "README.md"}, sib{Rfilename: ".gitattributes"})
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"siblings": sibs})
		})
	for name, body := range bodies {
		name, body := name, body
		mux.HandleFunc(fmt.Sprintf("/%s/resolve/%s/%s", repo, rev, name),
			func(w http.ResponseWriter, _ *http.Request) {
				if served != nil {
					*served++
				}
				_, _ = w.Write(body)
			})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// stubbedVerifierFetcher is newVerifierFetcher()'s own result, repointed at a
// local origin and given a fast retry policy. Everything that decides the
// FILENAMES — repo, revision, remote file, local rename — is production's.
func stubbedVerifierFetcher(t *testing.T, base string) provision.Fetcher {
	t.Helper()
	f, ok := newVerifierFetcher().(*sidecar.HFFetcher)
	if !ok {
		t.Fatalf("newVerifierFetcher no longer returns an *sidecar.HFFetcher (%T); "+
			"this test must be updated to keep driving the PRODUCTION fetcher, not a fake", f)
	}
	f.Policy = retry.Policy{MaxAttempts: 1}
	return f.WithBaseURL(base)
}

func TestTheRealVerifierFetcherLandsTheGGUFWhereTheRealSentinelCheckLooks(t *testing.T) {
	content := []byte("GGUF\x00not-really-three-gigabytes-but-the-names-are-real")
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])

	served := 0
	srv := hfStubServer(t, provision.VerifierRepo, provision.VerifierRevision,
		map[string][]byte{provision.VerifierFile: content}, &served)

	dir := filepath.Join(t.TempDir(), provision.VerifierDirName)
	if err := provision.EnsureFile(context.Background(), dir,
		provision.VerifierSentinel, sha, stubbedVerifierFetcher(t, srv.URL)); err != nil {
		t.Fatalf("EnsureFile over the REAL verifier fetcher failed: %v\n"+
			"this is the C1 defect: the fetcher writes %q and the sentinel check reads %q",
			err, provision.VerifierFile, provision.VerifierSentinel)
	}

	// The sentinel is what verifier.weights_path() opens — assert the file the
	// SIDECAR will look for, not merely that EnsureFile returned nil.
	got, err := os.ReadFile(filepath.Join(dir, provision.VerifierSentinel))
	if err != nil {
		t.Fatalf("the provisioned dir has no %s: %v", provision.VerifierSentinel, err)
	}
	if string(got) != string(content) {
		t.Fatalf("%s holds the wrong bytes", provision.VerifierSentinel)
	}
	// And the remote name must NOT survive beside it: two copies of a 3 GB file
	// is its own defect, and it would also mean the rename had not happened.
	if _, err := os.Stat(filepath.Join(dir, provision.VerifierFile)); !os.IsNotExist(err) {
		t.Fatalf("the remote filename %s is still on disk beside the sentinel", provision.VerifierFile)
	}
	// Only the GGUF: README.md/.gitattributes are in the manifest and must not
	// be fetched.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected only %s in the model dir, got %d entries", provision.VerifierSentinel, len(entries))
	}

	// SUCCESS LATCHES ON DISK: a second EnsureFile must be a no-op, because the
	// sentinel it hashes is now the file that is actually there. Before the fix
	// this is where the ~3 GB re-download came from.
	if err := provision.EnsureFile(context.Background(), dir,
		provision.VerifierSentinel, sha, stubbedVerifierFetcher(t, srv.URL)); err != nil {
		t.Fatalf("second EnsureFile failed: %v", err)
	}
	if served != 1 {
		t.Fatalf("the GGUF was fetched %d times; a provisioned sentinel must never be re-downloaded", served)
	}
}

// TestTheVerifierProvisionerLatchesOnceTheFetchLands drives the same seam
// through verifierProvisioner itself — demand(), the emitter, the cooldown —
// so the fix is proved at the level the daemon actually calls, not only at
// EnsureFile's.
func TestTheVerifierProvisionerLatchesOnceTheFetchLands(t *testing.T) {
	content := []byte("GGUF\x00provisioner-level")
	sum := sha256.Sum256(content)
	srv := hfStubServer(t, provision.VerifierRepo, provision.VerifierRevision,
		map[string][]byte{provision.VerifierFile: content}, nil)

	dir := filepath.Join(t.TempDir(), provision.VerifierDirName)
	p := &verifierProvisioner{
		dir:      dir,
		sha:      hex.EncodeToString(sum[:]),
		fetcher:  stubbedVerifierFetcher(t, srv.URL),
		bg:       context.Background(),
		cooldown: time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.ensure(ctx); err != nil {
		t.Fatalf("verifierProvisioner.ensure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, provision.VerifierSentinel)); err != nil {
		t.Fatalf("the provisioner reported success but %s is absent: %v", provision.VerifierSentinel, err)
	}
	if !p.done {
		t.Fatal("success must latch (p.done) so a later demand() does not re-hash the GGUF")
	}
}

// TestTheRealGGUFProvisionsEndToEnd is the FULL-SIZE proof, opt-in because it
// moves ~3 GB through a loopback HTTP server and hashes it twice.
//
//	KELD_VERIFIER_GGUF_E2E=/path/to/gemma-4-E2B-it-Q4_K_M.gguf go test ./internal/agent/daemon -run E2E -v
//
// It runs the production fetcher and the production SHA constant
// (provision.VerifierSHA256) against a real, verified copy of the real file —
// the one thing a small fixture cannot establish, which is that the pinned
// digest belongs to the bytes this path actually installs.
func TestTheRealGGUFProvisionsEndToEnd(t *testing.T) {
	src := os.Getenv("KELD_VERIFIER_GGUF_E2E")
	if src == "" {
		t.Skip("set KELD_VERIFIER_GGUF_E2E to a real gemma-4-E2B-it-Q4_K_M.gguf to run the full-size proof")
	}
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/api/models/%s/revision/%s", provision.VerifierRepo, provision.VerifierRevision),
		func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"siblings": []map[string]string{
					{"rfilename": provision.VerifierFile},
					{"rfilename": "README.md"},
				},
			})
		})
	mux.HandleFunc(fmt.Sprintf("/%s/resolve/%s/%s", provision.VerifierRepo, provision.VerifierRevision, provision.VerifierFile),
		func(w http.ResponseWriter, r *http.Request) {
			f, err := os.Open(src)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			defer f.Close()
			_, _ = io.Copy(w, f)
		})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), provision.VerifierDirName)
	start := time.Now()
	if err := provision.EnsureFile(context.Background(), dir, provision.VerifierSentinel,
		provision.VerifierSHA256, stubbedVerifierFetcher(t, srv.URL)); err != nil {
		t.Fatalf("full-size EnsureFile failed: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, provision.VerifierSentinel))
	if err != nil {
		t.Fatalf("no %s after a full-size fetch: %v", provision.VerifierSentinel, err)
	}
	t.Logf("provisioned %s (%d bytes) in %s, SHA-256 %s verified",
		provision.VerifierSentinel, fi.Size(), time.Since(start).Round(time.Second), provision.VerifierSHA256)
}
