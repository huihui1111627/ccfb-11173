package theory

import (
	"math"
	"testing"

	"ptpsim/internal/engine"
)

// TestPredictGrandmasterFieldByField: the hand calculation must pick the
// device that wins each IEEE 1588 data-set comparison and must report the
// deciding field in its audit trail.
func TestPredictGrandmasterFieldByField(t *testing.T) {
	attrs := []DeviceAttr{
		{Name: "weak", Identity: "zzzz", Priority1: 128, Class: 248, Accuracy: 0x21, Variance: 0x4E5, Priority2: 128, GMCapable: true},
		{Name: "strong", Identity: "aaaa", Priority1: 10, Class: 6, Accuracy: 0x20, Variance: 0x4000, Priority2: 128, GMCapable: true},
		{Name: "mid", Identity: "mmmm", Priority1: 50, Class: 6, Accuracy: 0x20, Variance: 0x4000, Priority2: 128, GMCapable: true},
	}
	winner, traceSteps, err := PredictGrandmaster(attrs, 0)
	if err != nil {
		t.Fatal(err)
	}
	if winner != "strong" {
		t.Fatalf("predicted %s, want strong", winner)
	}
	foundPriority := false
	for _, s := range traceSteps {
		if s.Field == "priority1" {
			foundPriority = true
		}
	}
	if !foundPriority {
		t.Fatal("audit trail did not identify priority1 as a deciding field")
	}
}

// TestPredictGrandmasterPowerLoss: a powered-off candidate is excluded and
// the next-best live device wins.
func TestPredictGrandmasterPowerLoss(t *testing.T) {
	attrs := []DeviceAttr{
		{Name: "gm", Identity: "aaaa", Priority1: 10, Class: 6, Accuracy: 0x20, Variance: 0x4000, Priority2: 128, GMCapable: true,
			PoweredAt: func(tt engine.SimTime) bool { return tt < engine.Sec(8) }},
		{Name: "backup", Identity: "bbbb", Priority1: 20, Class: 6, Accuracy: 0x20, Variance: 0x4000, Priority2: 128, GMCapable: true,
			PoweredAt: func(tt engine.SimTime) bool { return true }},
	}
	if w, _, _ := PredictGrandmaster(attrs, engine.Sec(5)); w != "gm" {
		t.Fatalf("pre-failure winner = %s, want gm", w)
	}
	w, _, err := PredictGrandmaster(attrs, engine.Sec(9))
	if err != nil {
		t.Fatal(err)
	}
	if w != "backup" {
		t.Fatalf("post-failure winner = %s, want backup", w)
	}
}

// TestE2EAsymmetryClosedForm validates the clause 11.3 equations against a
// fully worked numeric example, including the residual asymmetry bias.
func TestE2EAsymmetryClosedForm(t *testing.T) {
	// Master/slave clocks identical; one-way delays 80 us and 20 us.
	// t1=0, Sync arrives at t2 = 0 + 80000.
	// Delay_Req egress t3 = 100000, arrives at master t4 = 100000 + 20000.
	t1, t2, t3, t4 := 0.0, 80000.0, 100000.0, 120000.0
	mean, offset, _ := E2EOffset(t1, t2, t3, t4, 0)
	if math.Abs(mean-50000) > 1e-6 {
		t.Fatalf("mean path = %.3f, want 50000", mean)
	}
	// Protocol offset estimate: t2-t1 - mean = 80000 - 50000 = 30000,
	// which is exactly the (dMS-dSM)/2 asymmetry bias.
	if math.Abs(offset-30000) > 1e-6 {
		t.Fatalf("offset = %.3f, want 30000 (asymmetry bias)", offset)
	}
	// With a +30000 correction the bias cancels.
	_, corrected, _ := E2EOffset(t1, t2, t3, t4, 30000)
	if math.Abs(corrected-0) > 1e-6 {
		t.Fatalf("corrected offset = %.3f, want 0", corrected)
	}
	residual := AsymmetryResidual(80000, 20000, 0)
	if math.Abs(residual-30000) > 1e-6 {
		t.Fatalf("residual = %.3f, want 30000", residual)
	}
}

// TestP2PPeerDelayFormula checks clause 11.4's four-timestamp calculation.
func TestP2PPeerDelayFormula(t *testing.T) {
	// Symmetric 10 us link, no clock offset:
	// t1=0, responder ingress t2=10000, egress t3=20000, requester t4=30000.
	got := P2PPeerDelay(0, 10000, 20000, 30000)
	if math.Abs(got-10000) > 1e-6 {
		t.Fatalf("peer delay = %.3f, want 10000", got)
	}
}

// TestFailoverTimeComposition: timeout plus propagation plus state-machine
// dwell add exactly per the hand calculation.
func TestFailoverTimeComposition(t *testing.T) {
	got := FailoverTime(engine.Sec(8), 3, engine.Sec(1), 2, engine.Us(10), engine.Ms(40))
	want := engine.Sec(8) + engine.Sec(3) + engine.Us(20) + engine.Ms(80)
	if got != want {
		t.Fatalf("failover = %v, want %v", got, want)
	}
}

// TestRingPassivePort verifies the hand-derived loop-breaking port against
// the converged timing-tree meeting point. Wiring is i.p2 -> (i+1).p1.
func TestRingPassivePort(t *testing.T) {
	order := []string{"bc0", "bc1", "bc2", "bc3", "bc4"}
	// GM = bc0 (index 0), 5-ring: far-side meeting node is index
	// (0 + (5-1)/2 + 1) mod 5 = 3 -> bc3, its clockwise-facing p1 Passive.
	dev, port := RingPassivePort("bc0", order, 0)
	if dev != "bc3" || port != 1 {
		t.Fatalf("5-ring passive = %s:p%d, want bc3:p1", dev, port)
	}
	// GM at index 1 -> meeting node index 4 -> bc4, p1.
	dev2, port2 := RingPassivePort("bc1", order, 1)
	if dev2 != "bc4" || port2 != 1 {
		t.Fatalf("rotated 5-ring passive = %s:p%d, want bc4:p1", dev2, port2)
	}
	// Even ring: antipodal node, deterministic tie port.
	even := []string{"n0", "n1", "n2", "n3"}
	d, p := RingPassivePort("n0", even, 0)
	if d != "n2" || p != 2 {
		t.Fatalf("4-ring passive = %s:p%d, want n2:p2", d, p)
	}
}
