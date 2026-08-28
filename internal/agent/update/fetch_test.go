package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// releaseServer serves a tiny fake release. Handlers may be overridden per
// test to inject the failures that matter.
type releaseServer struct {
	assets    map[string][]byte
	checksums string // full body; "" ⇒ 404
	perFile   map[string]string
	status    map[string]int // path suffix ⇒ forced status
	hits      map[string]*int32
}

func newReleaseServer() *releaseServer {
	return &releaseServer{
		assets:  map[string][]byte{},
		perFile: map[string]string{},
		status:  map[string]int{},
		hits:    map[string]*int32{},
	}
}

func (r *releaseServer) hit(name string) int32 {
	if c, ok := r.hits[name]; ok {
		return atomic.LoadInt32(c)
	}
	return 0
}

func (r *releaseServer) start(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		name := filepath.Base(req.URL.Path)
		if _, ok := r.hits[name]; !ok {
			var n int32
			r.hits[name] = &n
		}
		atomic.AddInt32(r.hits[name], 1)
		if code, ok := r.status[name]; ok && code != 0 {
			w.WriteHeader(code)
			return
		}
		if name == "checksums.txt" {
			if r.checksums == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			fmt.Fprint(w, r.checksums)
			return
		}
		if strings.HasSuffix(name, ".sha256") {
			if v, ok := r.perFile[strings.TrimSuffix(name, ".sha256")]; ok {
				fmt.Fprintf(w, "%s  %s\n", v, strings.TrimSuffix(name, ".sha256"))
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if b, ok := r.assets[name]; ok {
			w.Write(b)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func fetcher(t *testing.T, base string) *Fetcher {
	return &Fetcher{HTTP: &http.Client{Timeout: 5 * time.Second}, BaseURL: base, Policy: fastPolicy()}
}

func TestFetchHappyPath(t *testing.T) {
	rs := newReleaseServer()
	body := []byte("payload")
	rs.assets["a.tar.gz"] = body
	rs.checksums = fmt.Sprintf("%s  a.tar.gz\n", sha(body))
	f := fetcher(t, rs.start(t))
	dest := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := f.Fetch(context.Background(), "v1", "a.tar.gz", dest); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "payload" {
		t.Fatalf("got %q", got)
	}
}

// A hash that IS published and does NOT match is always fatal, and the
// half-downloaded file must not be left behind for a later step to pick up.
func TestFetchChecksumMismatchIsFatalAndRemovesTheFile(t *testing.T) {
	rs := newReleaseServer()
	rs.assets["a.tar.gz"] = []byte("payload")
	rs.checksums = fmt.Sprintf("%s  a.tar.gz\n", sha([]byte("something else")))
	f := fetcher(t, rs.start(t))
	dest := filepath.Join(t.TempDir(), "a.tar.gz")
	err := f.Fetch(context.Background(), "v1", "a.tar.gz", dest)
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("error should name the problem: %v", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatal("a corrupt download must not be left on disk")
	}
}

// install.sh WARNS when no hash is published and continues, because a human is
// reading its output and can abort. An unattended swap has no such reader, so
// the one case the installer degrades on is the case this must refuse.
func TestFetchRefusesAnAssetWithNoPublishedHash(t *testing.T) {
	rs := newReleaseServer()
	rs.assets["a.tar.gz"] = []byte("payload")
	f := fetcher(t, rs.start(t))
	err := f.Fetch(context.Background(), "v1", "a.tar.gz", filepath.Join(t.TempDir(), "a.tar.gz"))
	if err == nil {
		t.Fatal("an unhashed asset must be refused, not warned about")
	}
	if !strings.Contains(err.Error(), "no published") {
		t.Fatalf("got %v", err)
	}
}

// checksums.txt is absent for the separately-built sidecar; CI publishes a
// per-file <asset>.sha256 instead.
func TestFetchFallsBackToPerFileHash(t *testing.T) {
	rs := newReleaseServer()
	body := []byte("sidecar")
	rs.assets["sc.tar.gz"] = body
	rs.perFile["sc.tar.gz"] = sha(body)
	f := fetcher(t, rs.start(t))
	if err := f.Fetch(context.Background(), "v1", "sc.tar.gz", filepath.Join(t.TempDir(), "sc.tar.gz")); err != nil {
		t.Fatal(err)
	}
}

func TestFetchAssetNotFound(t *testing.T) {
	rs := newReleaseServer()
	rs.checksums = "deadbeef  a.tar.gz\n"
	f := fetcher(t, rs.start(t))
	if err := f.Fetch(context.Background(), "v1", "a.tar.gz", filepath.Join(t.TempDir(), "a")); err == nil {
		t.Fatal("want error")
	}
	// A 404 is permanent: retrying it is a storm against a release that will
	// never have this asset.
	if n := rs.hit("a.tar.gz"); n != 1 {
		t.Fatalf("404 retried %d times; permanent errors must not retry", n)
	}
}

func TestFetchRetriesATransientServerError(t *testing.T) {
	rs := newReleaseServer()
	body := []byte("payload")
	rs.assets["a.tar.gz"] = body
	rs.checksums = fmt.Sprintf("%s  a.tar.gz\n", sha(body))
	rs.status["a.tar.gz"] = http.StatusServiceUnavailable
	base := rs.start(t)
	// Clear the forced status after the first hit.
	go func() {
		for rs.hit("a.tar.gz") < 1 {
			time.Sleep(time.Millisecond)
		}
		rs.status["a.tar.gz"] = 0
	}()
	f := fetcher(t, base)
	if err := f.Fetch(context.Background(), "v1", "a.tar.gz", filepath.Join(t.TempDir(), "a.tar.gz")); err != nil {
		t.Fatalf("a 503 then 200 should succeed: %v", err)
	}
	if rs.hit("a.tar.gz") < 2 {
		t.Fatal("expected a retry")
	}
}

func TestFetchGivesUpOnASustained5xx(t *testing.T) {
	rs := newReleaseServer()
	rs.checksums = "deadbeef  a.tar.gz\n"
	rs.status["a.tar.gz"] = http.StatusBadGateway
	f := fetcher(t, rs.start(t))
	if err := f.Fetch(context.Background(), "v1", "a.tar.gz", filepath.Join(t.TempDir(), "a")); err == nil {
		t.Fatal("want error")
	}
}

func TestFetchHonoursContextCancellation(t *testing.T) {
	rs := newReleaseServer()
	body := []byte("payload")
	rs.assets["a.tar.gz"] = body
	rs.checksums = fmt.Sprintf("%s  a.tar.gz\n", sha(body))
	f := fetcher(t, rs.start(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.Fetch(ctx, "v1", "a.tar.gz", filepath.Join(t.TempDir(), "a")); err == nil {
		t.Fatal("want error on a cancelled context")
	}
}

func TestParseChecksumsIgnoresJunkAndStripsTheStar(t *testing.T) {
	in := "deadbeef  *a.tar.gz\nnot a line\n\ncafebabe  b.zip\nshort\n"
	m := ParseChecksums(strings.NewReader(in))
	if m["a.tar.gz"] != "deadbeef" {
		t.Fatalf("star prefix not stripped: %v", m)
	}
	if m["b.zip"] != "cafebabe" {
		t.Fatalf("got %v", m)
	}
	if len(m) != 2 {
		t.Fatalf("junk lines were not ignored: %v", m)
	}
}

// A truncated transfer is the failure install.sh's checksum was actually
// introduced for. It must not pass verification.
func TestFetchDetectsATruncatedBody(t *testing.T) {
	full := []byte("the whole payload, all of it")
	rs := newReleaseServer()
	rs.assets["a.tar.gz"] = full[:5]
	rs.checksums = fmt.Sprintf("%s  a.tar.gz\n", sha(full))
	f := fetcher(t, rs.start(t))
	if err := f.Fetch(context.Background(), "v1", "a.tar.gz", filepath.Join(t.TempDir(), "a")); err == nil {
		t.Fatal("a truncated body must fail verification")
	}
}

// checksums.txt served but not listing this asset falls through to the
// per-file hash, and refuses if that is absent too.
func TestFetchChecksumsWithoutOurAssetFallsThrough(t *testing.T) {
	rs := newReleaseServer()
	body := []byte("payload")
	rs.assets["a.tar.gz"] = body
	rs.checksums = "deadbeef  other.tar.gz\n"
	f := fetcher(t, rs.start(t))
	if err := f.Fetch(context.Background(), "v1", "a.tar.gz", filepath.Join(t.TempDir(), "a")); err == nil {
		t.Fatal("want refusal")
	}
	rs.perFile["a.tar.gz"] = sha(body)
	if err := f.Fetch(context.Background(), "v1", "a.tar.gz", filepath.Join(t.TempDir(), "a")); err != nil {
		t.Fatalf("per-file hash should rescue it: %v", err)
	}
}

func TestAssetNamesMatchWhatTheReleasePublishes(t *testing.T) {
	cli, sc := AssetNames("linux", "amd64")
	if cli != "keld_linux_amd64.tar.gz" || sc != "keld-agent-sidecar_linux_amd64.tar.gz" {
		t.Fatalf("got %q %q", cli, sc)
	}
	if w, _ := AssetNames("windows", "amd64"); w != "keld_windows_amd64.zip" {
		t.Fatalf("windows ships a zip, got %q", w)
	}
}
