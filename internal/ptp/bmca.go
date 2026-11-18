package ptp

import (
	"fmt"
	"strings"

	"ptpsim/internal/clock"
	"ptpsim/internal/engine"
	"ptpsim/internal/trace"
)

// bestForeign returns the best foreign master record on a port using the
// full data set comparison of 9.3.5 b) plus the topology tie-breaks.
func bestForeign(p *Port) *EForeignMaster {
	var best *EForeignMaster
	for _, fm := range p.foreign {
		if best == nil || foreignBetter(fm, best) {
			best = fm
		}
	}
	return best
}

// foreignBetter implements the ordering:
//  1. grandmaster data set comparison (9.3.5 b) items 1..5)
//  2. stepsRemoved (item 6): the receiving port increments, so fewer wins
//  3. identity of the port that sent the Announce (item 7/8): lower wins
//  4. local receiving port number as a final deterministic tie-break
func foreignBetter(a, b *EForeignMaster) bool {
	c := CompareDatasets(a.Announce, b.Announce)
	if c != 0 {
		return c < 0
	}
	if a.Announce.StepsRemoved != b.Announce.StepsRemoved {
		return a.Announce.StepsRemoved < b.Announce.StepsRemoved
	}
	if cid := compareIdentity(a.Announce.OriginPortIdentity.Clock,
		b.Announce.OriginPortIdentity.Clock); cid != 0 {
		return cid < 0
	}
	if a.Announce.OriginPortIdentity.Port != b.Announce.OriginPortIdentity.Port {
		return a.Announce.OriginPortIdentity.Port < b.Announce.OriginPortIdentity.Port
	}
	return a.RxPort.Index < b.RxPort.Index
}

// runBMCA executes the port state characterization / state decision algorithm
// (9.2.6 / 9.3) for the device. It is invoked on Announce receipt, foreign
// timeout, and power-on.
func (d *Device) runBMCA(g int32) {
	if d.Type == TypeTC {
		return
	}
	// E_rbest: best foreign master per port.
	type candidate struct {
		port *Port
		fm   *EForeignMaster
	}
	var rbest []candidate
	for _, p := range d.ports {
		fm := bestForeign(p)
		if fm != nil {
			rbest = append(rbest, candidate{p, fm})
		}
	}
	// E_best: best across all ports.
	var global *candidate
	for i := range rbest {
		if global == nil || foreignBetter(rbest[i].fm, global.fm) {
			global = &rbest[i]
		}
	}

	localFields := &AnnounceFields{
		GrandmasterPriority1:    d.def.Priority1,
		GrandmasterClockQuality: d.def.ClockQuality,
		GrandmasterPriority2:    d.def.Priority2,
		GrandmasterIdentity:     d.def.ClockIdentity,
		StepsRemoved:            0,
	}

	var detail strings.Builder
	var newSlave *Port
	var newParent *ParentDS
	decision := ""

	switch {
	case global == nil:
		// No foreign master anywhere: a grandmaster-capable device becomes
		// grandmaster itself (M1); every port goes Master.
		if d.cfg.GMCapable {
			decision = "M1: no foreign master, device becomes grandmaster"
			d.parent = nil
			d.def.GrandmasterIdentity = d.def.ClockIdentity
			newParent = nil
			for _, p := range d.ports {
				d.promoteMaster(p, g, "M1: grandmaster port")
			}
		} else {
			decision = "M1-denied: no foreign master and not GM capable"
			for _, p := range d.ports {
				p.setRole(RoleListening, "no master heard, listening")
			}
		}
	case d.cfg.GMCapable && CompareDatasets(localFields, global.fm.Announce) < 0:
		// M2: local data set is strictly better than the best foreign one.
		decision = fmt.Sprintf("M2: local dataset beats foreign master %s",
			global.fm.Announce.GrandmasterIdentity)
		d.parent = nil
		d.def.GrandmasterIdentity = d.def.ClockIdentity
		for _, p := range d.ports {
			d.promoteMaster(p, g, "M2: superior local dataset")
		}
	case d.cfg.GMCapable && CompareDatasets(localFields, global.fm.Announce) == 0:
		// M3: the foreign Announce describes ourselves: loop of our own
		// grandmaster data. The port hearing it goes Passive.
		decision = "M3: own Announce received, breaking loop"
		for _, p := range d.ports {
			if p == global.port {
				d.demotePassive(p, "M3: own grandmaster data loop")
			} else {
				d.promoteMaster(p, g, "M3: remaining ports master")
			}
		}
	default:
		// S1: the global best is a foreign master. The port receiving it
		// becomes Slave; every other port applies P1/P2.
		decision = fmt.Sprintf("S1: synchronize to %s via port %d (stepsRemoved %d)",
			global.fm.Announce.GrandmasterIdentity, global.port.Index,
			global.fm.Announce.StepsRemoved)
		best := global.fm
		newSlave = global.port
		newParent = &ParentDS{
			GrandmasterIdentity:     best.Announce.GrandmasterIdentity,
			GrandmasterClockQuality: best.Announce.GrandmasterClockQuality,
			GrandmasterPriority1:    best.Announce.GrandmasterPriority1,
			GrandmasterPriority2:    best.Announce.GrandmasterPriority2,
			ParentPortIdentity:      best.Announce.OriginPortIdentity,
			StepsRemoved:            best.Announce.StepsRemoved + 1,
		}
		d.def.GrandmasterIdentity = best.Announce.GrandmasterIdentity
		for _, p := range d.ports {
			if p == newSlave {
				d.promoteSlave(p, g, "S1: selected slave port")
				continue
			}
			// P1: port without a foreign master that carries the same
			// grandmaster -> Master. P2: competing port hearing an equal or
			// worse grandmaster -> Passive.
			fm := bestForeign(p)
			if fm == nil {
				d.promoteMaster(p, g, "P1: downstream port becomes master")
				continue
			}
			cmp := CompareDatasets(fm.Announce, best.Announce)
			if cmp > 0 || (cmp == 0 && !foreignBetter(fm, best)) {
				d.demotePassive(p, "P2: worse/equal master on alternate path")
			} else if cmp < 0 {
				// Cannot happen: global best would have been this one.
				d.demotePassive(p, "P2: inconsistent comparison")
			} else {
				d.demotePassive(p, "P2: tie, loop prevention")
			}
		}
	}

	// Commit the parent data set / slave port and reset servo on change.
	prevSlave := d.slavePort
	d.slavePort = newSlave
	d.parent = newParent
	if newSlave != nil && prevSlave != newSlave {
		// New timing domain: invalidate in-flight measurements.
		newSlave.syncPending = false
		newSlave.delayPending = false
		d.svc = clock.NewServo(d.cfg.Servo)
	}

	ev := trace.ElectionEvent{
		T:            d.eng.Now(),
		Device:       d.Name,
		Grandmaster:  string(d.def.GrandmasterIdentity),
		GmIdentity:   string(d.def.GrandmasterIdentity),
		StepsRemoved: 0,
		Decision:     decision,
	}
	if d.parent != nil {
		ev.StepsRemoved = int(d.parent.StepsRemoved)
		ev.BestSource = string(d.parent.ParentPortIdentity.Clock)
	} else {
		ev.BestSource = "self"
	}
	detail.WriteString(decision)
	for _, c := range rbest {
		fmt.Fprintf(&detail, " | port %d hears %s steps=%d",
			c.port.Index, c.fm.Announce.GrandmasterIdentity,
			c.fm.Announce.StepsRemoved)
	}
	ev.Detail = detail.String()
	d.lastElection = ev
	d.rec.Election(ev)
}

