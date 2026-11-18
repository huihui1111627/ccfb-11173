package ptp

import (
	"fmt"
	"math/rand/v2"
	"sync/atomic"

	"ptpsim/internal/clock"
	"ptpsim/internal/engine"
	"ptpsim/internal/trace"
)

// DeviceConfig builds one simulated node.
type DeviceConfig struct {
	Name       string
	Type       DeviceType
	Priority1  uint8
	Priority2  uint8
	ClockClass uint8
	Accuracy   uint8
	Variance   uint16
	TwoStep    bool
	Domain     uint8
	// GranmasterCapable: a TC is never capable; OC/BC normally are.
	GMCapable bool
	// LogAnnounceInterval / LogSyncInterval are base-2 logs of seconds
	// (0 -> 1 s, -3 -> 125 ms).
	LogAnnounceInterval    int
	LogSyncInterval        int
	AnnounceReceiptTimeout uint8
	// Delay mechanism for non-TC ports.
	Delay DelayMechanism
	// TC residence parameters.
	TCForwardMin engine.SimTime
	TCForwardJit engine.SimTime
	// Servo configuration for slave-capable devices.
	Servo clock.PIConfig
	// Oscillator configuration.
	Osc clock.OscConfig
	// Peer delay interval (P2P), log2 seconds.
	LogPDelayInterval int
	// Ports numbering starts at 1.
	PortCount int
}

// Port is one interface of a device.
type Port struct {
	Index  uint16
	device *Device
	link   *Link
	peer   *Port

	role atomic.Value // PortRole

	// Foreign master data set (E2E BMCA); P2P ports keep it too since BMCA
	// runs independently of the delay mechanism.
	foreign []*EForeignMaster

	// Timer bookkeeping (true times).
	announceTimeout  engine.SimTime
	syncDeadline     engine.SimTime
	announceDeadline engine.SimTime
	pdelayDeadline   engine.SimTime

	// Last transmitted sequence numbers.
	syncSeq   uint16
	annSeq    uint16
	delaySeq  uint16
	pdelaySeq uint16

	// P2P peer delay state.
	peerDelayNs       float64
	peerDelayValid    bool
	pdelayPending     bool
	pdelayTxLocal     float64 // local t1 of the outstanding Pdelay_Req
	pdelayT1          float64 // t1 captured at request egress
	pdelayT4          float64 // local receive timestamp of Pdelay_Resp
	pdelayResponderT3 float64 // egress timestamp of Pdelay_Resp at neighbor
	pdelayGen         int
	// Peering neighbor identity.
	neighbor PortIdentity

	// E2E delay request state.
	delayPending bool
	delayTxLocal float64 // t1 local
	delayGen     int

	// Timer-arm guards: BMCA runs on every Announce, but each periodic
	// message chain must be started exactly once per port role episode.
	delayTimerArmed  bool
	pdelayTimerArmed bool
	annTimerArmed    bool
	syncTimerArmed   bool

	// Outstanding two-step Sync state.
	syncPending    bool
	syncSeqPending uint16
	syncT2         float64 // local receive timestamp of Sync
	syncGen        int
	// pendingSync holds the completed Sync tuple awaiting E2E Delay_Resp.
	pendingSync *syncState
	// follow-up loss injection for the configured sequence (scenario use).
	forceFollowUpLoss bool

	// Counters for the dashboard.
	Counters PortCounters
}

// PortCounters exposes per-port protocol counters.
type PortCounters struct {
	SyncTx        int64
	SyncRx        int64
	FollowTx      int64
	FollowRx      int64
	AnnounceTx    int64
	AnnounceRx    int64
	DelayReqTx    int64
	DelayRespRx   int64
	PdelayTx      int64
	PdelayRx      int64
	ForeignMaster int
	StateChanges  int64
}

// Role returns the current port role.
func (p *Port) Role() PortRole {
	v := p.role.Load()
	if v == nil {
		return RoleInitializing
	}
	return v.(PortRole)
}

