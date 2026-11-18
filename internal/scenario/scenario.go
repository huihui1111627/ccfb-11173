// Package scenario defines repeatable experiment topologies and the events
// injected into them (grandmaster power loss, Follow_Up loss, asymmetric
// links). Every stochastic element is keyed off one master seed so a run is
// fully reproducible.
package scenario

import (
	"fmt"

	"ptpsim/internal/clock"
	"ptpsim/internal/engine"
	"ptpsim/internal/ptp"
	"ptpsim/internal/trace"
)

// Kind identifies a built-in topology.
type Kind string

const (
	// LinearOC: grandmaster OC chained through zero or more BCs to OC slaves.
	LinearOC Kind = "linear-oc"
	// RingBC: boundary clocks in a ring; BMCA must block the loop with a
	// Passive port (M3/P2 decisions).
	RingBC Kind = "ring-bc"
	// TransparentTC: GM -- transparent clock -- OC slaves, E2E or P2P.
	TransparentTC Kind = "transparent-tc"
)

// Event is a scheduled scenario action.
type Event struct {
	At   engine.SimTime
	Kind EventKind
	// Device / Port target the action.
	Device string
	Port   int
	Detail string
}

// EventKind enumerates injectable actions.
type EventKind string

const (
	EvPowerOffGM      EventKind = "power_off_gm"
	EvFollowUpLoss    EventKind = "followup_loss"
	EvClearFollowLoss EventKind = "clear_followup_loss"
)

// Spec fully describes one run.
type Spec struct {
	Name     string
	Kind     Kind
	Seed     uint64
	Duration engine.SimTime
	Delay    ptp.DelayMechanism
	TwoStep  bool
	// LinkDelay is the symmetric one-way delay unless overridden.
	LinkDelay engine.SimTime
	// Asymmetry: map "devA:port-devB:port" style keys -> (forward, backward).
	AsymLinks map[string]AsymLink
	// Events injected on the timeline.
	Events []Event
	// Servo / oscillator overrides (zero value = defaults).
	Servo clock.PIConfig
	// NoNoise removes random walk and phase noise for deterministic
	// theory-vs-simulation checks.
	NoNoise bool
	// RingSize / ChainDepth parameterize the topologies.
	RingSize   int
	ChainDepth int
	// LossProb applies uniform background loss to every link.
	LossProb float64
}

// AsymLink describes an asymmetric directional delay pair.
type AsymLink struct {
	Forward  engine.SimTime
	Backward engine.SimTime
	// Comp enables the correctionField compensation equal to
	// (forward-backward)/2 on master-to-slave event messages.
	Comp bool
}

// Build materializes the spec into a ready-to-run Mesh (devices powered on).
func Build(s Spec) (*ptp.Mesh, error) {
	eng := engine.New()
	rec := trace.NewRecorder(20000, 4000, 2000, 20000, 2000)
	mesh := ptp.NewMesh(eng, rec, s.Seed)
	if s.Servo == (clock.PIConfig{}) {
		s.Servo = clock.DefaultPIConfig()
	}
	switch s.Kind {
	case LinearOC:
		buildLinear(mesh, s)
	case RingBC:
		buildRing(mesh, s)
	case TransparentTC:
		buildTC(mesh, s)
	default:
		return nil, fmt.Errorf("unknown topology %q", s.Kind)
	}
	applyEvents(mesh, s)
	for _, d := range mesh.Devices() {
		d.PowerOn()
	}
	return mesh, nil
}

// oscCfg returns the oscillator config for one device, honoring NoNoise.
func oscCfg(s Spec, seedSalt uint64) clock.OscConfig {
	c := clock.DefaultOscConfig()
	// Vary static offsets/drift deterministically per device; the
	// device-dependent term must occupy the first seed position.
	r1, r2 := engine.Split(seedSalt+0x100000001b3, s.Seed, 0x5151)
	rng := engine.Rng(r1, r2)
	c.InitialFreqPPB = -800 + rng.Float64()*1600    // +/- 800 ppb
	c.FixedPhaseOffsetNs = -200 + rng.Float64()*400 // +/- 200 ns
	c.DriftPPBPerSec = -1 + rng.Float64()*2
	if s.NoNoise {
		c.WalkSigmaPPB = 0
		c.PhaseSigmaNs = 0
	}
	return c
}

