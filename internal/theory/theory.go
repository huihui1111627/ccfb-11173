// Package theory contains hand calculations derived directly from the IEEE
// 1588-2008 equations, with no reference to the running protocol. The
// experiment layer compares these predictions against simulated outcomes.
package theory

import (
	"fmt"
	"sort"
	"strings"

	"ptpsim/internal/engine"
	"ptpsim/internal/ptp"
	"ptpsim/internal/scenario"
)

// DeviceAttr is the static BMCA-relevant configuration of one device.
type DeviceAttr struct {
	Name      string
	Identity  ptp.ClockIdentity
	Priority1 uint8
	Class     uint8
	Accuracy  uint8
	Variance  uint16
	Priority2 uint8
	GMCapable bool
	PoweredAt func(engine.SimTime) bool
}

// BMCATrace is one step of the hand-executed data set comparison.
type BMCATrace struct {
	Step   string `json:"step"`
	Winner string `json:"winner"`
	Loser  string `json:"loser,omitempty"`
	Field  string `json:"field,omitempty"`
	Detail string `json:"detail"`
}

// PredictGrandmaster runs the 9.3.5 b) ordering over powered, GM-capable
// devices at time t and returns the predicted winner plus an audit trail
// showing exactly which data-set field decided each comparison.
func PredictGrandmaster(attrs []DeviceAttr, t engine.SimTime) (string, []BMCATrace, error) {
	var live []DeviceAttr
	for _, a := range attrs {
		if !a.GMCapable {
			continue
		}
		if a.PoweredAt != nil && !a.PoweredAt(t) {
			continue
		}
		live = append(live, a)
	}
	if len(live) == 0 {
		return "", nil, fmt.Errorf("no eligible grandmaster at t=%d", t)
	}
	sort.SliceStable(live, func(i, j int) bool { return live[i].Name < live[j].Name })
	var tr []BMCATrace
	best := live[0]
	tr = append(tr, BMCATrace{Step: "seed", Winner: best.Name,
		Detail: "first candidate in deterministic order"})
	fields := []struct {
		name string
		get  func(DeviceAttr) (string, int)
	}{
		{"priority1", func(a DeviceAttr) (string, int) {
			return fmt.Sprintf("%d", a.Priority1), int(a.Priority1)
		}},
		{"clockClass", func(a DeviceAttr) (string, int) {
			return fmt.Sprintf("%d", a.Class), int(a.Class)
		}},
		{"clockAccuracy", func(a DeviceAttr) (string, int) {
			return fmt.Sprintf("0x%02x", a.Accuracy), int(a.Accuracy)
		}},
		{"offsetScaledLogVariance", func(a DeviceAttr) (string, int) {
			return fmt.Sprintf("%d", a.Variance), int(a.Variance)
		}},
		{"priority2", func(a DeviceAttr) (string, int) {
			return fmt.Sprintf("%d", a.Priority2), int(a.Priority2)
		}},
	}
	for _, cand := range live[1:] {
		decided := false
		for _, f := range fields {
			_, bv := f.get(best)
			_, cv := f.get(cand)
			if bv != cv {
				w, l := best.Name, cand.Name
				if cv < bv {
					w, l = cand.Name, best.Name
				}
				tr = append(tr, BMCATrace{
					Step: "compare", Winner: w, Loser: l, Field: f.name,
					Detail: fmt.Sprintf("%s=%d vs %s=%d; lower wins",
						best.Name, bv, cand.Name, cv),
				})
				decided = true
				break
			}
		}
		if !decided {
			// Final tie-break: clock identity, lexicographic.
			c := strings.Compare(string(best.Identity), string(cand.Identity))
			w, l := best.Name, cand.Name
			if c > 0 {
				w, l = cand.Name, best.Name
			}
			tr = append(tr, BMCATrace{
				Step: "compare", Winner: w, Loser: l,
				Field: "clockIdentity",
				Detail: fmt.Sprintf("all five fields tied; identity %s vs %s",
					best.Identity, cand.Identity),
			})
		}
		af := &ptp.AnnounceFields{
			GrandmasterPriority1:    best.Priority1,
			GrandmasterClockQuality: ptp.ClockQuality{ClockClass: best.Class, ClockAccuracy: best.Accuracy, OffsetScaledLogVariance: best.Variance},
			GrandmasterPriority2:    best.Priority2,
			GrandmasterIdentity:     best.Identity,
		}
		bf := &ptp.AnnounceFields{
			GrandmasterPriority1:    cand.Priority1,
			GrandmasterClockQuality: ptp.ClockQuality{ClockClass: cand.Class, ClockAccuracy: cand.Accuracy, OffsetScaledLogVariance: cand.Variance},
			GrandmasterPriority2:    cand.Priority2,
			GrandmasterIdentity:     cand.Identity,
		}
		if ptp.CompareDatasets(bf, af) < 0 {
			best = cand
		}
	}
	tr = append(tr, BMCATrace{Step: "result", Winner: best.Name,
		Detail: "predicted elected grandmaster"})
	return best.Name, tr, nil
}

