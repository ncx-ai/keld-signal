package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/features"
	"github.com/ncx-ai/keld-signal/internal/agent/provision"
)

// encoderTestHome isolates ~/.keld so nothing here can see, touch or overwrite
// the real ~/.keld/models/qwen3-embedding-0.6b.
func encoderTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	return home
}

// newTestEncoderProvisioner is newEncoderProvisioner with the network fetcher
// replaced and the failure cooldown compressed, so a test never reaches
// huggingface.co and never waits five real minutes.
func newTestEncoderProvisioner(t *testing.T, gate func() bool, f provision.Fetcher,
	emitter *clientevents.Emitter) *encoderProvisioner {
	t.Helper()
	prev := newEncoderFetcher
	newEncoderFetcher = func() provision.Fetcher { return f }
	t.Cleanup(func() { newEncoderFetcher = prev })

	p := newEncoderProvisioner(t.Context(), features.TextEmbedEnabled(), gate, emitter)
	if p == nil {
		t.Fatal("newEncoderProvisioner returned nil with the toggle on")
	}
	p.cooldown = 20 * time.Millisecond
	return p
}

// waitFor polls until cond or the deadline, so a test asserts on an outcome
// rather than on a sleep long enough to hide a race.
func encWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ⚠️ THE TOGGLE-OFF CASE IS THE EXPENSIVE ONE TO GET WRONG: every machine in
// the fleet has KELD_TEXTEMBED off, and a provisioner that merely declined at
// fetch time would still have to be trusted to decline. It is absent instead.
//
// The "true"/"on"/"yes" rows are the load-bearing ones. textembed.enabled() is
// `== "1"`, so those values are OFF sidecar-side; a Go side generous enough to
// accept them would download 1.2 GB for an encoder that never runs.
func TestEncoderProvisionerIsAbsentUnlessTheToggleIsExactlyOne(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{
		{"", false},
		{"0", false},
		{"off", false},
		{"true", false},
		{"on", false},
		{"yes", false},
		{"1", true},
	} {
		t.Run("KELD_TEXTEMBED="+tc.val, func(t *testing.T) {
			encoderTestHome(t)
			t.Setenv(features.EnvTextEmbed, tc.val)

			var fetches atomic.Int32
			prev := newEncoderFetcher
			newEncoderFetcher = func() provision.Fetcher {
				return fetcherFunc(func(context.Context, string) error {
					fetches.Add(1)
					return nil
				})
			}
			t.Cleanup(func() { newEncoderFetcher = prev })

			on := func() bool { return true }
			p := newEncoderProvisioner(t.Context(), features.TextEmbedEnabled(), on, nil)
			if (p != nil) != tc.want {
				t.Fatalf("provisioner non-nil = %v, want %v", p != nil, tc.want)
			}
			if p == nil {
				// The nil receiver must be safe: features.go returns the
				// unwrapped advance in this case, but demand() is also called
				// on a nil pointer by the guard in demand itself.
				p.demand()
				if p.status() != encoderOff {
					t.Fatalf("status = %q, want %q", p.status(), encoderOff)
				}
			} else {
				p.demand()
				encWaitFor(t, "the one fetch the toggle authorised", func() bool { return fetches.Load() == 1 })
			}
		})
	}
}

// The org's `features` toggle is the second condition, and it is read LIVE:
// an org that has the path switched off must not pay the download, and one that
// switches it on mid-run must get it without a daemon restart.
func TestEncoderDemandRespectsTheLiveFeaturesToggle(t *testing.T) {
	encoderTestHome(t)
	t.Setenv(features.EnvTextEmbed, "1")

	var fetches atomic.Int32
	var on atomic.Bool
	p := newTestEncoderProvisioner(t, on.Load, fetcherFunc(func(_ context.Context, dest string) error {
		fetches.Add(1)
		return os.WriteFile(filepath.Join(dest, "model.safetensors"), []byte("w"), 0o644)
	}), nil)

	for i := 0; i < 5; i++ {
		p.demand()
	}
	time.Sleep(100 * time.Millisecond) // give a wrongly-eager fetch every chance
	if n := fetches.Load(); n != 0 {
		t.Fatalf("fetched %d times with the features toggle off", n)
	}
	if got := p.status(); got != encoderIdle {
		t.Fatalf("status = %q, want %q", got, encoderIdle)
	}

	on.Store(true)
	p.demand()
	encWaitFor(t, "the fetch the org just authorised", func() bool { return fetches.Load() == 1 })
}

