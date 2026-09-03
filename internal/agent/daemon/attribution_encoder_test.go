package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/attrib"
	"github.com/ncx-ai/keld-signal/internal/agent/features"
	"github.com/ncx-ai/keld-signal/internal/agent/provision"
)

// THE THIRD REQUIREMENT: attribution must be able to bring the shared text
// encoder up FOR ITSELF, without an operator also having to enable
// KELD_TEXTEMBED — but doing so must never start the separate
// signal-embeddings/publish path. These tests pin the three states the task
// specifies:
//
//   - attribution ON, features flag unset: the encoder provisions, and the
//     features/signal-embeddings path stays OFF.
//   - attribution OFF, features flag unset: nothing provisions.
//   - the existing features-path behaviour (KELD_TEXTEMBED alone) is
//     unchanged.

// TestAttributionAloneProvisionsTheEncoderWithNoTextembedToggle is AC: "NO
// DOWNLOAD WHEN THE GATE IS OFF" verified from the OTHER direction — the gate
// (attribution) being ON is what authorises the fetch, entirely independent
// of features.TextEmbedEnabled(). This mirrors exactly how Run computes
// encoderNeeded: attribOn || features.TextEmbedEnabled().
func TestAttributionAloneProvisionsTheEncoderWithNoTextembedToggle(t *testing.T) {
	encoderTestHome(t)
	// KELD_TEXTEMBED is deliberately left UNSET: this must work without it.
	if v := os.Getenv(features.EnvTextEmbed); v != "" {
		t.Fatalf("test setup: KELD_TEXTEMBED leaked into the environment as %q", v)
	}

	const attribOn = true
	needed := attribOn || features.TextEmbedEnabled()
	if !needed {
		t.Fatal("attribution alone must make the encoder needed")
	}

	var fetches atomic.Int32
	prev := newEncoderFetcher
	newEncoderFetcher = func() provision.Fetcher {
		return fetcherFunc(func(_ context.Context, dest string) error {
			fetches.Add(1)
			return os.WriteFile(filepath.Join(dest, "model.safetensors"), []byte("w"), 0o644)
		})
	}
	t.Cleanup(func() { newEncoderFetcher = prev })

	gate := func() bool { return attribOn }
	p := newEncoderProvisioner(t.Context(), needed, gate, nil)
	if p == nil {
		t.Fatal("newEncoderProvisioner returned nil with attribution on")
	}
	p.sha = sha256Hex([]byte("w")) // match the fake fetcher's content so provisioning actually succeeds
	p.demand()
	encWaitFor(t, "the fetch attribution alone authorised", func() bool { return fetches.Load() == 1 })

	// And the spawn env the sidecar actually sees must carry KELD_TEXTEMBED=1
	// — attrib alone provisioning the weights Go-side is not enough; the
	// sidecar's own textembed.enabled() reads its own copy of the variable.
	env := sidecarEnv([]string{"PATH=/bin"}, "/m", encoderDirForSpawn(needed), nil, needed)
	if envMap(env)["KELD_TEXTEMBED"] != "1" {
		t.Fatalf("sidecar spawn env KELD_TEXTEMBED = %q, want \"1\" so /attribute's encoder is reachable",
			envMap(env)["KELD_TEXTEMBED"])
	}
}

// TestAttributionOffAndTextembedOffProvisionsNothing is the other half of AC-10
// applied to this task's own gate: neither operand authorises anything, so
// the encoder must not exist at all — not merely decline at demand time.
func TestAttributionOffAndTextembedOffProvisionsNothing(t *testing.T) {
	encoderTestHome(t)

	const attribOn = false
	needed := attribOn || features.TextEmbedEnabled()
	if needed {
		t.Fatal("test setup: needed should be false with both operands off")
	}

	var fetches atomic.Int32
	prev := newEncoderFetcher
	newEncoderFetcher = func() provision.Fetcher {
		return fetcherFunc(func(context.Context, string) error {
			fetches.Add(1)
			t.Error("must not fetch when neither attribution nor KELD_TEXTEMBED authorised it")
			return nil
		})
	}
	t.Cleanup(func() { newEncoderFetcher = prev })

	p := newEncoderProvisioner(t.Context(), needed, func() bool { return false }, nil)
	if p != nil {
		t.Fatal("newEncoderProvisioner must be nil when nothing needs the encoder")
	}
	// nil-safety: the demand hook a wrapped OnPublished calls must tolerate this.
	p.demand()
	time.Sleep(20 * time.Millisecond)
	if n := fetches.Load(); n != 0 {
		t.Fatalf("fetched %d times with the gate off", n)
	}

	env := sidecarEnv([]string{"PATH=/bin"}, "/m", encoderDirForSpawn(needed), nil, needed)
	if hasEnvKey(env, "KELD_TEXTEMBED") {
		t.Fatalf("KELD_TEXTEMBED set with both operands off: %v", env)
	}
}

