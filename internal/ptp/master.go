package ptp

import (
	"fmt"

	"ptpsim/internal/engine"
	"ptpsim/internal/trace"
)

// scheduleLocal schedules cb after a local-time duration; the deadline is
// mapped through the oscillator so a fast/slow clock fires timers at the
// correct true time.
func (d *Device) scheduleLocal(p *Port, dur engine.SimTime, g int32, cb func(p *Port, g int32)) {
	now := d.eng.Now()
	deadline := d.osc.TrueAtLocal(now, d.osc.LocalAt(now)+float64(dur))
	d.eng.At(deadline, func() {
		if !d.alive(g) {
			return
		}
		cb(p, g)
	})
}

// tickAnnounce transmits one Announce and re-arms.
func (d *Device) tickAnnounce(p *Port, g int32) {
	if p.Role() != RoleMaster {
		return
	}
	p.annSeq++
	var steps uint16
	if d.parent != nil {
		steps = d.parent.StepsRemoved
	}
	fields := LocalAnnounce(&d.def, d.parent, steps)
	fields.OriginPortIdentity = PortIdentity{Clock: d.Identity(), Port: p.Index}
	msg := &Message{
		Type:     MsgAnnounce,
		SeqID:    p.annSeq,
		Announce: fields,
		TwoStep:  d.def.TwoStep,
	}
	p.Counters.AnnounceTx++
	d.send(p, msg)
	d.scheduleLocal(p, intervalDuration(d.cfg.LogAnnounceInterval), g, d.tickAnnounce)
}

// tickSync transmits Sync (and Follow_Up in two-step mode) and re-arms.
func (d *Device) tickSync(p *Port, g int32) {
	if p.Role() != RoleMaster {
		return
	}
	p.syncSeq++
	seq := p.syncSeq
	sync := &Message{
		Type:    MsgSync,
		SeqID:   seq,
		TwoStep: d.def.TwoStep,
	}
	p.Counters.SyncTx++
	d.send(p, sync)

	if d.def.TwoStep {
		// The Follow_Up carries the precise hardware egress timestamp of the
		// Sync event message. Scenario injection can force its loss to
		// exercise two-step failure handling.
		egressLocal := sync.TS.OriginTimestampNs
		follow := &Message{
			Type:    MsgFollowUp,
			SeqID:   seq,
			TwoStep: true,
			TS: TSF{
				PreciseOriginTimestampNs: egressLocal,
				CorrectionFieldNs:        sync.TS.CorrectionFieldNs,
			},
		}
		follow.Lost = p.forceFollowUpLoss
		p.Counters.FollowTx++
		// Follow_Up is a general message: sent a hair after the event.
		d.eng.Schedule(engine.Us(50), func() {
			if !d.alive(g) || p.Role() != RoleMaster {
				return
			}
			d.send(p, follow)
		})
	}
	d.scheduleLocal(p, intervalDuration(d.cfg.LogSyncInterval), g, d.tickSync)
}

// scheduleDelayReq (E2E slave): periodically issue Delay_Req.
func (d *Device) scheduleDelayReq(p *Port, g int32) {
	d.scheduleLocal(p, intervalDuration(d.cfg.LogSyncInterval), g, d.tickDelayReq)
}

func (d *Device) tickDelayReq(p *Port, g int32) {
	if p.Role() != RoleSlave || d.cfg.Delay != DelayE2E {
		return
	}
	// Re-arm first so the chain can never stall, even if a request goes
	// unanswered and the pending flag has to time out.
	d.scheduleLocal(p, intervalDuration(d.cfg.LogSyncInterval), g, d.tickDelayReq)
	if p.delayPending {
		// Previous request unanswered: declare it lost after one interval and
		// clear the gate so this round can start a fresh exchange.
		p.delayPending = false
		d.rec.Generic(trace.GenericEvent{
			T: d.eng.Now(), Device: d.Name, What: "delayreq_timeout",
			Detail: fmt.Sprintf("port %d Delay_Req unanswered, retrying", p.Index),
		})
	}
	p.delaySeq++
	msg := &Message{Type: MsgDelayReq, SeqID: p.delaySeq, TwoStep: d.def.TwoStep}
	p.delayPending = true
	p.delayGen = d.osc.Generation()
	p.Counters.DelayReqTx++
	d.send(p, msg)
}

// schedulePDelay (P2P): periodic Pdelay_Req on any port (slave or TC).
func (d *Device) schedulePDelay(p *Port, g int32) {
	d.scheduleLocal(p, intervalDuration(d.cfg.LogPDelayInterval), g, d.tickPDelay)
}

func (d *Device) tickPDelay(p *Port, g int32) {
	// Always re-arm so a lost Pdelay_Req cannot stop the peer-delay chain.
	d.scheduleLocal(p, intervalDuration(d.cfg.LogPDelayInterval), g, d.tickPDelay)
	if !p.portRunsPdelay() {
		return
	}
	if p.pdelayPending {
		// Earlier exchange never completed; time it out and start over.
		p.pdelayPending = false
		d.rec.Generic(trace.GenericEvent{
			T: d.eng.Now(), Device: d.Name, What: "pdelay_timeout",
			Detail: fmt.Sprintf("port %d peer-delay exchange timed out", p.Index),
		})
	}
	p.pdelaySeq++
	msg := &Message{Type: MsgPDelayReq, SeqID: p.pdelaySeq}
	p.pdelayPending = true
	p.Counters.PdelayTx++
	d.send(p, msg)
}

// portRunsPdelay says whether the port participates in peer delay.
func (p *Port) portRunsPdelay() bool {
	if p.device.Type == TypeTC {
		return p.device.cfg.Delay == DelayP2P
	}
	return p.device.cfg.Delay == DelayP2P && (p.Role() == RoleSlave || p.Role() == RoleMaster)
}