// promoteMaster drives a port toward Master via PreMaster/Uncalibrated per the
// state machine and arms the Announce/Sync timers.
func (d *Device) promoteMaster(p *Port, g int32, reason string) {
	role := p.Role()
	if role == RoleMaster {
		return
	}
	p.foreign = nil
	p.Counters.ForeignMaster = 0
	if role == RoleSlave {
		// Leaving slave: brief Uncalibrated then Master.
		p.setRole(RoleUncalibrated, reason)
		d.eng.Schedule(engine.Ms(20), func() {
			if !d.alive(g) || p.Role() != RoleUncalibrated {
				return
			}
			p.setRole(RolePreMaster, "qualifying master timing")
			d.eng.Schedule(engine.Ms(20), func() {
				if !d.alive(g) || p.Role() != RolePreMaster {
					return
				}
				p.setRole(RoleMaster, reason)
				d.armMasterTimers(p, g)
			})
		})
		return
	}
	p.setRole(RolePreMaster, reason)
	d.eng.Schedule(engine.Ms(20), func() {
		if !d.alive(g) || p.Role() != RolePreMaster {
			return
		}
		p.setRole(RoleMaster, reason)
		d.armMasterTimers(p, g)
	})
}

func (d *Device) promoteSlave(p *Port, g int32, reason string) {
	if p.Role() != RoleSlave {
		p.setRole(RoleUncalibrated, reason)
		d.eng.Schedule(engine.Ms(20), func() {
			if !d.alive(g) || p.Role() != RoleUncalibrated {
				return
			}
			p.setRole(RoleSlave, reason)
			d.armSlaveTimers(p, g)
		})
		return
	}
	d.armSlaveTimers(p, g)
}

func (d *Device) demotePassive(p *Port, reason string) {
	if p.Role() == RolePassive {
		return
	}
	p.syncPending = false
	p.delayPending = false
	p.setRole(RolePassive, reason)
}

func (d *Device) armMasterTimers(p *Port, g int32) {
	if p.syncTimerArmed || p.annTimerArmed {
		return
	}
	p.annTimerArmed = true
	p.syncTimerArmed = true
	d.eng.Schedule(0, func() { d.tickAnnounce(p, g) })
	d.eng.Schedule(engine.Ms(5), func() { d.tickSync(p, g) })
}

func (d *Device) armSlaveTimers(p *Port, g int32) {
	// Refresh timeout and start delay requests / peer delay as needed.
	d.scheduleAnnounceTimeout(p, g)
	if d.cfg.Delay == DelayP2P {
		if !p.pdelayTimerArmed {
			p.pdelayTimerArmed = true
			d.schedulePDelay(p, g)
		}
	} else {
		if !p.delayTimerArmed {
			p.delayTimerArmed = true
			d.scheduleDelayReq(p, g)
		}
	}
}