func (p *Port) setRole(r PortRole, reason string) {
	old := p.Role()
	if old == r {
		return
	}
	// Leaving a role episode: its periodic timer chains belong to the old
	// role and must not keep firing or be reused by the next episode.
	if old == RoleMaster || old == RoleSlave {
		p.annTimerArmed = false
		p.syncTimerArmed = false
		p.delayTimerArmed = false
		p.pdelayTimerArmed = false
	}
	p.role.Store(r)
	p.Counters.StateChanges++
	p.device.rec.PortState(trace.PortStateEvent{
		T: p.device.eng.Now(), Device: p.device.Name, Port: int(p.Index),
		From: string(old), To: string(r), Reason: reason,
	})
}

// Device is a simulated PTP node.
type Device struct {
	Name string
	Type DeviceType
	cfg  DeviceConfig

	eng *engine.Engine
	rng *rand.Rand
	rec *trace.Recorder
	osc *clock.Oscillator
	svc *clock.Servo

	def    DefaultDS
	parent *ParentDS
	ports  []*Port

	powered atomic.Bool
	// generation bumped at power off; stale timer callbacks become no-ops.
	gen atomic.Int32

	// election history detail for UI.
	lastElection trace.ElectionEvent

	// Slave port pointer (nil when this device is not synchronized).
	slavePort *Port

	// Two-step transparent-clock bookkeeping: correction accumulated while
	// a Sync event message transited the device, keyed by sequence ID, to be
	// injected into the matching Follow_Up general message (16.4.3). A TC
	// never timestamps Follow_Up itself.
	tcFollowCorrection map[uint16]float64

	// mes back-reference for ground-truth offset measurements.
	mes *Mesh
}

// NewDevice constructs a device and its ports (ports start Initializing).
func NewDevice(eng *engine.Engine, rec *trace.Recorder, seed uint64, cfg DeviceConfig) *Device {
	id := ClockIdentity(fmt.Sprintf("%016x", seed&0x7fffffffffffffff))
	d := &Device{
		Name: cfg.Name,
		Type: cfg.Type,
		cfg:  cfg,
		eng:  eng,
		rec:  rec,
		def: DefaultDS{
			ClockIdentity: id,
			ClockQuality: ClockQuality{
				ClockClass:              cfg.ClockClass,
				ClockAccuracy:           cfg.Accuracy,
				OffsetScaledLogVariance: cfg.Variance,
			},
			Priority1: cfg.Priority1,
			Priority2: cfg.Priority2,
			TwoStep:   cfg.TwoStep,
		},
	}
	r1, r2 := engine.Split(seed, 0x7070707070707070, 0xA5A5)
	d.rng = engine.Rng(r1, r2)
	d.osc = clock.New(cfg.Name, engine.Rng(engine.Split(seed, 0x1234, 0x01)), cfg.Osc)
	d.svc = clock.NewServo(cfg.Servo)
	for i := 1; i <= cfg.PortCount; i++ {
		p := &Port{Index: uint16(i), device: d}
		p.role.Store(RoleInitializing)
		d.ports = append(d.ports, p)
	}
	return d
}

// Identity returns the clock identity.
func (d *Device) Identity() ClockIdentity { return d.def.ClockIdentity }

// Config returns the static device configuration.
func (d *Device) Config() DeviceConfig { return d.cfg }

// Ports exposes the port list.
func (d *Device) Ports() []*Port { return d.ports }

// Oscillator / servo accessors.
func (d *Device) Osc() *clock.Oscillator { return d.osc }
func (d *Device) Servo() *clock.Servo    { return d.svc }

// PoweredOff reports the power state.
func (d *Device) PoweredOff() bool { return !d.powered.Load() }

// intervalSeconds converts a log2 interval to seconds (13.1 of IEEE 1588).
func intervalSeconds(log int) float64 {
	if log >= 0 {
		return float64(uint64(1) << uint(log))
	}
	return 1.0 / float64(uint64(1)<<uint(-log))
}

func intervalDuration(log int) engine.SimTime {
	s := intervalSeconds(log)
	return engine.Sec(s)
}