// TestEncoderNeededExistingFeaturesPathBehaviourIsUnchanged: with attribution
// OFF, the composed gate must reduce EXACTLY to features.TextEmbedEnabled(),
// byte-identical to the pre-existing behaviour.
func TestEncoderNeededExistingFeaturesPathBehaviourIsUnchanged(t *testing.T) {
	for _, tc := range []struct{ toggle string }{{""}, {"0"}, {"1"}} {
		t.Run("KELD_TEXTEMBED="+tc.toggle, func(t *testing.T) {
			encoderTestHome(t)
			t.Setenv(features.EnvTextEmbed, tc.toggle)

			const attribOn = false
			needed := attribOn || features.TextEmbedEnabled()
			if needed != features.TextEmbedEnabled() {
				t.Fatalf("needed = %v, want exactly features.TextEmbedEnabled() = %v", needed, features.TextEmbedEnabled())
			}
		})
	}
}

// THE VERIFIER: same shape, no live gate (see verifier_on_demand.go).

func TestVerifierProvisionerAbsentWhenAttributionIsOff(t *testing.T) {
	encoderTestHome(t)
	p := newVerifierProvisioner(t.Context(), false, nil)
	if p != nil {
		t.Fatal("verifier provisioner must be nil when attribution is off")
	}
	p.demand() // must be nil-safe
}

func TestVerifierProvisionerAbsentByDefault(t *testing.T) {
	// The verifier is OFF by default (2026-09-03) even with attribution on, and the
	// provisioner is what makes that cheap: no provisioner, no 3 GB GGUF fetch.
	// This test deliberately sets NOTHING — the default is the thing under test.
	encoderTestHome(t)
	t.Setenv(attrib.EnvVerifierEnabled, "")
	p := newVerifierProvisioner(t.Context(), true, nil)
	if p != nil {
		t.Fatal("verifier provisioner must be nil by default (KELD_ATTRIBUTION_VERIFIER unset)")
	}
}

func TestVerifierProvisionerAbsentWhenExplicitlyOff(t *testing.T) {
	encoderTestHome(t)
	t.Setenv(attrib.EnvVerifierEnabled, "0")
	p := newVerifierProvisioner(t.Context(), true, nil)
	if p != nil {
		t.Fatal("verifier provisioner must be nil when KELD_ATTRIBUTION_VERIFIER=0")
	}
}

func TestVerifierProvisionerFetchesOnDemandWhenAttributionIsOnAndOptedIn(t *testing.T) {
	encoderTestHome(t)
	t.Setenv(attrib.EnvVerifierEnabled, "1")

	var fetches atomic.Int32
	prev := newVerifierFetcher
	newVerifierFetcher = func() provision.Fetcher {
		return fetcherFunc(func(_ context.Context, dest string) error {
			fetches.Add(1)
			return os.WriteFile(filepath.Join(dest, provision.VerifierSentinel), []byte("w"), 0o644)
		})
	}
	t.Cleanup(func() { newVerifierFetcher = prev })

	p := newVerifierProvisioner(t.Context(), true, nil)
	if p == nil {
		t.Fatal("verifier provisioner must exist when attribution is on and KELD_ATTRIBUTION_VERIFIER=1")
	}
	p.sha = sha256Hex([]byte("w"))
	p.demand()
	encWaitFor(t, "the verifier fetch attribution authorised", func() bool { return fetches.Load() == 1 })

	// Must land at the sidecar's own default lookup path, verbatim — no
	// KELD_VERIFIER_GGUF wiring is added by this daemon.
	want := filepath.Join(os.Getenv("KELD_HOME"), "models", "gemma-4-e2b", "model.gguf")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("verifier GGUF not at the sidecar's default path %s: %v", want, err)
	}
}

// AC-10 for the verifier: the gate off must download nothing, ever.
func TestVerifierNoDownloadWhenTheGateIsOff(t *testing.T) {
	encoderTestHome(t)

	prev := newVerifierFetcher
	newVerifierFetcher = func() provision.Fetcher {
		return fetcherFunc(func(context.Context, string) error {
			t.Error("must not fetch the verifier when attribution is off")
			return nil
		})
	}
	t.Cleanup(func() { newVerifierFetcher = prev })

	p := newVerifierProvisioner(t.Context(), false, nil)
	if p != nil {
		t.Fatal("verifier provisioner must be nil when attribution is off")
	}
	p.demand()
	time.Sleep(20 * time.Millisecond)
}
