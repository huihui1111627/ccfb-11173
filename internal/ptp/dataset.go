// Package ptp contains the IEEE 1588-2008 protocol implementation: clock
// datasets, messages, the Best Master Clock Algorithm, port state machines,
// end-to-end and peer-to-peer delay mechanisms, transparent and boundary
// clocks, all driven by the discrete-event engine.
package ptp

import (
	"fmt"
	"strings"

	"ptpsim/internal/engine"
)

// ClockIdentity is 8 bytes; here it is carried as a canonical hex string so it
// doubles as a stable map key and JSON value.
type ClockIdentity string

// PortIdentity identifies one port (clock identity + port number).
type PortIdentity struct {
	Clock ClockIdentity `json:"clock"`
	Port  uint16        `json:"port"`
}

func (p PortIdentity) String() string { return fmt.Sprintf("%s:%d", p.Clock, p.Port) }

// PortRole is the state machine state of a port.
type PortRole string

const (
	RoleInitializing PortRole = "Initializing"
	RoleListening    PortRole = "Listening"
	RoleUncalibrated PortRole = "Uncalibrated"
	RoleMaster       PortRole = "Master"
	RoleSlave        PortRole = "Slave"
	RolePassive      PortRole = "Passive"
	RolePreMaster    PortRole = "PreMaster"
	RoleFaulty       PortRole = "Faulty"
	RoleDisabled     PortRole = "Disabled"
)

// DelayMechanism selects how a port measures path delay.
type DelayMechanism string

const (
	DelayE2E DelayMechanism = "E2E" // Delay_Req / Delay_Resp
	DelayP2P DelayMechanism = "P2P" // Pdelay_Req / Pdelay_Resp / Pdelay_Resp_Follow_Up
)

// DeviceType distinguishes ordinary clocks, boundary clocks and transparent
// clocks.
type DeviceType string

const (
	TypeOC DeviceType = "OC" // ordinary clock
	TypeBC DeviceType = "BC" // boundary clock
	TypeTC DeviceType = "TC" // transparent clock (E2E or P2P)
)

// DefaultDS is the local default data set (IEEE 1588 table covering the
// fields used by BMCA).
type DefaultDS struct {
	ClockIdentity       ClockIdentity `json:"clockIdentity"`
	ClockQuality        ClockQuality  `json:"clockQuality"`
	Priority1           uint8         `json:"priority1"`
	Priority2           uint8         `json:"priority2"`
	TwoStep             bool          `json:"twoStep"`
	GrandmasterIdentity ClockIdentity `json:"grandmasterIdentity"`
}

// ClockQuality is the three-field BMCA tuple.
type ClockQuality struct {
	ClockClass              uint8  `json:"clockClass"`
	ClockAccuracy           uint8  `json:"clockAccuracy"` // IEEE 1588 enum (0x20 = 25 ns ...)
	OffsetScaledLogVariance uint16 `json:"offsetScaledLogVariance"`
}

// ParentDS describes the selected grandmaster as known locally.
type ParentDS struct {
	GrandmasterIdentity     ClockIdentity `json:"grandmasterIdentity"`
	GrandmasterClockQuality ClockQuality  `json:"grandmasterClockQuality"`
	GrandmasterPriority1    uint8         `json:"grandmasterPriority1"`
	GrandmasterPriority2    uint8         `json:"grandmasterPriority2"`
	ParentPortIdentity      PortIdentity  `json:"parentPortIdentity"`
	StepsRemoved            uint16        `json:"stepsRemoved"`
}

// EForeignMaster is one entry of a port's foreign master data set (8.6.3).
type EForeignMaster struct {
	Announce *AnnounceFields
	PortID   PortIdentity // identity embedded in the received Announce
	RxPort   *Port
	Count    int
	LastSeen engine.SimTime
	BestRank int // index during election, for UI
}

// CompareDatasets implements the data set comparison algorithm of 9.3.5 b):
// priority1, class, accuracy, variance, priority2, identity. Returns -1 when a
// is better, +1 when b is better, 0 on full tie.
func CompareDatasets(a, b *AnnounceFields) int {
	if a.GrandmasterPriority1 != b.GrandmasterPriority1 {
		return cmpU8(a.GrandmasterPriority1, b.GrandmasterPriority1)
	}
	if a.GrandmasterClockQuality.ClockClass != b.GrandmasterClockQuality.ClockClass {
		return cmpU8(a.GrandmasterClockQuality.ClockClass, b.GrandmasterClockQuality.ClockClass)
	}
	if a.GrandmasterClockQuality.ClockAccuracy != b.GrandmasterClockQuality.ClockAccuracy {
		return cmpU8(a.GrandmasterClockQuality.ClockAccuracy, b.GrandmasterClockQuality.ClockAccuracy)
	}
	if a.GrandmasterClockQuality.OffsetScaledLogVariance != b.GrandmasterClockQuality.OffsetScaledLogVariance {
		return cmpU16(a.GrandmasterClockQuality.OffsetScaledLogVariance, b.GrandmasterClockQuality.OffsetScaledLogVariance)
	}
	if a.GrandmasterPriority2 != b.GrandmasterPriority2 {
		return cmpU8(a.GrandmasterPriority2, b.GrandmasterPriority2)
	}
	return compareIdentity(a.GrandmasterIdentity, b.GrandmasterIdentity)
}

// compareIdentity orders clock identities lexicographically (lower is better
// as the final tie-break).
func compareIdentity(a, b ClockIdentity) int {
	return strings.Compare(string(a), string(b))
}

func cmpU8(a, b uint8) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func cmpU16(a, b uint16) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// LocalAnnounce builds the AnnounceFields this device would transmit, given
// its current ParentDS (or itself when it is grandmaster-capable).
func LocalAnnounce(d *DefaultDS, p *ParentDS, steps uint16) *AnnounceFields {
	if p == nil {
		return &AnnounceFields{
			GrandmasterPriority1:    d.Priority1,
			GrandmasterClockQuality: d.ClockQuality,
			GrandmasterPriority2:    d.Priority2,
			GrandmasterIdentity:     d.ClockIdentity,
			StepsRemoved:            steps,
		}
	}
	return &AnnounceFields{
		GrandmasterPriority1:    p.GrandmasterPriority1,
		GrandmasterClockQuality: p.GrandmasterClockQuality,
		GrandmasterPriority2:    p.GrandmasterPriority2,
		GrandmasterIdentity:     p.GrandmasterIdentity,
		StepsRemoved:            steps,
	}
}
