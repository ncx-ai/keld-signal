package govern

import "testing"

func TestConcurrencyDropsUnderLoad(t *testing.T) {
	g := New(nil, 4)
	for i := 0; i < 20; i++ {
		g.Observe(95) // sustained high CPU
	}
	if g.Concurrency() != 1 {
		t.Fatalf("high load -> concurrency 1, got %d", g.Concurrency())
	}
}

func TestConcurrencyFullWhenCalm(t *testing.T) {
	g := New(nil, 4)
	for i := 0; i < 20; i++ {
		g.Observe(5)
	}
	if g.Concurrency() != 4 {
		t.Fatalf("calm -> maxConc, got %d", g.Concurrency())
	}
}

func TestAdmitShedsUnderSustainedHighLoad(t *testing.T) {
	g := New(nil, 4)
	for i := 0; i < 20; i++ {
		g.Observe(99)
	}
	shed := 0
	for i := 0; i < 100; i++ {
		if !g.Admit() {
			shed++
		}
	}
	if shed == 0 {
		t.Fatal("sustained high load should shed some admissions")
	}
}
