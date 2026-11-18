package ptp

import (
	"fmt"

	"ptpsim/internal/engine"
	"ptpsim/internal/trace"
)

// deliver is the device-level ingress: transparent clocks forward, every
// other device runs the protocol logic.
func (d *Device) deliver(p *Port, msg *Message) {
	if d.Type == TypeTC {
		d.tcForward(p, msg)
		return
	}
	d.handleProtocol(p, msg)
}

// ---------------------------------------------------------------------------
// Transparent clock (16.4 / 16.5)
// ---------------------------------------------------------------------------

func (d *Device) tcForward(in *Port, msg *Message) {
	if in.peer == nil || in.link == nil {
		return
	}
	out := d.otherPort(in)
	if out == nil || out.peer == nil {
		return
	}
	// P2P peer-delay messages terminate at the TC port: they measure the
	// single link, they are never forwarded.
	if msg.Type == MsgPDelayReq || msg.Type == MsgPDelayResp ||
		msg.Type == MsgPDelayRespFollowUp {
		d.handlePDelay(in, msg)
		return
	}

	if msg.IsEvent() {
		msg.IngressTrue = d.eng.Now()
		msg.IngressLocal = msg.ReceiveTimestampNs
	}
	fwd := d.cfg.TCForwardMin
	if d.cfg.TCForwardJit > 0 {
		fwd += engine.SimTime(d.rng.Int64N(int64(d.cfg.TCForwardJit)))
	}
	d.eng.Schedule(fwd, func() {
		if d.PoweredOff() {
			return
		}
		if msg.IsEvent() {
			msg.EgressTrue = d.eng.Now()
			msg.EgressLocal = d.osc.LocalAt(d.eng.Now())
			// Residence measured in the TC local time scale (16.4.2): any
			// frequency error of the TC oscillator appears in the measured
			// residence and therefore inside correctionField, exactly as a
			// real hardware TC would produce.
			residence := msg.EgressLocal - msg.IngressLocal
			// P2P transparent clock additionally corrects for the ingress
			// link's peer delay (16.5.2).
			peerCorr := 0.0
			if d.cfg.Delay == DelayP2P && in.peerDelayValid {
				peerCorr = in.peerDelayNs
			}
			added := residence + peerCorr
			if msg.Type == MsgSync && msg.TwoStep {
				// Two-step: the Sync carries no usable correction for the
				// slave; hold the accumulated value for the Follow_Up.
				if d.tcFollowCorrection == nil {
					d.tcFollowCorrection = map[uint16]float64{}
				}
				d.tcFollowCorrection[msg.SeqID] = added
			} else {
				msg.TS.CorrectionFieldNs += added
			}
			msg.Hops = append(msg.Hops, HopInfo{
				Device:       d.Name,
				ResidenceNs:  residence,
				CorrectionNs: msg.TS.CorrectionFieldNs + added,
			})
			d.rec.Message(trace.MsgEvent{
				T: d.eng.Now(), Kind: trace.KindCorrected,
				Device: d.Name, Port: int(in.Index),
				MsgType: msg.Type.String(), SeqID: msg.SeqID,
				Correction: msg.TS.CorrectionFieldNs + added, Residence: residence,
				Detail: fmt.Sprintf("TC residence %.3f ns accumulated", residence),
			})
		}
		if msg.Type == MsgFollowUp {
			// Inject the correction held for the matching two-step Sync.
			if held, ok := d.tcFollowCorrection[msg.SeqID]; ok {
				msg.TS.CorrectionFieldNs += held
				delete(d.tcFollowCorrection, msg.SeqID)
			}
		}
		d.send(out, msg)
	})
}