// ⚠️ THE NON-BLOCKING CLAIM, at the unit level. ensure() is the only function
// here that can block, and a caller with a short budget must be TOLD "not ready"
// rather than parked: EnsureModel stages into a temp dir it deletes on failure,
// so a cancelled fetch discards everything it had and the next attempt restarts
// from zero. Two callers must also JOIN one download rather than start two.
func TestEncoderEnsureTellsAShortCallerNotReadyInsteadOfWaiting(t *testing.T) {
	encoderTestHome(t)
	t.Setenv(features.EnvTextEmbed, "1")

	started := make(chan struct{}, 4)
	release := make(chan struct{})
	defer close(release)

	on := func() bool { return true }
	p := newTestEncoderProvisioner(t, on, slowFetcher{started: started, release: release}, nil)

	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		begin := time.Now()
		err := p.ensure(ctx)
		took := time.Since(begin)
		cancel()
		if err == nil {
			t.Fatalf("ensure %d returned nil while the weights were still downloading", i)
		}
		if took > time.Second {
			t.Fatalf("ensure %d waited %s — it must return on its caller's budget, not the download's", i, took)
		}
	}
	if n := len(started); n != 1 {
		t.Fatalf("started %d downloads for 2 callers, want 1 joined attempt", n)
	}
}

