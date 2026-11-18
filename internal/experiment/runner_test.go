package experiment

import (
	"math"
	"testing"

	"ptpsim/internal/engine"
	"ptpsim/internal/ptp"
	"ptpsim/internal/scenario"
)

func TestLinearE2EConvergence(t *testing.T) {
	s := scenario.Spec{
		Name:       "linear-e2e",
		Kind:       scenario.LinearOC,
		Seed:       42,
		Duration:   engine.Sec(20),
		Delay:      ptp.DelayE2E,
		TwoStep:    true,
		LinkDelay:  engine.Us(50),
		ChainDepth: 2,
	}
	res, mesh, err := Run(s)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.FinalGM != "gm" {
		t.Fatalf("expected gm elected, got %q", res.FinalGM)
	}
	if len(res.PassivePorts) != 0 {
		t.Fatalf("linear topology must not have passive ports, got %v", res.PassivePorts)
	}
	if !res.Reproducible && res.ReproHash == "" {
		// first run alone: check samples exist and converge
	}
	// Every slave should end with a small true offset.
	for name, maxAbs := range res.SlaveSummary.PerDeviceMax {
		if maxAbs > 500 {
			t.Errorf("slave %s max |offset| %.2f ns exceeds 500 ns", name, maxAbs)
		}
	}
	if len(res.SlaveSummary.PerDeviceMean) == 0 {
		t.Fatal("no slave samples collected")
	}
	t.Logf("mean %.3f ns max %.3f ns rms %.3f ns; GM=%s",
		res.SlaveSummary.MeanAbsNs, res.SlaveSummary.MaxAbsNs,
		res.SlaveSummary.RMSNs, res.FinalGM)
	for _, d := range mesh.Devices() {
		for _, p := range d.Ports() {
			t.Logf("  %s p%d %s", d.Name, p.Index, p.Role())
		}
	}
}

func TestReproducibility(t *testing.T) {
	s := scenario.Spec{
		Name: "repro", Kind: scenario.TransparentTC, Seed: 7,
		Duration: engine.Sec(8), Delay: ptp.DelayP2P, TwoStep: true,
		LinkDelay: engine.Us(20), ChainDepth: 1,
	}
	a, b, err := RunReproducible(s)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if a.TraceHash != b.TraceHash {
		t.Fatalf("trace hashes differ: %s vs %s", a.TraceHash, b.TraceHash)
	}
	if !a.Reproducible {
		t.Fatal("reproducibility flag false")
	}
}

func TestGMFailover(t *testing.T) {
	s := scenario.Spec{
		Name: "failover", Kind: scenario.LinearOC, Seed: 99,
		Duration: engine.Sec(25), Delay: ptp.DelayE2E, TwoStep: false,
		LinkDelay:  engine.Us(10),
		ChainDepth: 1,
		Events: []scenario.Event{{
			At: engine.Sec(8), Kind: scenario.EvPowerOffGM, Device: "gm",
		}},
	}
	res, mesh, err := Run(s)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Failover == nil {
		t.Fatal("no failover recorded")
	}
	if res.Failover.NewGM == "" || res.Failover.NewGM == "gm" {
		t.Fatalf("bad new GM: %+v", res.Failover)
	}
	if res.FinalGM == "gm" {
		t.Fatal("gm still elected after power off")
	}
	// The timeout fires announceReceiptTimeout (3) local seconds after the
	// LAST received Announce, not after the power-off instant. The new GM's
	// oscillator carries a fractional frequency offset, so true elapsed time
	// is the nominal timeout divided by (1+f). Derive both anchors from the
	// trace instead of asserting wall-clock seconds from the failure event.
	bc1 := mesh.Device("bc1")
	freqFrac := bc1.Osc().FreqFreeRunPPB(res.Failover.FailAt) / 1e9
	var lastAnn engine.SimTime
	for _, m := range mesh.Rec.Messages() {
		if m.MsgType == "Announce" && m.Kind == "recv" &&
			m.Device == "bc1" && m.Port == 1 && m.T <= res.Failover.FailAt {
			if m.T > lastAnn {
				lastAnn = m.T
			}
		}
	}
	if lastAnn == 0 {
		t.Fatal("no pre-failure Announce received by the replacement GM")
	}
	nominal := 3.0 // announceReceiptTimeout * announceInterval (local s)
	expectedFromLast := nominal / (1 + freqFrac)
	gotFromLast := res.Failover.ReelectionAt.Seconds() - lastAnn.Seconds()
	if math.Abs(gotFromLast-expectedFromLast) > 0.15 {
		t.Errorf("timeout %.3f s from last Announce deviates from local-clock prediction %.3f s (freq %.1f ppb)",
			gotFromLast, expectedFromLast, freqFrac*1e9)
	}
	t.Logf("failover: %s -> %s; timeout %.3f local-scaled s after last Announce (predicted %.3f s)",
		res.Failover.OldGM, res.Failover.NewGM, gotFromLast, expectedFromLast)
}

