// Package trace collects the observable history of a simulation: every PTP
// message on every link, port state transitions, BMCA decisions and servo
// offset samples. Recorders keep bounded ring buffers so long simulations do
// not grow without limit.
package trace

import (
	"sync"

	"ptpsim/internal/engine"
)

// Kind classifies a traced message event.
type Kind string

const (
	KindSend      Kind = "send"
	KindReceive   Kind = "recv"
	KindDrop      Kind = "drop"
	KindTimeout   Kind = "timeout"
	KindCorrected Kind = "corrected"
)

// MsgEvent is one packet observation.
type MsgEvent struct {
	T        engine.SimTime `json:"t"`
	Kind     Kind           `json:"kind"`
	Device   string         `json:"device"`
	Port     int            `json:"port"`
	PeerDev  string         `json:"peerDev,omitempty"`
	PeerPort int            `json:"peerPort,omitempty"`
	MsgType  string         `json:"msgType"`
	SeqID    uint16         `json:"seqId"`
	TwoStep  bool           `json:"twoStep,omitempty"`
	// Correction in nanoseconds carried in the message.
	Correction float64 `json:"correctionNs,omitempty"`
	// For TC observations: residence time added at this hop.
	Residence float64 `json:"residenceNs,omitempty"`
	Lost      bool    `json:"lost,omitempty"`
	Detail    string  `json:"detail,omitempty"`
}

// PortStateEvent records a port state machine transition.
type PortStateEvent struct {
	T      engine.SimTime `json:"t"`
	Device string         `json:"device"`
	Port   int            `json:"port"`
	From   string         `json:"from"`
	To     string         `json:"to"`
	Reason string         `json:"reason"`
}

// ElectionEvent records one BMCA run outcome on a device.
type ElectionEvent struct {
	T            engine.SimTime `json:"t"`
	Device       string         `json:"device"`
	Grandmaster  string         `json:"grandmaster"`
	GmIdentity   string         `json:"gmIdentity"`
	BestSource   string         `json:"bestSource"` // "self" or remote clock identity
	StepsRemoved int            `json:"stepsRemoved"`
	Decision     string         `json:"decision"`
	Detail       string         `json:"detail"`
}

// OffsetSample is one servo convergence point for an ordinary/boundary slave.
type OffsetSample struct {
	T            engine.SimTime `json:"t"`
	Device       string         `json:"device"`
	OffsetNs     float64        `json:"offsetNs"`     // measured slave - master
	TrueOffsetNs float64        `json:"trueOffsetNs"` // actual oscillator time difference
	FreqPPB      float64        `json:"freqPpb"`      // current servo frequency correction
	MeanPathNs   float64        `json:"meanPathNs"`
}

// GenericEvent covers power on/off, loss injection and other scenario notes.
type GenericEvent struct {
	T      engine.SimTime `json:"t"`
	Device string         `json:"device"`
	What   string         `json:"what"`
	Detail string         `json:"detail"`
}

type ring[T any] struct {
	mu   sync.Mutex
	buf  []T
	next int
	full bool
	cap  int
}

func newRing[T any](cap int) *ring[T] { return &ring[T]{buf: make([]T, cap), cap: cap} }

func (r *ring[T]) add(v T) {
	r.mu.Lock()
	r.buf[r.next] = v
	r.next = (r.next + 1) % r.cap
	if r.next == 0 {
		r.full = true
	}
	r.mu.Unlock()
}

func (r *ring[T]) snapshot() []T {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.cap
	if !r.full {
		n = r.next
	}
	out := make([]T, 0, n)
	if r.full {
		out = append(out, r.buf[r.next:]...)
		out = append(out, r.buf[:r.next]...)
	} else {
		out = append(out, r.buf[:n]...)
	}
	return out
}

// Recorder stores the full trace history (bounded per stream).
type Recorder struct {
	messages *ring[MsgEvent]
	states   *ring[PortStateEvent]
	elect    *ring[ElectionEvent]
	offsets  *ring[OffsetSample]
	generic  *ring[GenericEvent]
}

// NewRecorder builds a recorder with explicit ring capacities.
func NewRecorder(msgCap, stateCap, electCap, offsetCap, genericCap int) *Recorder {
	return &Recorder{
		messages: newRing[MsgEvent](msgCap),
		states:   newRing[PortStateEvent](stateCap),
		elect:    newRing[ElectionEvent](electCap),
		offsets:  newRing[OffsetSample](offsetCap),
		generic:  newRing[GenericEvent](genericCap),
	}
}

func (r *Recorder) Message(e MsgEvent)         { r.messages.add(e) }
func (r *Recorder) PortState(e PortStateEvent) { r.states.add(e) }
func (r *Recorder) Election(e ElectionEvent)   { r.elect.add(e) }
func (r *Recorder) Offset(e OffsetSample)      { r.offsets.add(e) }
func (r *Recorder) Generic(e GenericEvent)     { r.generic.add(e) }

func (r *Recorder) Messages() []MsgEvent         { return r.messages.snapshot() }
func (r *Recorder) PortStates() []PortStateEvent { return r.states.snapshot() }
func (r *Recorder) Elections() []ElectionEvent   { return r.elect.snapshot() }
func (r *Recorder) Offsets() []OffsetSample      { return r.offsets.snapshot() }
func (r *Recorder) Generics() []GenericEvent     { return r.generic.snapshot() }
