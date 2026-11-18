package ptp

import (
	"math/rand/v2"

	"ptpsim/internal/engine"
	"ptpsim/internal/trace"
)

// LinkConfig describes a bidirectional link. Forward/backward delays are
// configured independently so asymmetric paths can be modelled and compensated.
type LinkConfig struct {
	Name string
	EndA *Port
	EndB *Port
	// DelayAtoB / DelayBtoA are one-way propagation delays (ns).
	DelayAtoB engine.SimTime
	DelayBtoA engine.SimTime
	// AsymmetryCompAtoB / BtoA: operator-configured compensation in ns added
	// to the correction field of event messages travelling in that
	// direction, per IEEE 1588 asymmetric link correction convention.
	AsymCompAtoB float64
	AsymCompBtoA float64
	// LossProb is the independent per-packet drop probability.
	LossProb float64
	// JitterNs is zero-mean uniform jitter added to each delivery (for
	// non-P2P background realism; PTP timestamping still uses exact times).
	JitterNs float64
	// TCForwardDelay is the per-hop processing time inside transparent
	// clocks (set by the device, stored on the port instead).
}

// Link is the simulated wire.
type Link struct {
	cfg LinkConfig
	eng *engine.Engine
	rng *rand.Rand
	rec *trace.Recorder
}

// NewLink wires two ports together.
func NewLink(eng *engine.Engine, rng *rand.Rand, rec *trace.Recorder, cfg LinkConfig) *Link {
	l := &Link{cfg: cfg, eng: eng, rng: rng, rec: rec}
	cfg.EndA.link = l
	cfg.EndB.link = l
	cfg.EndA.peer = cfg.EndB
	cfg.EndB.peer = cfg.EndA
	return l
}

// Config returns the link configuration (used by the web API).
func (l *Link) Config() LinkConfig { return l.cfg }

// EndpointNames returns the device name and port index of the A and B ends.
func (l *Link) EndpointNames() (aName string, aPort int, bName string, bPort int) {
	return l.cfg.EndA.device.Name, int(l.cfg.EndA.Index),
		l.cfg.EndB.device.Name, int(l.cfg.EndB.Index)
}

// send is invoked by a port; direction is derived from the source port.
func (l *Link) send(from *Port, msg *Message) {
	aToB := from == l.cfg.EndA
	var to *Port
	var delay engine.SimTime
	var asym float64
	if aToB {
		to = l.cfg.EndB
		delay = l.cfg.DelayAtoB
		asym = l.cfg.AsymCompAtoB
	} else {
		to = l.cfg.EndA
		delay = l.cfg.DelayBtoA
		asym = l.cfg.AsymCompBtoA
	}

	l.rec.Message(trace.MsgEvent{
		T:          l.eng.Now(),
		Kind:       trace.KindSend,
		Device:     from.device.Name,
		Port:       int(from.Index),
		PeerDev:    to.device.Name,
		PeerPort:   int(to.Index),
		MsgType:    msg.Type.String(),
		SeqID:      msg.SeqID,
		TwoStep:    msg.TwoStep,
		Correction: msg.TS.CorrectionFieldNs,
	})

	// Scenario-injected loss (e.g. missing Follow_Up) and random loss.
	if msg.Lost || (l.cfg.LossProb > 0 && l.rng.Float64() < l.cfg.LossProb) {
		d := delay
		l.eng.Schedule(d, func() {
			l.rec.Message(trace.MsgEvent{
				T: l.eng.Now(), Kind: trace.KindDrop,
				Device: to.device.Name, Port: int(to.Index),
				PeerDev: from.device.Name, PeerPort: int(from.Index),
				MsgType: msg.Type.String(), SeqID: msg.SeqID, Lost: true,
				Detail: "packet lost on link",
			})
		})
		return
	}

	jitter := engine.SimTime(0)
	if l.cfg.JitterNs > 0 {
		jitter = engine.SimTime(l.rng.Float64()*2-1) * engine.Ns(int64(l.cfg.JitterNs))
	}

	// Operator-configured asymmetry correction rides in event messages.
	if msg.IsEvent() && asym != 0 {
		msg.TS.CorrectionFieldNs += asym
	}

	l.eng.Schedule(delay+jitter, func() {
		if to.device.PoweredOff() {
			l.rec.Message(trace.MsgEvent{
				T: l.eng.Now(), Kind: trace.KindDrop,
				Device: to.device.Name, Port: int(to.Index),
				MsgType: msg.Type.String(), SeqID: msg.SeqID,
				Detail: "destination powered off",
			})
			return
		}
		// Receiver hardware timestamp in its LOCAL clock.
		msg.ReceiveTimestampNs = to.device.osc.LocalAt(l.eng.Now())
		l.rec.Message(trace.MsgEvent{
			T: l.eng.Now(), Kind: trace.KindReceive,
			Device: to.device.Name, Port: int(to.Index),
			PeerDev: from.device.Name, PeerPort: int(from.Index),
			MsgType: msg.Type.String(), SeqID: msg.SeqID, TwoStep: msg.TwoStep,
			Correction: msg.TS.CorrectionFieldNs,
		})
		to.device.deliver(to, msg)
	})
}