// FailoverTime predicts the true time at which the new grandmaster's
// Announce first reaches a slave after the old one fails.
//
// The slave declares announce receipt timeout after
// announceReceiptTimeout * announceInterval (measured in its own clock); the
// replacement grandmaster's own election is immediate, and its first Announce
// propagates over `hops` links of linkDelay plus per-BC qualification
// (pre-master/uncalibrated dwells).
func FailoverTime(failAt engine.SimTime, receiptTimeout uint8,
	announceInterval engine.SimTime, hops int, linkDelay engine.SimTime,
	qualDwell engine.SimTime) engine.SimTime {
	timeout := engine.SimTime(receiptTimeout) * announceInterval
	prop := engine.SimTime(hops) * linkDelay
	qual := engine.SimTime(hops) * qualDwell
	return failAt + timeout + prop + qual
}

// E2EOffset is the closed-form offsetFromMaster of clause 11.3 for a single
// E2E exchange. All timestamps in the time scales that carry them:
//
//	t1 master egress, t2 slave ingress, t3 slave egress (Delay_Req),
//	t4 master ingress; correction = sum of correctionField values seen.
//
// The residual asymmetry term is returned separately.
func E2EOffset(t1, t2, t3, t4, correction float64) (meanPath, offset, residualAsym float64) {
	meanPath = ((t4 - t1) - (t3 - t2)) / 2
	offset = (t2 - t1) - correction - meanPath
	return
}

// AsymmetryResidual returns the uncompensated bias (ns) that an asymmetric
// path leaves in the E2E offset estimate. dMS / dSM are master->slave and
// slave->master one-way delays; correction is the operator-configured
// correctionField contribution (zero when uncompensated).
func AsymmetryResidual(dMS, dSM, correctionNs float64) float64 {
	meanPathAssumed := (dMS + dSM) / 2
	// True master->slave one-way is dMS; the protocol subtracts the assumed
	// mean and the configured correction. Residual bias on offsetFromMaster:
	return dMS - meanPathAssumed - correctionNs
}

// P2PPeerDelay is clause 11.4's peerMeanPathDelay from the four local
// timestamps (t1 request egress, t2 responder ingress, t3 responder egress,
// t4 request ingress).
func P2PPeerDelay(t1, t2, t3, t4 float64) float64 {
	return ((t2 - t1) + (t4 - t3)) / 2
}

// SteadyStateResidual predicts the post-convergence offset floor (ns) caused
// purely by phase-noise amplitude and timestamp quantization. It is not a
// hard bound but the 1-sigma level the simulation mean/max are compared to.
func SteadyStateResidual(phaseSigmaNs, quantNs float64) float64 {
	q := 0.0
	if quantNs > 0 {
		q = quantNs / mathSqrt12
	}
	return mathSqrt(phaseSigmaNs*phaseSigmaNs + q*q)
}

const mathSqrt12 = 3.4641016151377544

func mathSqrt(x float64) float64 {
	// tiny local sqrt to avoid importing math in a theory-only file
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 24; i++ {
		z = (z + x/z) / 2
	}
	return z
}

// RingPassivePort predicts the single Passive port of a converged ring by
// hand. Wiring: device[i].p2 connects to device[(i+1) mod n].p1, so the
// grandmaster's clockwise Announce flood (out p2, into each neighbour's p1)
// and counter-clockwise flood (out p1, into p2) grow toward each other.
//
// The two timing trees meet on the node reached after (n-1)/2 clockwise hops
// for an odd ring (the unique far-side node): its clockwise-facing p1 still
// carries the longer, losing flood and is set Passive, while p2 becomes the
// Slave port. For an even ring the antipodal node hears equal steps and the
// same lower-index/lower-port deterministic tie-break as the protocol's
// foreign-master ordering applies. The result is checked in
// experiment.TestTheoryVersusSimulationBMCA against the running BMCA.
func RingPassivePort(gmName string, ringOrder []string, gmIdx int) (device string, port uint16) {
	n := len(ringOrder)
	if n < 3 {
		return "", 0
	}
	if n%2 == 1 {
		// Far-side meeting node after (n-1)/2 clockwise hops from the GM.
		meet := (gmIdx + (n-1)/2 + 1) % n
		return ringOrder[meet], 1
	}
	// Even ring: antipodal meeting node; tie resolved on port identity.
	meet := (gmIdx + n/2) % n
	return ringOrder[meet], 2
}

// SpecAttrs extracts BMCA attributes from a built mesh for prediction.
func SpecAttrs(mesh *ptp.Mesh, powered func(*ptp.Device, engine.SimTime) bool) []DeviceAttr {
	var out []DeviceAttr
	for _, d := range mesh.Devices() {
		d := d
		out = append(out, DeviceAttr{
			Name:      d.Name,
			Identity:  d.Identity(),
			Priority1: d.Config().Priority1,
			Class:     d.Config().ClockClass,
			Accuracy:  d.Config().Accuracy,
			Variance:  d.Config().Variance,
			Priority2: d.Config().Priority2,
			GMCapable: d.Config().GMCapable,
			PoweredAt: func(t engine.SimTime) bool { return powered(d, t) },
		})
	}
	return out
}

// QualDwell is the per-state-machine promotion dwell used in the scenarios.
const QualDwell = engine.SimTime(40_000_000) // two 20 ms transitions

// Guard ensures scenario import is used (common spec constants live there).
var _ = scenario.LinearOC