// PowerOn starts the protocol stack: TCs begin forwarding immediately;
// OC/BC ports enter Listening and the BMCA timers start.
func (d *Device) PowerOn() {
	g := d.gen.Load()
	d.powered.Store(true)
	d.rec.Generic(trace.GenericEvent{T: d.eng.Now(), Device: d.Name, What: "power_on"})
	if d.Type == TypeTC {
		for _, p := range d.ports {
			p.setRole(RoleMaster, "transparent clock: forwarding (ports shown as Master)")
		}
		// P2P transparent clocks run peer delay on every port.
		if d.cfg.Delay == DelayP2P {
			for _, p := range d.ports {
				d.schedulePDelay(p, g)
			}
		}
		return
	}
	for _, p := range d.ports {
		p.foreign = nil
		p.setRole(RoleListening, "power on: waiting for Announce")
		p.announceTimeout = 0
		d.scheduleAnnounceTimeout(p, g)
	}
	// Re-run BMCA once immediately so a lone grandmaster-class device
	// promotes itself without waiting for a timeout.
	d.runBMCA(g)
}

// PowerOff stops all protocol activity; scheduled callbacks check the
// generation and become no-ops.
func (d *Device) PowerOff() {
	d.gen.Add(1)
	d.powered.Store(false)
	d.slavePort = nil
	d.parent = nil
	for _, p := range d.ports {
		p.setRole(RoleFaulty, "power off")
		p.foreign = nil
		p.syncPending = false
		p.delayPending = false
		p.pdelayPending = false
		p.annTimerArmed = false
		p.syncTimerArmed = false
		p.delayTimerArmed = false
		p.pdelayTimerArmed = false
	}
	d.rec.Generic(trace.GenericEvent{T: d.eng.Now(), Device: d.Name, What: "power_off"})
}

func (d *Device) alive(g int32) bool {
	return d.powered.Load() && d.gen.Load() == g
}

// scheduleAnnounceTimeout arms the Announce receipt timeout (9.2.6.11):
// announceReceiptTimeout * announceInterval, measured in the port's local
// clock. We compute the true time corresponding to that local deadline.
func (d *Device) scheduleAnnounceTimeout(p *Port, g int32) {
	interval := intervalDuration(d.cfg.LogAnnounceInterval)
	timeout := interval * engine.SimTime(d.cfg.AnnounceReceiptTimeout)
	nowLocal := d.osc.LocalAt(d.eng.Now())
	deadlineLocal := nowLocal + float64(timeout)
	trueDeadline := d.osc.TrueAtLocal(d.eng.Now(), deadlineLocal)
	p.announceTimeout = trueDeadline
	d.eng.At(trueDeadline, func() {
		if !d.alive(g) {
			return
		}
		if d.eng.Now() < p.announceTimeout {
			return // re-armed
		}
		// Timeout: all foreign masters expire, re-elect.
		if len(p.foreign) > 0 {
			d.rec.Generic(trace.GenericEvent{
				T: d.eng.Now(), Device: d.Name,
				What:   "announce_timeout",
				Detail: fmt.Sprintf("port %d foreign masters expired", p.Index),
			})
		}
		p.foreign = nil
		p.Counters.ForeignMaster = 0
		d.runBMCA(g)
	})
}

// send emits a message through the port's link.
func (d *Device) send(p *Port, msg *Message) {
	if p.link == nil {
		return
	}
	msg.Source = PortIdentity{Clock: d.Identity(), Port: p.Index}
	msg.Domain = d.cfg.Domain
	// Hardware egress timestamp for event messages in the LOCAL time scale.
	if msg.IsEvent() {
		local := d.osc.LocalAt(d.eng.Now())
		switch msg.Type {
		case MsgSync:
			// One-step: precise origin equals egress timestamp. Two-step:
			// the estimate goes in the Sync and is superseded by the
			// Follow_Up precise timestamp.
			if !msg.TwoStep || msg.TS.OriginTimestampNs == 0 {
				msg.TS.OriginTimestampNs = local
			}
		case MsgDelayReq:
			// Requester t3 bookkeeping.
			p.delayTxLocal = local
			p.delayGen = d.osc.Generation()
		case MsgPDelayReq:
			// Requester t1 bookkeeping.
			p.pdelayTxLocal = local
			p.pdelayGen = d.osc.Generation()
		case MsgPDelayResp:
			// Responder's egress timestamp t3': captured at the real
			// egress instant and echoed back to the requester.
			msg.RequestEgressTimestampNs = local
		}
	}
	p.link.send(p, msg)
}
