package govern

import "testing"

type fakeSampler struct {
	load float64
}

func (f fakeSampler) CPUPercent() float64 {
	return f.load
}

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

func TestAdmitShedsMoreUnderHigherLoad(t *testing.T) {
	mk := func(load float64) int {
		g := New(nil, 4)
		for i := 0; i < 30; i++ {
			g.Observe(load)
		}
		admits := 0
		for i := 0; i < 100; i++ {
			if g.Admit() {
				admits++
			}
		}
		return admits
	}
	a99, a88 := mk(99), mk(88)
	if !(a99 < a88 && a88 < 100) {
		t.Fatalf("severe load must shed more: admits@99=%d admits@88=%d (want a99<a88<100)", a99, a88)
	}
}

func TestGovernorConcurrentSampleAndAdmit(t *testing.T) {
	g := New(fakeSampler{load: 90}, 4)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			g.Sample()
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		_ = g.Admit()
		_ = g.Concurrency()
	}
	<-done
}
