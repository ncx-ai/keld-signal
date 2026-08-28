package settings_test

import (
	"encoding/json"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// An absent agent_release block must decode to nil AND must not enable
// updates. This is the strictest reading of the rule already written for
// Features: an omitted key leaves the local base rather than defaulting on.
// Auto-update is the most consequential thing Atlas could switch on by
// omission, so the nil receiver is answered explicitly rather than by luck.
func TestAbsentReleaseBlockMeansNoUpdate(t *testing.T) {
	var r settings.Remote
	if err := json.Unmarshal([]byte(`{"include_entity_text":true}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.Release != nil {
		t.Fatalf("absent block decoded non-nil: %+v", r.Release)
	}
	if _, _, enabled := r.Release.Target(); enabled {
		t.Fatal("a nil Release must not enable updates")
	}
}

func TestReleaseTargetReadsVersionAndBase(t *testing.T) {
	var r settings.Remote
	body := `{"agent_release":{"enabled":true,"version":"v0.4.2","base_url":"https://mirror/x"}}`
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	v, base, enabled := r.Release.Target()
	if v != "v0.4.2" || base != "https://mirror/x" || !enabled {
		t.Fatalf("got %q %q %v", v, base, enabled)
	}
}

func TestReleaseDisabledExplicitly(t *testing.T) {
	var r settings.Remote
	if err := json.Unmarshal([]byte(`{"agent_release":{"enabled":false,"version":"v9"}}`), &r); err != nil {
		t.Fatal(err)
	}
	if _, _, enabled := r.Release.Target(); enabled {
		t.Fatal("enabled:false must disable")
	}
}

// A block that names no version cannot enable an update: there is nothing to
// move to, and treating "enabled with no pin" as latest-wins would reintroduce
// the GitHub-polling behaviour the design refuses.
func TestReleaseEnabledWithNoVersionIsNotATarget(t *testing.T) {
	var r settings.Remote
	if err := json.Unmarshal([]byte(`{"agent_release":{"enabled":true}}`), &r); err != nil {
		t.Fatal(err)
	}
	v, _, enabled := r.Release.Target()
	if v != "" {
		t.Fatalf("version = %q, want empty", v)
	}
	if !enabled {
		t.Fatal("enabled:true should still report enabled; the empty version is what stops the update")
	}
}

// enabled is absent: a version pinned without an explicit enable must not act.
func TestReleaseNilEnabledIsOff(t *testing.T) {
	var r settings.Remote
	if err := json.Unmarshal([]byte(`{"agent_release":{"version":"v0.4.2"}}`), &r); err != nil {
		t.Fatal(err)
	}
	if _, _, enabled := r.Release.Target(); enabled {
		t.Fatal("a nil enabled must not enable updates")
	}
}
