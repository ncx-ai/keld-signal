package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/ncx-ai/keld-signal/internal/retry"
)

// ErrNoPublishedHash identifies Fetch's refusal to install an asset with no
// published SHA-256, across the package boundary. Callers that need to
// distinguish this refusal from any other Fetch error (the installer's
// warn-and-continue policy is the one caller that does) must match on this
// sentinel with errors.Is, never on a substring of Fetch's error text — a
// future reword of that text must not silently flip a warning into a refusal,
// or the reverse.
var ErrNoPublishedHash = errors.New("update: no published SHA-256 for the release asset")

// DefaultMirrorURL is the root of the public release mirror (R2 behind
// dl.keld.co), the host scripts/install.sh fetches from too.
const DefaultMirrorURL = "https://dl.keld.co"

// channel is the mirror's top-level prefix for a tag: a '-' marks a pre-release,
// the rule publish-releases.yml writes by and every installer reads by.
func channel(tag string) string {
	if strings.Contains(tag, "-") {
		return "prereleases"
	}
	return "releases"
}

// AssetNames returns the release asset names for an OS/arch: the archive
// holding keld + keld-agent, and the frozen analysis sidecar's tarball. These
// are the names .goreleaser.yaml and the installers workflow already publish;
// Windows takes a zip (format_overrides in .goreleaser.yaml).
func AssetNames(goos, goarch string) (cli, sidecar string) {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("keld_%s_%s%s", goos, goarch, ext),
		fmt.Sprintf("keld-agent-sidecar_%s_%s.tar.gz", goos, goarch)
}

// Fetcher downloads and verifies one release asset.
type Fetcher struct {
	HTTP    *http.Client
	BaseURL string
	Policy  retry.Policy

	// Progress, when non-nil, is called as bytes land. total is -1 when the
	// server sent no Content-Length. It exists for the installer's wizard pane,
	// which needs a determinate bar; the unattended update path leaves it nil.
	//
	// It is called from the download goroutine, synchronously, so a slow
	// callback slows the download — keep it to a channel send or an atomic store.
	Progress func(received, total int64)
}

func (f *Fetcher) policy() retry.Policy {
	if f.Policy.MaxAttempts > 0 {
		return f.Policy
	}
	return retry.DefaultPolicy()
}

func (f *Fetcher) client() *http.Client {
	if f.HTTP != nil {
		return f.HTTP
	}
	return &http.Client{Timeout: 30 * time.Minute}
}

// releaseDir is where a tag's assets live. An explicit BaseURL (the
// `install-sidecar --base-url` flag, and tests) is used as-is, <base>/<tag>.
func (f *Fetcher) releaseDir(tag string) string {
	if f.BaseURL != "" {
		return strings.TrimRight(f.BaseURL, "/") + "/" + tag
	}
	return DefaultMirrorURL + "/" + channel(tag) + "/" + tag
}

// fastPolicy is used by tests: the real backoff would make the retry cases
// take seconds each for no added coverage.
func fastPolicy() retry.Policy {
	return retry.Policy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond, Multiplier: 2}
}

// progressReader counts bytes as they are read and reports them.
type progressReader struct {
	r        io.Reader
	total    int64
	received int64
	report   func(received, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.received += int64(n)
		if p.report != nil {
			p.report(p.received, p.total)
		}
	}
	return n, err
}

// Fetch downloads the asset to dest — <mirror>/<channel>/<tag>/<asset> by
// default, or <BaseURL>/<tag>/<asset> when BaseURL is set — and verifies its
// SHA-256 against the release's published hash.
//
// NO PUBLISHED HASH IS FATAL. scripts/install.sh warns and continues in that
// case, deliberately, because a human is reading its output and can abort. An
// unattended swap has no such reader — so the single case where the installer
// degrades gracefully is the case this must refuse outright.
func (f *Fetcher) Fetch(ctx context.Context, tag, asset, dest string) error {
	want, err := f.publishedSHA(ctx, tag, asset)
	if err != nil {
		return err
	}
	if want == "" {
		return fmt.Errorf("update: no published SHA-256 for %s in release %s; refusing to install an unverified asset: %w", asset, tag, ErrNoPublishedHash)
	}
	url := f.releaseDir(tag) + "/" + asset
	sum, err := f.download(ctx, url, dest)
	if err != nil {
		_ = os.Remove(dest)
		return err
	}
	if !strings.EqualFold(sum, want) {
		_ = os.Remove(dest)
		return fmt.Errorf("update: checksum mismatch for %s: expected %s, got %s (the download is corrupt or truncated)", asset, want, sum)
	}
	return nil
}

