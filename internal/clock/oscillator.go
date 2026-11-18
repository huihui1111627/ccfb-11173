// Package clock models the local oscillator of a PTP node and the servo that
// disciplines it.
//
// An oscillator maps true (simulator) time t to a local clock reading c(t):
//
//	c(t) = fixedOffset
//	     + integral_0^t (1 + f(s)) ds        // f: drift + random walk + servo
//	     + phi(t)                            // low-pass filtered phase noise
//	     + phaseSteps                        // hard servo phase corrections
//
// f is treated as piecewise constant between walk-interval boundaries, phi as
// a staircase between phase-tau boundaries, so the integral is exact and every
// random draw comes from a per-node rand source: identical seeds reproduce the
// same clock trajectory nanosecond for nanosecond.
package clock

import (
	"math"
	"math/rand/v2"

	"ptpsim/internal/engine"
)

// OscConfig parameterizes one local oscillator.
type OscConfig struct {
	// InitialFreqPPB is the static fractional frequency offset in parts per
	// billion (e.g. 5000 = 5 ppm fast).
	InitialFreqPPB float64
	// DriftPPBPerSec is a deterministic linear frequency ramp (ppb/s),
	// modelling aging/temperature drift.
	DriftPPBPerSec float64
	// WalkSigmaPPB is the standard deviation of the frequency random walk
	// step applied per WalkInterval.
	WalkSigmaPPB float64
	WalkInterval engine.SimTime
	// PhaseSigmaNs is the standard deviation of white phase noise per step;
	// it is integrated through a one-pole low-pass filter with PhaseTau.
	PhaseSigmaNs float64
	PhaseTau     engine.SimTime
	// FixedPhaseOffsetNs offsets the oscillator at t=0.
	FixedPhaseOffsetNs float64
	// TimeResolutionNs, when > 0, quantizes the local reading (hardware
	// timestamp granularity).
	TimeResolutionNs float64
}

// DefaultOscConfig returns a TCXO-like configuration: sub-ppm initial offset,
// small random walk, sub-nanosecond phase noise.
func DefaultOscConfig() OscConfig {
	return OscConfig{
		InitialFreqPPB:     200,
		DriftPPBPerSec:     0.2,
		WalkSigmaPPB:       2.0,
		WalkInterval:       engine.Ms(100),
		PhaseSigmaNs:       0.5,
		PhaseTau:           engine.Ms(10),
		FixedPhaseOffsetNs: 50,
		TimeResolutionNs:   0,
	}
}

// Oscillator is one node's free-running local clock plus servo corrections.
type Oscillator struct {
	name string
	rng  *rand.Rand
	cfg  OscConfig

	baseFreq float64 // InitialFreqPPB as fraction
	drift    float64 // DriftPPBPerSec as fraction per second
	walkPPB  float64 // accumulated random walk in ppb
	servo    float64 // servo frequency correction, fraction
	phi      float64 // current filtered phase noise, ns
	phaseAdj float64 // cumulative hard phase steps, ns

	// acc = integral of (1+f) ds over [0, accUpTo], in ns.
	acc     float64
	accUpTo engine.SimTime

	nextWalk engine.SimTime
	nextPhi  engine.SimTime
	curFreq  float64 // total frequency fraction valid over [accUpTo, nextWalk)

	generation int
}

// New builds an oscillator. rng must be node-specific.
func New(name string, rng *rand.Rand, cfg OscConfig) *Oscillator {
	o := &Oscillator{
		name:     name,
		rng:      rng,
		cfg:      cfg,
		baseFreq: cfg.InitialFreqPPB / 1e9,
		drift:    cfg.DriftPPBPerSec / 1e9,
	}
	walk := cfg.WalkInterval
	if walk <= 0 {
		walk = engine.Ms(100)
	}
	tau := cfg.PhaseTau
	if tau <= 0 {
		tau = engine.Ms(10)
	}
	o.nextWalk = walk
	o.nextPhi = tau
	o.curFreq = o.freeFreqAt(0)
	return o
}

// freeFreqAt computes the unserviced frequency fraction at true time t.
func (o *Oscillator) freeFreqAt(t engine.SimTime) float64 {
	ageSec := float64(t) / 1e9
	return o.baseFreq + o.drift*ageSec + o.walkPPB/1e9
}

