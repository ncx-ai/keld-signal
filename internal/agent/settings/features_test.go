package settings

import "testing"

func boolp(b bool) *bool { return &b }

// ⚠️ BOTH DEFAULT OFF. Atlas has no consumer for a feature row yet, and this
// repo's standing rule is that rows nothing reads are opt-in and announced
// rather than quietly accumulated — the same reason KELD_TICK and KELD_BLOCKS
// ship off. A default that drifted to on would start collecting ~200 KB per
// user per active day on every deterministic-mode machine.
func TestFeatureTogglesDefaultOff(t *testing.T) {
	t.Setenv(FeaturesEnv, "")
	t.Setenv(FeaturesPublishEnv, "")

	l := NewLive(Settings{})
	if l.FeaturesEnabled() || l.FeaturesPublishEnabled() {
		t.Fatalf("toggles default on: features=%v publish=%v",
			l.FeaturesEnabled(), l.FeaturesPublishEnabled())
	}
	// A nil remote, and a remote that omits the block, must both leave them off.
	l.Apply(nil)
	if l.FeaturesEnabled() || l.FeaturesPublishEnabled() {
		t.Fatal("a nil remote turned the toggles on")
	}
	l.Apply(&Remote{})
	if l.FeaturesEnabled() || l.FeaturesPublishEnabled() {
		t.Fatal("a remote omitting the block turned the toggles on")
	}
}

// Local precedence is env > agent-config.json > off, resolved ONCE at
// construction so the org override is the last word — the same reason the
// region list is resolved there.
func TestFeatureTogglePrecedence(t *testing.T) {
	cases := []struct {
		name                    string
		env, envPub             string
		cfg, cfgPub             bool
		remote                  *Features
		wantFeat, wantPublished bool
	}{
		{name: "nothing set", wantFeat: false, wantPublished: false},
		{name: "config on", cfg: true, cfgPub: true, wantFeat: true, wantPublished: true},
		{name: "env on over an absent config", env: "1", envPub: "yes", wantFeat: true, wantPublished: true},
		{
			// Both directions, so an operator can turn OFF what a config file
			// turned on without editing the file.
			name: "env off over a config that says on",
			env:  "0", envPub: "false", cfg: true, cfgPub: true,
		},
		{
			name: "remote wins over env",
			env:  "1", envPub: "1",
			remote:   &Features{Enabled: boolp(false), Publish: boolp(false)},
			wantFeat: false, wantPublished: false,
		},
		{
			name:     "remote can turn on what nothing local did",
			remote:   &Features{Enabled: boolp(true), Publish: boolp(true)},
			wantFeat: true, wantPublished: true,
		},
		{
			// The two govern different halves: collect locally, hold.
			name:     "collect without publishing is representable",
			remote:   &Features{Enabled: boolp(true), Publish: boolp(false)},
			wantFeat: true, wantPublished: false,
		},
		{
			// A partial block leaves the unmentioned key at the local value,
			// which is what the pointer fields are for.
			name:     "a partial remote block leaves the other key alone",
			env:      "1",
			remote:   &Features{Publish: boolp(true)},
			wantFeat: true, wantPublished: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(FeaturesEnv, tc.env)
			t.Setenv(FeaturesPublishEnv, tc.envPub)
			l := NewLive(Settings{Features: tc.cfg, FeaturesPublish: tc.cfgPub})
			l.Apply(&Remote{Features: tc.remote})
			if l.FeaturesEnabled() != tc.wantFeat {
				t.Fatalf("features = %v, want %v", l.FeaturesEnabled(), tc.wantFeat)
			}
			if l.FeaturesPublishEnabled() != tc.wantPublished {
				t.Fatalf("publish = %v, want %v", l.FeaturesPublishEnabled(), tc.wantPublished)
			}
		})
	}
}

// A remote that stops mentioning the block must fall back to the LOCAL value,
// not to whatever the last poll happened to say — Apply recomputes from the
// base every time, which is what makes an org clearing a key mean "unset".
func TestClearingTheRemoteBlockFallsBackToLocal(t *testing.T) {
	t.Setenv(FeaturesEnv, "1")
	t.Setenv(FeaturesPublishEnv, "")
	l := NewLive(Settings{})

	l.Apply(&Remote{Features: &Features{Enabled: boolp(false), Publish: boolp(true)}})
	if l.FeaturesEnabled() || !l.FeaturesPublishEnabled() {
		t.Fatal("remote override did not apply")
	}
	l.Apply(&Remote{})
	if !l.FeaturesEnabled() || l.FeaturesPublishEnabled() {
		t.Fatalf("did not fall back to local: features=%v publish=%v",
			l.FeaturesEnabled(), l.FeaturesPublishEnabled())
	}
}