func TestRingPassive(t *testing.T) {
	s := scenario.Spec{
		Name: "ring", Kind: scenario.RingBC, Seed: 5,
		Duration: engine.Sec(12), Delay: ptp.DelayE2E, TwoStep: true,
		LinkDelay: engine.Us(15), RingSize: 5,
	}
	res, _, err := Run(s)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.PassivePorts) != 1 {
		t.Fatalf("ring must converge with exactly 1 passive port, got %v", res.PassivePorts)
	}
	if res.FinalGM != "bc0" {
		t.Fatalf("expected bc0 as GM, got %s", res.FinalGM)
	}
	t.Logf("passive: %v", res.PassivePorts)
}

func TestFollowUpLossInvalidates(t *testing.T) {
	s := scenario.Spec{
		Name: "foldup-loss", Kind: scenario.LinearOC, Seed: 11,
		Duration: engine.Sec(34), Delay: ptp.DelayE2E, TwoStep: true,
		LinkDelay:  engine.Us(10),
		ChainDepth: 0,
		Events: []scenario.Event{
			{At: engine.Sec(3), Kind: scenario.EvFollowUpLoss, Device: "gm", Port: 1},
			{At: engine.Sec(7), Kind: scenario.EvClearFollowLoss, Device: "gm", Port: 1},
		},
	}
	res, _, err := Run(s)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.FollowUpDropped == 0 {
		t.Fatal("expected dropped Follow_Up events in trace")
	}
	// Statistics window starts at Duration/2 = 17 s, ten full Sync intervals
	// after the 7 s recovery, so the resync phase step and PI re-convergence
	// transients have settled. The residual is phase-noise limited.
	if math.IsNaN(res.SlaveSummary.MeanAbsNs) || res.SlaveSummary.MeanAbsNs > 500 {
		t.Fatalf("slave did not recover after Follow_Up loss: %+v", res.SlaveSummary)
	}
	// Steady state after recovery must be tightly bounded.
	if res.SlaveSummary.MaxAbsNs > 1500 {
		t.Fatalf("post-recovery max offset too large: %.2f ns",
			res.SlaveSummary.MaxAbsNs)
	}
}

func TestAsymmetryCompensation(t *testing.T) {
	asym := map[string]scenario.AsymLink{
		"gm-bc1": {Forward: engine.Us(80), Backward: engine.Us(20), Comp: true},
	}
	s := scenario.Spec{
		Name: "asym", Kind: scenario.LinearOC, Seed: 13,
		Duration: engine.Sec(15), Delay: ptp.DelayE2E, TwoStep: false,
		LinkDelay:  engine.Us(40),
		ChainDepth: 1, AsymLinks: asym,
	}
	res, _, err := Run(s)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := res.SlaveSummary.MeanAbsNs; got > 200 {
		t.Fatalf("asymmetry-compensated mean offset too large: %.2f ns", got)
	}
}