func baseDevice(s Spec, name string, ports int, gmCapable bool,
	class uint8, prio1 uint8, salt uint64) ptp.DeviceConfig {
	devType := ptp.TypeOC
	if ports >= 2 {
		devType = ptp.TypeBC
	}
	return ptp.DeviceConfig{
		Name:                   name,
		Type:                   devType,
		Priority1:              prio1,
		Priority2:              128,
		ClockClass:             class,
		Accuracy:               0x21, // 100 ns
		Variance:               0x4E5,
		TwoStep:                s.TwoStep,
		Domain:                 0,
		GMCapable:              gmCapable,
		LogAnnounceInterval:    0, // 1 s
		LogSyncInterval:        0, // 1 s
		AnnounceReceiptTimeout: 3,
		Delay:                  s.Delay,
		TCForwardMin:           engine.Us(8),
		TCForwardJit:           engine.Us(4),
		Servo:                  s.Servo,
		Osc:                    oscCfg(s, salt),
		LogPDelayInterval:      0,
		PortCount:              ports,
	}
}

func seedOf(s Spec, name string, salt uint64) uint64 {
	// The name-dependent term must be in the first position: Split's first
	// return is derived from seed1 XOR the salted constant, so the varying
	// term goes into seed1 and the fixed master seed into seed2.
	a, _ := engine.Split(uint64(len(name))*7919+salt+0x100000001b3, s.Seed, 0x99)
	return a
}

func addLink(mesh *ptp.Mesh, s Spec, a *ptp.Device, pa int, b *ptp.Device, pb int, key string) {
	r1, r2 := engine.Split(uint64(len(a.Name+b.Name))*104729+uint64(pa*31+pb)+0x100000001b3,
		s.Seed, 0x77)
	cfg := ptp.LinkConfig{
		Name:      key,
		EndA:      a.Ports()[pa-1],
		EndB:      b.Ports()[pb-1],
		DelayAtoB: s.LinkDelay,
		DelayBtoA: s.LinkDelay,
		LossProb:  s.LossProb,
	}
	if al, ok := s.AsymLinks[key]; ok {
		cfg.DelayAtoB = al.Forward
		cfg.DelayBtoA = al.Backward
		if al.Comp {
			// IEEE 1588 convention: correction on A->B event messages.
			cfg.AsymCompAtoB = float64(al.Forward-al.Backward) / 2
			cfg.AsymCompBtoA = -float64(al.Forward-al.Backward) / 2
		}
	}
	mesh.AddLink(ptp.NewLink(mesh.Eng, engine.Rng(r1, r2), mesh.Rec, cfg))
}

// buildLinear: GM chained through `depth` BCs (3 ports each): p1 upstream,
// p2 downstream, p3 leaf OC slave. With depth == 0 a single OC slave hangs
// directly off the GM; the final BC gets its leaf on p2 instead.
func buildLinear(mesh *ptp.Mesh, s Spec) {
	depth := s.ChainDepth
	if depth < 0 {
		depth = 1
	}
	gm := ptp.NewDevice(mesh.Eng, mesh.Rec, seedOf(s, "gm", 1),
		baseDevice(s, "gm", 1, true, 6, 10, 1))
	mesh.AddDevice(gm)
	prev := gm
	prevUpPort := 1
	for i := 1; i <= depth; i++ {
		bc := ptp.NewDevice(mesh.Eng, mesh.Rec, seedOf(s, fmt.Sprintf("bc%d", i), 2),
			baseDevice(s, fmt.Sprintf("bc%d", i), 3, true, 248, 128, uint64(10+i)))
		mesh.AddDevice(bc)
		addLink(mesh, s, prev, prevUpPort, bc, 1,
			fmt.Sprintf("%s-%s", prev.Name, bc.Name))

		leaf := ptp.NewDevice(mesh.Eng, mesh.Rec, seedOf(s, fmt.Sprintf("slave%d", i), 3),
			baseDevice(s, fmt.Sprintf("slave%d", i), 1, false, 248, 128, uint64(100+i)))
		mesh.AddDevice(leaf)
		leafPort := 3
		if i == depth {
			leafPort = 2
		}
		addLink(mesh, s, bc, leafPort, leaf, 1, fmt.Sprintf("%s-%s", bc.Name, leaf.Name))
		prev = bc
		prevUpPort = 2
	}
	if depth == 0 {
		leaf := ptp.NewDevice(mesh.Eng, mesh.Rec, seedOf(s, "slave1", 3),
			baseDevice(s, "slave1", 1, false, 248, 128, 101))
		mesh.AddDevice(leaf)
		addLink(mesh, s, gm, 1, leaf, 1, "gm-slave1")
	}
}

