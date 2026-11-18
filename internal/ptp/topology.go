package ptp

import (
	"sort"

	"ptpsim/internal/clock"
	"ptpsim/internal/engine"
	"ptpsim/internal/trace"
)

// Mesh owns every simulated object for one scenario run.
type Mesh struct {
	Eng     *engine.Engine
	Rec     *trace.Recorder
	devices map[string]*Device
	links   []*Link
	// seed used for this run; stored for reports.
	Seed uint64
	// ReferenceGM forces the true-offset reference clock identity; when
	// empty the current elected grandmaster is used dynamically.
	ReferenceGM ClockIdentity
}

// NewMesh builds an empty mesh.
func NewMesh(eng *engine.Engine, rec *trace.Recorder, seed uint64) *Mesh {
	return &Mesh{
		Eng:     eng,
		Rec:     rec,
		devices: map[string]*Device{},
		Seed:    seed,
	}
}

// AddDevice registers a built device.
func (m *Mesh) AddDevice(d *Device) *Device {
	d.mes = m
	m.devices[d.Name] = d
	return d
}

// Device looks one up by name.
func (m *Mesh) Device(name string) *Device { return m.devices[name] }

// Devices returns all devices sorted by name for deterministic iteration.
func (m *Mesh) Devices() []*Device {
	out := make([]*Device, 0, len(m.devices))
	for _, d := range m.devices {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// AddLink connects two ports.
func (m *Mesh) AddLink(l *Link) { m.links = append(m.links, l) }

// Links returns the link list.
func (m *Mesh) Links() []*Link { return m.links }

// Grandmaster returns the currently active grandmaster: a powered,
// grandmaster-capable device whose BMCA selected itself (no parent), chosen
// by the same data set ordering as BMCA. Returns nil during windows with no
// elected GM.
func (m *Mesh) Grandmaster() *Device {
	var candidates []*Device
	for _, d := range m.Devices() {
		if d.PoweredOff() || !d.cfg.GMCapable || d.Type == TypeTC {
			continue
		}
		if d.parent == nil && d.hasMasterPort() {
			candidates = append(candidates, d)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		a := &AnnounceFields{
			GrandmasterPriority1:    candidates[i].def.Priority1,
			GrandmasterClockQuality: candidates[i].def.ClockQuality,
			GrandmasterPriority2:    candidates[i].def.Priority2,
			GrandmasterIdentity:     candidates[i].def.ClockIdentity,
		}
		b := &AnnounceFields{
			GrandmasterPriority1:    candidates[j].def.Priority1,
			GrandmasterClockQuality: candidates[j].def.ClockQuality,
			GrandmasterPriority2:    candidates[j].def.Priority2,
			GrandmasterIdentity:     candidates[j].def.ClockIdentity,
		}
		return CompareDatasets(a, b) < 0
	})
	return candidates[0]
}

func (d *Device) hasMasterPort() bool {
	for _, p := range d.ports {
		if p.Role() == RoleMaster {
			return true
		}
	}
	return false
}

// SlaveDevices returns OC/BC devices currently holding a Slave port.
func (m *Mesh) SlaveDevices() []*Device {
	var out []*Device
	for _, d := range m.Devices() {
		if d.slavePort != nil && !d.PoweredOff() {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SlavePort returns the device's current slave port, or nil.
func (d *Device) SlavePort() *Port { return d.slavePort }

// Parent returns the parent data set, or nil when the device is its own GM.
func (d *Device) Parent() *ParentDS { return d.parent }

// PeerDelay returns the last measured peer delay (ns) and its validity.
func (p *Port) PeerDelay() (float64, bool) { return p.peerDelayNs, p.peerDelayValid }

// CountersSnapshot returns the per-port protocol counters.
func (p *Port) CountersSnapshot() PortCounters { return p.Counters }

// LinkName returns the name of the link attached to the port, or "".
func (p *Port) LinkName() string {
	if p.link == nil {
		return ""
	}
	return p.link.cfg.Name
}

// SetFollowUpLoss toggles scenario injection of Follow_Up loss.
func (p *Port) SetFollowUpLoss(v bool) { p.forceFollowUpLoss = v }

// IsFollowUpLossForced reports the injection state.
func (p *Port) IsFollowUpLossForced() bool { return p.forceFollowUpLoss }

// TrueOffsetNs returns the actual time difference between device d's local
// clock and the active grandmaster's local clock at true time t:
//
//	trueOffset = local_d(t) - local_gm(t)
//
// measured directly from the two oscillator trajectories (no protocol data),
// which is what makes it an independent ground truth for test comparisons.
func (m *Mesh) TrueOffsetNs(t engine.SimTime, d *Device) float64 {
	gm := m.ReferenceGM
	var ref *Device
	if gm != "" {
		ref = m.devices[string(gm)]
	}
	if ref == nil {
		ref = m.Grandmaster()
	}
	if ref == nil || ref == d {
		return 0
	}
	return d.osc.LocalAt(t) - ref.osc.LocalAt(t)
}

// ServoConfig is a re-export for scenario builders.
type ServoConfig = clock.PIConfig
