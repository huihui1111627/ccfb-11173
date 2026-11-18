package experiment

import (
	"fmt"
	"math"
	"testing"

	"ptpsim/internal/engine"
	"ptpsim/internal/ptp"
	"ptpsim/internal/scenario"
	"ptpsim/internal/theory"
)

// TestTheoryVersusSimulationBMCA runs the independent hand calculation of the
// best master clock algorithm and asserts the simulated election matches, for
// every built-in topology.
func TestTheoryVersusSimulationBMCA(t *testing.T) {
	specs := []scenario.Spec{
		{Name: "linear", Kind: scenario.LinearOC, Seed: 77,
			Duration: engine.Sec(12), Delay: ptp.DelayE2E, TwoStep: true,
			LinkDelay: engine.Us(20), ChainDepth: 2},
		{Name: "ring", Kind: scenario.RingBC, Seed: 77,
			Duration: engine.Sec(12), Delay: ptp.DelayE2E, TwoStep: true,
			LinkDelay: engine.Us(20), RingSize: 5},
		{Name: "tc", Kind: scenario.TransparentTC, Seed: 77,
			Duration: engine.Sec(12), Delay: ptp.DelayP2P, TwoStep: true,
			LinkDelay: engine.Us(20), ChainDepth: 1},
	}
	for _, s := range specs {
		t.Run(s.Name, func(t *testing.T) {
			res, mesh, err := Run(s)
			if err != nil {
				t.Fatal(err)
			}
			attrs := theory.SpecAttrs(mesh, func(*ptp.Device, engine.SimTime) bool { return true })
			predicted, steps, err := theory.PredictGrandmaster(attrs, s.Duration-engine.Ns(1))
			if err != nil {
				t.Fatal(err)
			}
			if predicted != res.FinalGM {
				t.Fatalf("BMCA mismatch: hand=%s sim=%s", predicted, res.FinalGM)
			}
			if len(steps) < 2 {
				t.Fatal("expected a field-by-field comparison audit trail")
			}
			// Every synchronized slave must be locked to the predicted GM.
			for _, d := range mesh.Devices() {
				if d.SlavePort() == nil {
					continue
				}
				if d.Parent() == nil ||
					string(d.Parent().GrandmasterIdentity) !=
						string(mesh.Device(predicted).Identity()) {
					t.Fatalf("slave %s not locked to predicted GM %s", d.Name, predicted)
				}
			}
			// For the ring, the hand-derived loop-breaking port must match
			// the single Passive port the simulation converged on.
			if s.Kind == scenario.RingBC {
				var order []string
				gmIdx := 0
				for i, d := range mesh.Devices() {
					order = append(order, d.Name)
					if d.Name == predicted {
						gmIdx = i
					}
				}
				pDev, pPort := theory.RingPassivePort(predicted, order, gmIdx)
				want := fmt.Sprintf("%s:p%d", pDev, pPort)
				if len(res.PassivePorts) != 1 || res.PassivePorts[0] != want {
					t.Fatalf("passive port: hand=%s sim=%v", want, res.PassivePorts)
				}
			}
		})
	}
}

// TestTheoryFailoverPrediction: after the GM powers off the hand calculation
// must predict the replacement, and the simulated switch must occur within one
// announce interval of the local-clock-scaled timeout prediction.
func TestTheoryFailoverPrediction(t *testing.T) {
	s := scenario.Spec{
		Name: "fo", Kind: scenario.LinearOC, Seed: 88,
		Duration: engine.Sec(26), Delay: ptp.DelayE2E, TwoStep: false,
		LinkDelay: engine.Us(10), ChainDepth: 1,
		Events: []scenario.Event{{At: engine.Sec(10), Kind: scenario.EvPowerOffGM, Device: "gm"}},
	}
	res, mesh, err := Run(s)
	if err != nil {
		t.Fatal(err)
	}
	powered := func(d *ptp.Device, at engine.SimTime) bool {
		return !(d.Name == "gm" && at >= engine.Sec(10))
	}
	attrs := theory.SpecAttrs(mesh, powered)
	predicted, _, err := theory.PredictGrandmaster(attrs, s.Duration-engine.Ns(1))
	if err != nil {
		t.Fatal(err)
	}
	if predicted != res.Failover.NewGM {
		t.Fatalf("failover GM: hand=%s sim=%s", predicted, res.Failover.NewGM)
	}
	// Independent failover-time estimate from the theory package.
	pred := theory.FailoverTime(engine.Sec(10), 3, engine.Sec(1), 1,
		engine.Us(10), theory.QualDwell)
	if math.Abs(float64(res.Failover.ReelectionAt-pred)) > float64(engine.Sec(2)) {
		t.Fatalf("failover timing outside theory bound: sim=%v theory=%v",
			res.Failover.ReelectionAt, pred)
	}
}

// TestNoNoiseTheoreticalFloor: with stochastic noise disabled, a single
// master/slave pair converges to an offset limited only by numeric
// resolution once the PI loop has settled, demonstrating the protocol
// equations themselves are unbiased. The assertion uses the final 20% of the
// run so initial phase-step / slew transients are excluded.
func TestNoNoiseTheoreticalFloor(t *testing.T) {
	s := scenario.Spec{
		Name: "quiet", Kind: scenario.LinearOC, Seed: 55,
		Duration: engine.Sec(40), Delay: ptp.DelayE2E, TwoStep: true,
		LinkDelay: engine.Us(30), ChainDepth: 0, NoNoise: true,
	}
	res, _, err := Run(s)
	if err != nil {
		t.Fatal(err)
	}
	// Steady-state tail: samples in the last 20% of the run.
	cutoff := engine.SimTime(float64(s.Duration) * 0.8)
	var tailMax float64
	for _, sm := range res.TrueSamples {
		if sm.Device != "slave1" || sm.T < cutoff {
			continue
		}
		if a := math.Abs(sm.TrueOffNs); a > tailMax {
			tailMax = a
		}
	}
	if tailMax > 5 {
		t.Fatalf("noise-free steady-state max offset %.3f ns exceeds 5 ns floor",
			tailMax)
	}
}