// Success LATCHES. Verification streams a SHA-256 over ~1.2 GB, so a second
// entry into EnsureModel would re-hash the weights.
//
// The corrupted sentinel is a probe, not a scenario: if a later demand re-enters
// EnsureModel it now sees a SHA mismatch and re-fetches, which is exactly the
// re-verification being ruled out.
func TestEncoderSuccessLatchesAndDoesNotReVerify(t *testing.T) {
	encoderTestHome(t)
	t.Setenv(features.EnvTextEmbed, "1")

	content := []byte("encoder-weights")
	var fetches atomic.Int32
	on := func() bool { return true }
	p := newTestEncoderProvisioner(t, on, fetcherFunc(func(_ context.Context, dest string) error {
		fetches.Add(1)
		return os.WriteFile(filepath.Join(dest, "model.safetensors"), content, 0o644)
	}), nil)
	p.sha = sha256Hex(content)

	p.demand()
	encWaitFor(t, "the first fetch to land", func() bool { return p.status() == encoderReady })

	if err := os.WriteFile(filepath.Join(p.dir, "model.safetensors"), []byte("corrupted"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		p.demand()
	}
	if err := p.ensure(t.Context()); err != nil {
		t.Fatalf("ensure after a latched success: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	if n := fetches.Load(); n != 1 {
		t.Fatalf("fetched %d times; success must latch", n)
	}
}

// A failure does NOT latch — but it does not re-arm instantly either. The demand
// signal is the watcher's poll loop (every 5s per advancing transcript), so an
// uncooled retry would re-attempt a 1.2 GB download every five seconds.
func TestEncoderFailureRetriesButOnlyAfterTheCooldown(t *testing.T) {
	encoderTestHome(t)
	t.Setenv(features.EnvTextEmbed, "1")

	var fetches atomic.Int32
	on := func() bool { return true }
	p := newTestEncoderProvisioner(t, on, fetcherFunc(func(context.Context, string) error {
		fetches.Add(1)
		return errors.New("dial tcp: connection refused")
	}), nil)
	p.cooldown = 250 * time.Millisecond

	p.demand()
	encWaitFor(t, "the first attempt to fail", func() bool { return p.status() == encoderFailed })

	// Inside the cooldown: the poll loop hammers demand and nothing refetches.
	for i := 0; i < 50; i++ {
		p.demand()
	}
	if n := fetches.Load(); n != 1 {
		t.Fatalf("fetched %d times inside the cooldown, want 1", n)
	}
	// ensure() must report the failure rather than park until its ctx expires:
	// "the fetch is failing" and "the fetch has not finished" are different.
	if err := p.ensure(t.Context()); err == nil {
		t.Fatal("ensure returned nil after a failed attempt")
	}

	encWaitFor(t, "the cooldown to re-arm the retry", func() bool {
		p.demand()
		return fetches.Load() >= 2
	})
}

// The three transitions are the whole observability surface for a 1.2 GB fetch,
// so all three must survive the default severity floor ("warn") — otherwise
// "ready" is dropped and an operator cannot tell it from "never started", which
// is the exact confusion encoderState exists to remove.
func TestEncoderProvisioningTransitionsAreReported(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fetch     func(dest string) error
		wantCodes []string
		wantSev   map[string]clientevents.Severity
	}{
		{
			name: "success",
			fetch: func(dest string) error {
				return os.WriteFile(filepath.Join(dest, "model.safetensors"), []byte("w"), 0o644)
			},
			wantCodes: []string{"features.encoder_provisioning", "features.encoder_provisioned"},
			wantSev: map[string]clientevents.Severity{
				"features.encoder_provisioning": clientevents.SevWarn,
				"features.encoder_provisioned":  clientevents.SevInfo,
			},
		},
		{
			name:      "failure",
			fetch:     func(string) error { return errors.New("dial tcp 1.2.3.4:443: i/o timeout") },
			wantCodes: []string{"features.encoder_provisioning", "features.encoder_provision_failed"},
			wantSev: map[string]clientevents.Severity{
				"features.encoder_provision_failed": clientevents.SevError,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoderTestHome(t)
			t.Setenv(features.EnvTextEmbed, "1")

			em := clientevents.NewEmitter(clientevents.Corr{}, 16)
			// The delivered default floor, so the test proves these survive it.
			em.SetGate(clientevents.Gate{Enabled: true, MinSeverity: clientevents.SevWarn, SampleRate: 1})

			content := []byte("w")
			on := func() bool { return true }
			p := newTestEncoderProvisioner(t, on,
				fetcherFunc(func(_ context.Context, dest string) error { return tc.fetch(dest) }), em)
			p.sha = sha256Hex(content)

			p.demand()
			want := encoderReady
			if tc.name == "failure" {
				want = encoderFailed
			}
			encWaitFor(t, "the attempt to settle", func() bool { return p.status() == want })

			seen := map[string]clientevents.Event{}
			for _, e := range em.Drain() {
				seen[e.Code] = e
			}
			for _, code := range tc.wantCodes {
				e, ok := seen[code]
				if !ok {
					t.Fatalf("%s was not emitted; got %v", code, seen)
				}
				if sev, want := tc.wantSev[code], e.Severity; sev != "" && sev != want {
					t.Errorf("%s severity = %v, want %v", code, want, sev)
				}
			}
			if e, ok := seen["features.encoder_provision_failed"]; ok {
				if _, ok := e.Fields["error"]; !ok {
					t.Errorf("failure event must carry a redacted error, got %+v", e.Fields)
				}
			}
		})
	}
}

// ⚠️ KELD_TEXTEMBED_DIR IS A CLAIM, NOT A HINT. Set while the weights are
// absent it tells any operator reading the child's environment that they are
// installed, and buys nothing: the sidecar reads an absent explicit dir as None,
// the same answer it gives with the variable unset — and unset resolves to the
// same default path, so a fetch that lands later is adopted either way.
func TestSidecarEnvSetsTheEncoderDirOnlyWhenTheWeightsArePresent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		toggle  string
		install bool
		wantSet bool
	}{
		{"toggle off, no weights", "", false, false},
		{"toggle off, weights present", "", true, false},
		{"toggle on, no weights yet", "1", false, false},
		{"toggle on, weights installed", "1", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := encoderTestHome(t)
			t.Setenv(features.EnvTextEmbed, tc.toggle)

			dir := filepath.Join(home, "models", provision.EncoderDirName)
			if tc.install {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "model.safetensors"), []byte("w"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			needed := features.TextEmbedEnabled()
			env := sidecarEnv([]string{"PATH=/bin"}, "/models/gliner2", encoderDirForSpawn(needed), nil, needed)
			got := hasEnvKey(env, "KELD_TEXTEMBED_DIR")
			if got != tc.wantSet {
				t.Fatalf("KELD_TEXTEMBED_DIR set = %v, want %v (env %v)", got, tc.wantSet, env)
			}
			if tc.wantSet && envMap(env)["KELD_TEXTEMBED_DIR"] != dir {
				t.Fatalf("KELD_TEXTEMBED_DIR = %q, want %q",
					envMap(env)["KELD_TEXTEMBED_DIR"], dir)
			}
		})
	}
}

