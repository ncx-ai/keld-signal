package hardware

import (
	"runtime"
	"testing"
)

// AC-12: Collect never fails and always fills the fields the host can answer.
func TestCollectBestEffort(t *testing.T) {
	got := Collect()
	if got.LogicalCores < 1 {
		t.Fatalf("cores = %d", got.LogicalCores)
	}
	if runtime.GOOS == "darwin" && (got.CPUModel == "" || got.MemTotalGB < 1) {
		t.Fatalf("darwin should resolve cpu+mem, got %+v", got)
	}
}