// FetchUnverified downloads without a published-hash check. It exists for ONE
// caller — the installer's sidecar fetch, where a human is watching and
// scripts/install.sh has always degraded this way. Auto-update must never call
// it: an unattended swap has no reader who can abort.
func (f *Fetcher) FetchUnverified(ctx context.Context, tag, asset, dest string) error {
	url := f.releaseDir(tag) + "/" + asset
	if _, err := f.download(ctx, url, dest); err != nil {
		_ = os.Remove(dest)
		return err
	}
	return nil
}

// download writes url to dest and returns the hex SHA-256 of what it wrote.
// The hash is computed from the bytes as they land, so a body that disagrees
// with its Content-Length cannot pass by being re-read from a cache.
func (f *Fetcher) download(ctx context.Context, url, dest string) (string, error) {
	var sum string
	err := retry.Do(ctx, f.policy(), func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := f.client().Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return retry.HTTPStatus(resp.StatusCode)
		}
		out, err := os.Create(dest)
		if err != nil {
			return err
		}
		h := sha256.New()
		var src io.Reader = resp.Body
		if f.Progress != nil {
			// Constructed per ATTEMPT, inside the retry closure: a retried
			// download restarts at zero bytes, and a counter that survived the
			// retry would report a bar running past 100%.
			src = &progressReader{r: resp.Body, total: resp.ContentLength, report: f.Progress}
		}
		n, err := io.Copy(io.MultiWriter(out, h), src)
		cerr := out.Close()
		if err != nil {
			return err
		}
		if cerr != nil {
			return cerr
		}
		// A body shorter than the declared length is the truncated-transfer
		// case; the hash below catches it, but saying so here keeps the error
		// legible when it is the cause.
		if resp.ContentLength >= 0 && n != resp.ContentLength {
			return fmt.Errorf("update: short read: %d of %d bytes", n, resp.ContentLength)
		}
		sum = hex.EncodeToString(h.Sum(nil))
		return nil
	})
	return sum, err
}

// publishedSHA resolves the expected hash: checksums.txt first (goreleaser
// publishes it for the keld archives), then a per-file <asset>.sha256, which
// is what CI publishes for the separately-built sidecar. An empty return means
// the release published no hash for this asset at all.
func (f *Fetcher) publishedSHA(ctx context.Context, tag, asset string) (string, error) {
	if body, ok, err := f.get(ctx, f.releaseDir(tag)+"/checksums.txt"); err != nil {
		return "", err
	} else if ok {
		if v := ParseChecksums(strings.NewReader(string(body)))[asset]; v != "" {
			return v, nil
		}
	}
	body, ok, err := f.get(ctx, f.releaseDir(tag)+"/"+asset+".sha256")
	if err != nil || !ok {
		return "", err
	}
	fields := strings.Fields(string(body))
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

// get fetches a small body. A 404 is "not published" (ok=false, nil error),
// not a failure: the two hash sources are tried in order and the first absence
// is expected.
func (f *Fetcher) get(ctx context.Context, url string) ([]byte, bool, error) {
	var body []byte
	var missing bool
	err := retry.Do(ctx, f.policy(), func() error {
		missing = false
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := f.client().Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			missing = true
			return nil
		}
		if resp.StatusCode/100 != 2 {
			return retry.HTTPStatus(resp.StatusCode)
		}
		body, err = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return body, !missing, nil
}

// ParseChecksums reads a `<hex>  <name>` listing, ignoring junk lines and
// stripping the `*` binary-mode prefix some tools emit.
func ParseChecksums(r io.Reader) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || !isHexDigest(fields[0]) {
			continue
		}
		out[strings.TrimPrefix(fields[len(fields)-1], "*")] = fields[0]
	}
	return out
}

// HostOSArch is the OS/arch this binary was built for — the release assets it
// may install. Split out so tests can name a platform they are not running on.
func HostOSArch() (string, string) { return runtime.GOOS, runtime.GOARCH }

// isHexDigest reports whether s looks like a hash rather than prose. Without
// it a comment or a stray sentence in checksums.txt parses as
// <first-word> <last-word> and lands in the map as a bogus expected hash for a
// bogus filename — harmless until the day one of those filenames collides with
// a real asset and the fetch is verified against a word.
func isHexDigest(s string) bool {
	if len(s) < 8 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
