package agentcfg

import (
	"os"
	"testing"
)

func TestWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KELD_HOME", dir)

	in := Info{Port: 8765, Secret: "deadbeef"}
	if err := Write(in); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got == nil || got.Port != 8765 || got.Secret != "deadbeef" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestSetSidecarPortUpdatesExisting(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if err := Write(Info{Port: 8765, Secret: "deadbeef"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := SetSidecarPort(40313); err != nil {
		t.Fatalf("SetSidecarPort: %v", err)
	}
	got, err := Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// The sidecar port is added without disturbing the daemon port/secret.
	if got.SidecarPort != 40313 || got.Port != 8765 || got.Secret != "deadbeef" {
		t.Fatalf("after SetSidecarPort: %+v", got)
	}
}

func TestSetSidecarPortErrorsWhenNoInfo(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if err := SetSidecarPort(1234); err == nil {
		t.Fatal("want error when agent.json is absent, got nil")
	}
}

func TestReadAbsentReturnsNil(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	got, err := Read()
	if err != nil || got != nil {
		t.Fatalf("want (nil,nil), got (%+v,%v)", got, err)
	}
}

func TestWritePerms0600(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KELD_HOME", dir)
	if err := Write(Info{Port: 1, Secret: "x"}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dir + "/agent.json")
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 600", perm)
	}
}

func TestNewSecretIsRandomHex(t *testing.T) {
	a, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewSecret()
	if a == b {
		t.Fatal("secrets should differ")
	}
	if len(a) != 64 {
		t.Fatalf("len = %d, want 64", len(a))
	}
}