// buildRing: N boundary clocks in a ring; bc0 is configured best so it wins
// the election and exactly one other port must go Passive.
func buildRing(mesh *ptp.Mesh, s Spec) {
	n := s.RingSize
	if n < 3 {
		n = 4
	}
	devs := make([]*ptp.Device, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("bc%d", i)
		prio := uint8(128)
		class := uint8(248)
		if i == 0 {
			prio, class = 10, 6
		}
		devs[i] = ptp.NewDevice(mesh.Eng, mesh.Rec, seedOf(s, name, uint64(200+i)),
			baseDevice(s, name, 2, true, class, prio, uint64(200+i)))
		mesh.AddDevice(devs[i])
	}
	for i := 0; i < n; i++ {
		a, b := devs[i], devs[(i+1)%n]
		addLink(mesh, s, a, 2, b, 1, fmt.Sprintf("%s-%s", a.Name, b.Name))
	}
}

// buildTC: GM -- chain of `depth` transparent clocks -- two OC slaves. TCs
// have 3 ports: p1 upstream, p2 downstream; the final TC fans out to two
// slaves on p2/p3. The TC never participates in BMCA.
func buildTC(mesh *ptp.Mesh, s Spec) {
	gm := ptp.NewDevice(mesh.Eng, mesh.Rec, seedOf(s, "gm", 5),
		baseDevice(s, "gm", 1, true, 6, 10, 5))
	mesh.AddDevice(gm)
	prev := gm
	prevPort := 1
	for i := 1; i <= s.ChainDepth; i++ {
		tccfg := baseDevice(s, fmt.Sprintf("tc%d", i), 3, false, 255, 255, uint64(300+i))
		tccfg.Type = ptp.TypeTC
		tc := ptp.NewDevice(mesh.Eng, mesh.Rec, seedOf(s, tccfg.Name, 6), tccfg)
		mesh.AddDevice(tc)
		addLink(mesh, s, prev, prevPort, tc, 1,
			fmt.Sprintf("%s-%s", prev.Name, tc.Name))
		prev = tc
		prevPort = 2
	}
	for i := 1; i <= 2; i++ {
		leaf := ptp.NewDevice(mesh.Eng, mesh.Rec, seedOf(s, fmt.Sprintf("slave%d", i), 7),
			baseDevice(s, fmt.Sprintf("slave%d", i), 1, false, 248, 128, uint64(400+i)))
		mesh.AddDevice(leaf)
		port := i + 1 // p2, p3 of the last TC (or gm p1 when depth == 0)
		if s.ChainDepth == 0 {
			port = 1
		}
		addLink(mesh, s, prev, port, leaf, 1, fmt.Sprintf("%s-%s", prev.Name, leaf.Name))
	}
}

// applyEvents schedules power-off / loss actions on the engine.
func applyEvents(mesh *ptp.Mesh, s Spec) {
	for _, ev := range s.Events {
		mesh.Eng.At(ev.At, func() {
			d := mesh.Device(ev.Device)
			if d == nil {
				return
			}
			switch ev.Kind {
			case EvPowerOffGM:
				d.PowerOff()
			case EvFollowUpLoss:
				if ev.Port > 0 && ev.Port <= len(d.Ports()) {
					d.Ports()[ev.Port-1].SetFollowUpLoss(true)
				}
			case EvClearFollowLoss:
				if ev.Port > 0 && ev.Port <= len(d.Ports()) {
					d.Ports()[ev.Port-1].SetFollowUpLoss(false)
				}
			}
		})
	}
}
