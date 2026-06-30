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