func (d *Device) otherPort(p *Port) *Port {
	for _, q := range d.ports {
		if q != p {
			return q
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// BC / OC protocol processing
// ---------------------------------------------------------------------------

func (d *Device) handleProtocol(p *Port, msg *Message) {
	switch msg.Type {
	case MsgAnnounce:
		d.handleAnnounce(p, msg)
	case MsgSync:
		d.handleSync(p, msg)
	case MsgFollowUp:
		d.handleFollowUp(p, msg)
	case MsgDelayReq:
		d.handleDelayReq(p, msg)
	case MsgDelayResp:
		d.handleDelayResp(p, msg)
	case MsgPDelayReq:
		d.handlePDelay(p, msg)
	case MsgPDelayResp, MsgPDelayRespFollowUp:
		d.handlePDelay(p, msg)
	}
}

// -- Announce / foreign master list (9.5.2) --------------------------------

func (d *Device) handleAnnounce(p *Port, msg *Message) {
	if msg.Announce == nil {
		return
	}
	p.Counters.AnnounceRx++
	// Ignore our own reflected Announce.
	if msg.Announce.GrandmasterIdentity == d.Identity() &&
		msg.Announce.OriginPortIdentity.Clock == d.Identity() {
		return
	}
	// Qualify / refresh the foreign master entry.
	var fm *EForeignMaster
	for _, e := range p.foreign {
		if e.PortID == msg.Announce.OriginPortIdentity {
			fm = e
			break
		}
	}
	if fm == nil {
		fm = &EForeignMaster{Announce: msg.Announce, PortID: msg.Announce.OriginPortIdentity, RxPort: p}
		p.foreign = append(p.foreign, fm)
	}
	fm.Announce = msg.Announce
	fm.Count++
	fm.LastSeen = d.eng.Now()
	p.Counters.ForeignMaster = len(p.foreign)

	// Re-arm the Announce receipt timeout on every accepted message.
	g := d.gen.Load()
	d.scheduleAnnounceTimeout(p, g)
	d.runBMCA(g)
}

// -- Sync / Follow_Up ------------------------------------------------------

func (d *Device) handleSync(p *Port, msg *Message) {
	if p.Role() != RoleSlave {
		return
	}
	p.Counters.SyncRx++
	t2 := msg.ReceiveTimestampNs
	if !msg.TwoStep {
		// One-step: precise origin is in the Sync itself.
		d.onSyncComplete(p, msg, msg.TS.OriginTimestampNs, t2, msg.TS.CorrectionFieldNs)
		return
	}
	// Two-step: park t2 and wait for the matching Follow_Up.
	p.syncPending = true
	p.syncSeqPending = msg.SeqID
	p.syncT2 = t2
	p.syncGen = d.osc.Generation()
}

func (d *Device) handleFollowUp(p *Port, msg *Message) {
	if p.Role() != RoleSlave {
		return
	}
	p.Counters.FollowRx++
	// Rule: if the Follow_Up for the outstanding two-step Sync is missing
	// (loss injected upstream, or an unexpected sequence), the whole
	// measurement round is discarded. A stale/duplicate Follow_Up likewise
	// invalidates the parked Sync.
	if !p.syncPending || msg.SeqID != p.syncSeqPending {
		d.rec.Generic(trace.GenericEvent{
			T: d.eng.Now(), Device: d.Name,
			What: "followup_mismatch",
			Detail: fmt.Sprintf("port %d seq %d: no matching Sync, round discarded",
				p.Index, msg.SeqID),
		})
		p.syncPending = false
		return
	}
	if msg.Lost {
		// Defensive: lost messages are normally dropped on the wire.
		p.syncPending = false
		return
	}
	p.syncPending = false
	if d.osc.Generation() != p.syncGen {
		// Clock was hard-stepped mid-round: invalidate.
		return
	}
	d.onSyncComplete(p, msg, msg.TS.PreciseOriginTimestampNs, p.syncT2,
		msg.TS.CorrectionFieldNs)
}

// syncState holds the master-side timestamp pair awaiting delay data.
type syncState struct {
	t1         float64 // master precise origin (master time)
	t2         float64 // slave receive (slave time)
	correction float64 // accumulated correctionField
	seq        uint16
	gen        int
}

// onSyncComplete records the Sync tuple; E2E slaves wait for Delay_Resp,
// P2P slaves can compute the offset immediately using peer delay.
func (d *Device) onSyncComplete(p *Port, msg *Message, t1, t2, correction float64) {
	st := syncState{t1: t1, t2: t2, correction: correction, seq: msg.SeqID,
		gen: d.osc.Generation()}
	if d.cfg.Delay == DelayP2P {
		if !p.peerDelayValid {
			return
		}
		d.applyServo(p, st, p.peerDelayNs)
		return
	}
	// E2E: park until the next Delay_Resp.
	p.pendingSync = &st
}

// -- E2E Delay_Req / Delay_Resp --------------------------------------------

func (d *Device) handleDelayReq(p *Port, msg *Message) {
	// Only a Master port answers (ordinary/boundary clock behavior).
	if p.Role() != RoleMaster {
		return
	}
	t4 := msg.ReceiveTimestampNs
	resp := &Message{
		Type:    MsgDelayResp,
		SeqID:   msg.SeqID,
		TwoStep: d.def.TwoStep,
		Target:  msg.Source,
		TS: TSF{
			// Responder receive timestamp t4 in its own time scale.
			OriginTimestampNs: t4,
			CorrectionFieldNs: msg.TS.CorrectionFieldNs,
		},
	}
	d.eng.Schedule(engine.Us(50), func() {
		if d.PoweredOff() || p.Role() != RoleMaster {
			return
		}
		d.send(p, resp)
	})
}

func (d *Device) handleDelayResp(p *Port, msg *Message) {
	if p.Role() != RoleSlave || d.cfg.Delay != DelayE2E {
		return
	}
	p.Counters.DelayRespRx++
	if !p.delayPending {
		return
	}
	p.delayPending = false
	if p.pendingSync == nil {
		return
	}
	if d.osc.Generation() != p.pendingSync.gen || d.osc.Generation() != p.delayGen {
		p.pendingSync = nil
		return
	}
	st := p.pendingSync
	p.pendingSync = nil

	// t3: our Delay_Req egress in slave local time.
	// t4: master receive timestamp carried in the response.
	t3 := p.delayTxLocal
	t4 := msg.TS.OriginTimestampNs
	// st.correction is the master->slave correctionField (TC residence plus
	// the configured asymmetry correction). Delay_Resp correction carries
	// the slave->master direction and is applied to meanPath only.
	corrMS := st.correction
	corrSM := msg.TS.CorrectionFieldNs

	// IEEE 1588 11.3. With an asymmetric link the raw E2E mean path delay
	// is (dMS + dSM)/2; the per-direction operator correction (asym) is
	// removed from each leg before averaging, so an explicitly compensated
	// link yields the symmetric component and offsetFromMaster loses its
	// (dMS-dSM)/2 systematic bias.
	rawMS := (t4 - st.t1) - corrMS
	rawSM := (t3 - st.t2) + corrSM
	meanPath := (rawMS - rawSM) / 2
	offset := (st.t2 - st.t1) - corrMS - meanPath
	d.recordAndServo(p, *st, offset, meanPath)
}

// -- P2P peer delay (11.4) --------------------------------------------------

func (d *Device) handlePDelay(p *Port, msg *Message) {
	switch msg.Type {
	case MsgPDelayReq:
		// Responder: capture t2 (local ingress), answer Pdelay_Resp and then
		// Pdelay_Resp_Follow_Up carrying the precise ingress timestamp t2.
		t2 := msg.ReceiveTimestampNs
		t1req := msg.TS.OriginTimestampNs
		resp := &Message{
			Type:   MsgPDelayResp,
			SeqID:  msg.SeqID,
			Target: msg.Source,
			TS: TSF{
				// t3' (response egress) is stamped in send(); the origin
				// field here stays zero until then.
				CorrectionFieldNs: msg.TS.CorrectionFieldNs,
			},
			// Echo the requester's t1 so it can correlate the exchange.
			RequestIngressTimestampNs: t1req,
		}
		// Keep t2 attached for the Follow_Up.
		d.eng.Schedule(engine.Us(50), func() {
			if d.PoweredOff() {
				return
			}
			d.send(p, resp)
			fu := &Message{
				Type:   MsgPDelayRespFollowUp,
				SeqID:  msg.SeqID,
				Target: msg.Source,
				TS: TSF{
					PreciseOriginTimestampNs: t2,
					CorrectionFieldNs:        msg.TS.CorrectionFieldNs,
				},
			}
			d.eng.Schedule(engine.Us(50), func() {
				if d.PoweredOff() {
					return
				}
				d.send(p, fu)
			})
		})
		p.Counters.PdelayRx++

	case MsgPDelayResp:
		// Requester: t4 = our receive timestamp of Pdelay_Resp; t3' is the
		// responder egress timestamp captured at actual send.
		if !p.pdelayPending {
			return
		}
		p.pdelayT4 = msg.ReceiveTimestampNs
		p.pdelayResponderT3 = msg.RequestEgressTimestampNs
		p.pdelayT1 = p.pdelayTxLocal
		p.Counters.PdelayRx++

	case MsgPDelayRespFollowUp:
		if !p.pdelayPending {
			return
		}
		p.pdelayPending = false
		if d.osc.Generation() != p.pdelayGen {
			return
		}
		t1 := p.pdelayT1
		t2 := msg.TS.PreciseOriginTimestampNs
		t3 := p.pdelayResponderT3
		t4 := p.pdelayT4
		// 11.4: peerMeanPathDelay.
		peerDelay := ((t4 - t1) - (t3 - t2)) / 2
		p.peerDelayNs = peerDelay
		p.peerDelayValid = true
		p.neighbor = msg.Source
		// Re-arm the next peer delay exchange (requester side; the
		// responder re-arms on Pdelay_Req ingress via its own timer).
		g := d.gen.Load()
		d.schedulePDelay(p, g)
	}
}

// applyServo is the P2P path: offset computed straight from the Sync tuple.
func (d *Device) applyServo(p *Port, st syncState, peerDelay float64) {
	// correctionField already carries TC residence + ingress peer delays.
	offset := (st.t2 - st.t1) - st.correction - peerDelay
	d.recordAndServo(p, st, offset, peerDelay)
}

// recordAndServo feeds the PI controller and emits the convergence sample.
func (d *Device) recordAndServo(p *Port, st syncState, offset, meanPath float64) {
	now := d.eng.Now()
	res := d.svc.Update(now, offset, d.osc)
	// True offset: difference between our local clock and the grandmaster's
	// time, measured directly from the two oscillator trajectories (no
	// protocol data), which is the independent ground truth used by tests
	// and the theoretical comparison.
	trueOffset := d.mes.TrueOffsetNs(now, d)
	d.rec.Offset(trace.OffsetSample{
		T:            now,
		Device:       d.Name,
		OffsetNs:     offset,
		TrueOffsetNs: trueOffset,
		FreqPPB:      d.osc.ServoFreqPPB(),
		MeanPathNs:   meanPath,
	})
	if res.Stepped {
		d.rec.Generic(trace.GenericEvent{
			T: now, Device: d.Name, What: "servo_step",
			Detail: fmt.Sprintf("phase step %.3f ns", res.StepNs),
		})
	}
}
