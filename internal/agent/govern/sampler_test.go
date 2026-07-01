package govern

import "testing"

func TestCPUSamplerBounded(t *testing.T) {
	s := CPUSampler{}
	for i := 0; i < 2; i++ {
		pct := s.CPUPercent()
		if pct < 0 || pct > 100 {
			t.Fatalf("CPUPercent() = %f, want [0,100]", pct)
		}
	}
}
