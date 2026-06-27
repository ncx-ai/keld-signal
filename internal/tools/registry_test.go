package tools

import "testing"

func TestGetUnknown(t *testing.T) {
	if _, err := Get("nope"); err == nil {
		t.Fatal("expected error")
	}
}

func TestSelectExplicit(t *testing.T) {
	got, err := Select([]string{"codex"})
	if err != nil || len(got) != 1 || got[0].Name() != "codex" {
		t.Fatalf("got %v %v", got, err)
	}
}
