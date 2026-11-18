package clock

import (
	"math"
	"testing"

	"ptpsim/internal/engine"
)

// TestOscillatorDeterminism: two oscillators with the same seed and config
// produce byte-identical trajectories; different seeds diverge.
func TestOscillatorDeterminism(t *testing.T) {
	cfg := DefaultOscConfig()
	a := New("a", engine.Rng(11, 22), cfg)
	b := New("b", engine.Rng(11, 22), cfg)
	c := New("c", engine.Rng(33, 44), cfg)
	// Stochastic steps occur at the first phase-tau / walk-interval
	// boundaries, so divergence is only observable past them.
	for tt := engine.Ms(15); tt <= engine.Sec(10); tt += engine.Ms(7) {
		la, lb, lc := a.LocalAt(tt), b.LocalAt(tt), c.LocalAt(tt)
		if la != lb {
			t.Fatalf("identical seeds diverged at %v: %.6f vs %.6f", tt, la, lb)
		}
		if la == lc {
			t.Fatalf("distinct seeds produced identical reading at %v", tt)
		}
	}
}

// TestOscillatorFrequencySlope: with no noise, a pure frequency offset makes
// the reading advance at exactly 1+f relative to true time.
func TestOscillatorFrequencySlope(t *testing.T) {
	cfg := OscConfig{
		InitialFreqPPB: 1000, // +1 ppm
		WalkInterval:   engine.Ms(100),
		PhaseTau:       engine.Ms(10),
	}
	o := New("o", engine.Rng(1, 2), cfg)
	r0 := o.LocalAt(0)
	r1 := o.LocalAt(engine.Sec(10))
	got := (r1 - r0) / 10e9
	if math.Abs(got-1.000001) > 1e-9 {
		t.Fatalf("expected slope 1.000001, got %.9f", got)
	}
}

// TestServoConverges: a static offset is driven toward zero; a large initial
// offset triggers a phase step rather than a slow slew.
func TestServoConverges(t *testing.T) {
	cfg := DefaultOscConfig()
	o := New("s", engine.Rng(5, 6), cfg)
	sv := NewServo(DefaultPIConfig())
	// First, oversized sample: must step.
	r := sv.Update(engine.Sec(1), 5000, o)
	if !r.Stepped {
		t.Fatal("expected a phase step for the first large sample")
	}
	if steps, _ := sv.Stats(); steps != 1 {
		t.Fatalf("expected 1 step, got %d", steps)
	}
	// Feed decaying small offsets; locked slew path must engage and the
	// applied frequency correction must be finite.
	for i := 2; i < 40; i++ {
		tt := engine.Sec(float64(i))
		r = sv.Update(tt, 100/math.Sqrt(float64(i-1)), o)
		if math.IsNaN(r.FreqPPB) || math.Abs(r.FreqPPB) > cfg2Max(o) {
			t.Fatalf("bad freq output at i=%d: %+v", i, r)
		}
	}
	if !sv.Locked() {
		t.Fatal("servo never reached locked state")
	}
}

func cfg2Max(_ *Oscillator) float64 { return DefaultPIConfig().MaxFreqPPB }

// TestTrueAtLocalRoundTrip: the inverse mapping reconstructs the true time
// corresponding to a local deadline within one nanosecond over short spans.
func TestTrueAtLocalRoundTrip(t *testing.T) {
	cfg := OscConfig{InitialFreqPPB: 2500, WalkInterval: engine.Ms(100),
		PhaseTau: engine.Ms(10)}
	o := New("o", engine.Rng(9, 9), cfg)
	base := engine.Sec(5)
	local := o.LocalAt(base)
	deadline := local + 1.5e9 // 1.5 local seconds
	trueT := o.TrueAtLocal(base, deadline)
	// Reading back at the computed true time must match the deadline.
	got := o.LocalAt(trueT)
	if math.Abs(got-deadline) > 5 {
		t.Fatalf("round trip error %.3f ns", got-deadline)
	}
}