// advance flushes the piecewise-constant integral up to t and refreshes the
// stochastic processes.
func (o *Oscillator) advance(t engine.SimTime) {
	for o.accUpTo < t {
		boundary := o.nextWalk
		if o.nextPhi < boundary {
			boundary = o.nextPhi
		}
		if boundary > t {
			boundary = t
		}
		dt := float64(boundary - o.accUpTo)
		o.acc += dt * (1 + o.curFreq)
		o.accUpTo = boundary

		if o.accUpTo >= o.nextPhi && o.nextPhi <= t {
			alpha := 1.0 - math.Exp(-float64(o.cfg.PhaseTau)/float64(o.cfg.PhaseTau))
			// = 1-e^-1 by construction (steps of one tau)
			white := o.rng.NormFloat64() * o.cfg.PhaseSigmaNs
			o.phi += alpha * (white - o.phi)
			o.nextPhi += o.cfg.PhaseTau
		}
		if o.accUpTo >= o.nextWalk && o.nextWalk <= t {
			o.walkPPB += o.rng.NormFloat64() * o.cfg.WalkSigmaPPB
			o.nextWalk += o.cfg.WalkInterval
			o.curFreq = o.freeFreqAt(o.accUpTo) + o.servo
		}
	}
}

// LocalAt returns the node-local clock reading (ns) at true time t.
func (o *Oscillator) LocalAt(t engine.SimTime) float64 {
	o.advance(t)
	v := o.cfg.FixedPhaseOffsetNs + o.acc + o.phi + o.phaseAdj
	if o.cfg.TimeResolutionNs > 0 {
		v = math.Round(v/o.cfg.TimeResolutionNs) * o.cfg.TimeResolutionNs
	}
	return v
}

// TrueAtLocal inverts LocalAt: the true time at which the local clock reads
// localNs. Used to schedule timers measured in local time.
func (o *Oscillator) TrueAtLocal(t engine.SimTime, localNs float64) engine.SimTime {
	o.advance(t)
	// local(t') = local(t) + (t'-t)*(1+curFreq)
	now := o.cfg.FixedPhaseOffsetNs + o.acc + o.phi + o.phaseAdj
	dLocal := localNs - now
	dt := dLocal / (1 + o.curFreq)
	return engine.SimTime(math.Round(float64(t) + dt))
}

// FreqPPB returns the current total fractional frequency in ppb.
func (o *Oscillator) FreqPPB(t engine.SimTime) float64 {
	o.advance(t)
	return o.curFreq * 1e9
}

// FreeRunFreqPPB returns the undisciplined frequency in ppb at true time t.
func (o *Oscillator) FreqFreeRunPPB(t engine.SimTime) float64 {
	o.advance(t)
	return (o.curFreq - o.servo) * 1e9
}

// AdjustFreq sets the servo frequency correction (fraction, -1e-6 = -1 ppm).
func (o *Oscillator) AdjustFreq(t engine.SimTime, frac float64) {
	o.advance(t)
	o.servo = frac
	o.curFreq = o.freeFreqAt(t) + o.servo
}

// ServoFreqPPB returns the current servo correction in ppb.
func (o *Oscillator) ServoFreqPPB() float64 { return o.servo * 1e9 }

// StepPhase applies an immediate phase correction of deltaNs nanoseconds.
func (o *Oscillator) StepPhase(t engine.SimTime, deltaNs float64) {
	o.advance(t)
	o.phaseAdj += deltaNs
	o.generation++
}

// PhaseAdjustment returns cumulative hard phase corrections.
func (o *Oscillator) PhaseAdjustment() float64 { return o.phaseAdj }

// Generation is bumped on every hard phase step / reset; in-flight
// measurements spanning a generation change must be discarded.
func (o *Oscillator) Generation() int { return o.generation }

// Reset restores the power-on state at true time t.
func (o *Oscillator) Reset(t engine.SimTime) {
	o.walkPPB = 0
	o.servo = 0
	o.phi = 0
	o.phaseAdj = 0
	o.acc = 0
	o.accUpTo = 0
	o.nextWalk = o.cfg.WalkInterval
	o.nextPhi = o.cfg.PhaseTau
	o.curFreq = o.freeFreqAt(0)
	o.generation++
}