// An EMPTY directory is not installed weights. This is the exact shape the
// runtime check runs against, and the one the sidecar must read as "no weights
// yet" rather than as a broken installation.
func TestEncoderWeightsPresenceIsTheSentinelNotTheDirectory(t *testing.T) {
	dir := t.TempDir()
	if encoderWeightsPresent(dir) {
		t.Fatal("an empty directory read as installed weights")
	}
	if err := os.WriteFile(filepath.Join(dir, "model.safetensors"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if encoderWeightsPresent(dir) {
		t.Fatal("a zero-length sentinel read as installed weights")
	}
	if err := os.WriteFile(filepath.Join(dir, "model.safetensors"), []byte("w"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !encoderWeightsPresent(dir) {
		t.Fatal("a real sentinel did not read as installed weights")
	}
	if encoderWeightsPresent("") {
		t.Fatal("an empty path read as installed weights")
	}
}

// The daemon and the sidecar must agree on the directory with no variable
// passed, because that agreement is what lets a sidecar spawned BEFORE the
// fetch finished adopt the weights with no respawn.
func TestEncoderDirMatchesTheSidecarDefault(t *testing.T) {
	home := encoderTestHome(t)
	want := filepath.Join(home, "models", "qwen3-embedding-0.6b")
	if got := encoderModelDir(); got != want {
		t.Fatalf("encoderModelDir = %q, want %q (textembed.weights_dir()'s default)", got, want)
	}
}

// The wiring: the advance hook the watcher's poll loop calls is what owes the
// download, and it must be wrapped only when the toggle is on.
func TestFeatureAdvanceTriggersTheEncoderFetchOnlyWhenTheToggleIsOn(t *testing.T) {
	for _, tc := range []struct {
		toggle    string
		wantFetch bool
	}{
		{"", false},
		{"1", true},
	} {
		t.Run("KELD_TEXTEMBED="+tc.toggle, func(t *testing.T) {
			encoderTestHome(t)
			t.Setenv(features.EnvTextEmbed, tc.toggle)

			var fetches atomic.Int32
			prev := newEncoderFetcher
			newEncoderFetcher = func() provision.Fetcher {
				return fetcherFunc(func(_ context.Context, dest string) error {
					fetches.Add(1)
					return os.WriteFile(filepath.Join(dest, "model.safetensors"), []byte("w"), 0o644)
				})
			}
			t.Cleanup(func() { newEncoderFetcher = prev })

			on := func() bool { return true }
			enc := newEncoderProvisioner(t.Context(), features.TextEmbedEnabled(), on, nil)
			adv := startFeatureEmitter(t.Context(), fakeFeatureClient{},
				"https://x/v1/enrichments", func() string { return "tok" },
				"actor", "inst", on, on, nil, enc)
			if adv == nil {
				t.Fatal("no advance observer")
			}
			adv("claude_code", filepath.Join(t.TempDir(), "session.jsonl"))

			if tc.wantFetch {
				encWaitFor(t, "the fetch the advance hook owes", func() bool { return fetches.Load() == 1 })
				return
			}
			time.Sleep(100 * time.Millisecond)
			if n := fetches.Load(); n != 0 {
				t.Fatalf("fetched %d times with KELD_TEXTEMBED off", n)
			}
		})
	}
}

// fetcherFunc adapts a function to provision.Fetcher.
type fetcherFunc func(ctx context.Context, dest string) error

func (f fetcherFunc) Fetch(ctx context.Context, dest string) error { return f(ctx, dest) }
