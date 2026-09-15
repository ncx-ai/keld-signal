package update

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestUnattendedPathNeverCallsFetchUnverified pins, at the source level, the
// comment already on FetchUnverified's own doc: "It exists for ONE caller —
// the installer's sidecar fetch, where a human is watching a progress bar
// [...]. Auto-update must never call it: an unattended swap has no reader who
// can abort." Auto-update (Updater.Maybe -> Fetcher.Fetch, in update.go) is
// the one code path in this branch that installs bytes onto a machine with
// nobody watching, and today only that comment stands between it and calling
// the unverified variant by accident — a comment enforces nothing.
//
// This is a source-level assertion rather than a behavioural one because the
// property being pinned is "this identifier is never WRITTEN outside its own
// definition", which a runtime test could only prove by exhaustively covering
// every path through the package. The repo's other guard tests take the same
// shortcut for the same reason (see e.g. publish.forbiddenWireKeys, which
// asserts over a marshalled struct rather than walking every producer).
func TestUnattendedPathNeverCallsFetchUnverified(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	// Matches a use of the identifier as a call — `.FetchUnverified(` or a
	// bare `FetchUnverified(` — but not its own func declaration, which reads
	// `func (f *Fetcher) FetchUnverified(ctx context.Context, ...)`.
	callRe := regexp.MustCompile(`FetchUnverified\(`)
	declRe := regexp.MustCompile(`^func \(f \*Fetcher\) FetchUnverified\(`)

	sawDecl := false
	var violations []string

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", name, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if declRe.MatchString(line) {
				sawDecl = true
				continue
			}
			if callRe.MatchString(line) {
				violations = append(violations, name+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
	}

	// Guard the guard: if FetchUnverified's own definition were ever renamed
	// or removed, every check above would pass vacuously.
	if !sawDecl {
		t.Fatalf("FetchUnverified's own definition was not found in %s — this test would otherwise pass vacuously", dir)
	}
	for _, v := range violations {
		t.Errorf("forbidden call to FetchUnverified outside its own definition: %s\n"+
			"FetchUnverified skips the published-hash check and exists for exactly ONE caller — "+
			"the installer's sidecar fetch (internal/cli/installsidecar.go), where a human is "+
			"watching a progress bar and can abort. Auto-update's unattended swap has no such "+
			"reader and must never call it.", v)
	}
}
