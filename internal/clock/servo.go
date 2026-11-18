package clock

import (
	"math"

	"ptpsim/internal/engine"
)

// PIConfig configures the proportional-integral servo. The gains use explicit,
// dimensionally consistent units:
//
//	KP: ppb of frequency correction per ns of offset
//	KI: ppb of frequency correction per ns of offset per second of integration
type PIConfig struct {
	KP float64
	KI float64
	// MaxFreqPPB clamps the output frequency correction.
	MaxFreqPPB float64
	// StepThresholdNs: when |offset| exceeds this on the first samples (or
	// after a step) the servo applies a hard phase step instead of slewing.
	StepThresholdNs float64
	// IntegralWindupPPB clamps the integral term.
	IntegralWindupPPB float64
}

// DefaultPIConfig gives a well-damped loop for ~1 s Sync intervals with
// TCXO-grade oscillators. The gains sit in the stable region of the discrete
// phase/frequency loop (a 1 ppb frequency error accumulates 1 ns of phase per
// 1 s sample): the proportional term removes most phase error within a few
// intervals while the integral term nulls any static frequency offset,
// converging monotonically to sub-nanosecond steady-state error.
func DefaultPIConfig() PIConfig {
	return PIConfig{
		KP:                0.5,
		KI:                0.1,
		MaxFreqPPB:        100000,
		StepThresholdNs:   1000,
		IntegralWindupPPB: 50000,
	}
}

// Servo is a PI controller mapping offset samples to oscillator corrections.
// It follows the conventional PTP servo form (cf. linuxptp pi servo):
//
//	feed = KP * offset
//	integral += KI * offset * dt
//	freqAdj = -(feed + integral)         [sign: positive offset = slave fast]
type Servo struct {
	cfg       PIConfig
	integral  float64 // ppb
	lastT     engine.SimTime
	haveLast  bool
	locked    bool
	stepCount int
	slewCount int
}

// NewServo builds a servo.
func NewServo(cfg PIConfig) *Servo {
	return &Servo{cfg: cfg}
}

// Locked reports whether the servo has gone past its initial phase step.
func (s *Servo) Locked() bool { return s.locked }

// Stats returns counters for the dashboard/tests.
func (s *Servo) Stats() (steps, slews int) { return s.stepCount, s.slewCount }

// SampleResult explains what the servo did with one sample.
type SampleResult struct {
	Stepped     bool
	StepNs      float64
	FreqPPB     float64
	IntegralPPB float64
}

// Update consumes an offset measurement (slave - master, ns) at true time t
// and drives the oscillator. Oscillator frequency is adjusted in every case;
// a hard phase step is used while unlocked or for large jumps.
func (s *Servo) Update(t engine.SimTime, offsetNs float64, osc *Oscillator) SampleResult {
	res := SampleResult{}

	// Initial sample or large jump: phase step.
	if !s.haveLast || math.Abs(offsetNs) > s.cfg.StepThresholdNs {
		osc.StepPhase(t, -offsetNs)
		s.stepCount++
		s.integral = 0
		s.locked = false
		res.Stepped = true
		res.StepNs = -offsetNs
		s.lastT = t
		s.haveLast = true
		// Hold frequency correction at its current value after a step.
		res.FreqPPB = osc.ServoFreqPPB()
		res.IntegralPPB = s.integral
		return res
	}

	dt := float64(t-s.lastT) / 1e9
	s.lastT = t
	if dt <= 0 {
		dt = 1.0 / 256.0
	}

	// Both terms are computed directly in ppb. Sign convention: a positive
	// offset means the slave clock reads ahead of the master, so it must be
	// slowed — the total frequency correction is negated.
	prop := s.cfg.KP * offsetNs
	s.integral += s.cfg.KI * offsetNs * dt // ppb, accumulated over seconds
	wind := s.cfg.IntegralWindupPPB
	if wind > 0 {
		s.integral = clamp(s.integral, -wind, wind)
	}
	freqPPB := -(prop + s.integral)
	freqPPB = clamp(freqPPB, -s.cfg.MaxFreqPPB, s.cfg.MaxFreqPPB)

	osc.AdjustFreq(t, freqPPB/1e9)
	s.slewCount++
	s.locked = true
	res.FreqPPB = freqPPB
	res.IntegralPPB = s.integral
	return res
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
