package ptp

import "ptpsim/internal/engine"

// MsgType enumerates PTP message types (13.3 / table 34).
type MsgType uint8

const (
	MsgSync MsgType = iota
	MsgDelayReq
	MsgPDelayReq
	MsgPDelayResp
	MsgFollowUp
	MsgDelayResp
	MsgPDelayRespFollowUp
	MsgAnnounce
	MsgSignaling
	MsgManagement
)

// String returns the canonical short name used on the dashboard.
func (m MsgType) String() string {
	switch m {
	case MsgSync:
		return "Sync"
	case MsgDelayReq:
		return "Delay_Req"
	case MsgPDelayReq:
		return "Pdelay_Req"
	case MsgPDelayResp:
		return "Pdelay_Resp"
	case MsgFollowUp:
		return "Follow_Up"
	case MsgDelayResp:
		return "Delay_Resp"
	case MsgPDelayRespFollowUp:
		return "Pdelay_Resp_Follow_Up"
	case MsgAnnounce:
		return "Announce"
	case MsgSignaling:
		return "Signaling"
	case MsgManagement:
		return "Management"
	default:
		return "Unknown"
	}
}

// Event reports whether the message is an event message requiring a hardware
// timestamp (Sync, Delay_Req, Pdelay_Req, Pdelay_Resp).
func (m MsgType) Event() bool {
	switch m {
	case MsgSync, MsgDelayReq, MsgPDelayReq, MsgPDelayResp:
		return true
	default:
		return false
	}
}

// AnnounceFields is the Announce payload used by BMCA.
type AnnounceFields struct {
	GrandmasterPriority1    uint8         `json:"gmPriority1"`
	GrandmasterClockQuality ClockQuality  `json:"gmClockQuality"`
	GrandmasterPriority2    uint8         `json:"gmPriority2"`
	GrandmasterIdentity     ClockIdentity `json:"gmIdentity"`
	StepsRemoved            uint16        `json:"stepsRemoved"`
	// OriginPortIdentity is the sending port (for the topology tie-break).
	OriginPortIdentity PortIdentity `json:"originPort"`
}

// TSF holds the timestamps of a message: origin/precise origin timestamps and
// the correction field. All values are nanoseconds (float to hold fractional
// correction contributions).
type TSF struct {
	// OriginTimestampNs: secondsField/nanosecondsField of event messages
	// (estimated egress in two-step mode).
	OriginTimestampNs float64 `json:"originTsNs,omitempty"`
	// PreciseOriginTimestampNs: Follow_Up / Pdelay_Resp_Follow_Up.
	PreciseOriginTimestampNs float64 `json:"preciseTsNs,omitempty"`
	// CorrectionFieldNs accumulates TC residence + asymmetry correction
	// (1588 correctionField is scaled ns; here always plain ns).
	CorrectionFieldNs float64 `json:"correctionNs"`
}

// Message is the in-simulator representation of a PTP packet.
type Message struct {
	Type     MsgType `json:"type"`
	SeqID    uint16  `json:"seqId"`
	Domain   uint8   `json:"domain"`
	TwoStep  bool    `json:"twoStep"`
	Source   PortIdentity
	Target   PortIdentity // zero value = multicast
	TS       TSF
	Announce *AnnounceFields `json:",omitempty"`

	// ReceiveTimestampNs is stamped by the link with the receiver's LOCAL
	// clock value at ingress (set during delivery).
	ReceiveTimestampNs float64 `json:"-"`
	// IngressTrue / EgressTrue carry simulator-true timestamps used
	// internally by transparent clocks for residence measurement.
	IngressTrue engine.SimTime `json:"-"`
	EgressTrue  engine.SimTime `json:"-"`
	// IngressLocal / EgressLocal: hardware timestamps in the LOCAL time
	// scale of the TC (for residence = egress - ingress under possible
	// frequency error).
	IngressLocal float64 `json:"-"`
	EgressLocal  float64 `json:"-"`

	// For Delay_Resp / Pdelay_Resp: echo of the requester timestamps.
	RequestIngressTimestampNs float64 `json:"-"`
	RequestEgressTimestampNs  float64 `json:"-"`

	// Lost marks a Follow_Up injected as lost by the scenario (drives rule
	// "two-step follow-up loss invalidates the round").
	Lost bool `json:"lost,omitempty"`

	// Per-hop metadata for the message timeline view.
	Hops []HopInfo `json:"hops,omitempty"`
}

// HopInfo records one transparent-clock traversal for the UI.
type HopInfo struct {
	Device       string  `json:"device"`
	ResidenceNs  float64 `json:"residenceNs"`
	CorrectionNs float64 `json:"correctionNs"`
}

// IsEvent wraps the type query.
func (m *Message) IsEvent() bool { return m.Type.Event() }
