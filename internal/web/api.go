// Package web serves the synchronization dashboard: a JSON API over a live
// simulation plus a single-page view. The API exposes the five panels the
// product requires: device topology, grandmaster election, per-port state,
// offset convergence curves, and the message timeline.
package web

import (
	"encoding/json"
	"net/http"

	"ptpsim/internal/engine"
	"ptpsim/internal/ptp"
	"ptpsim/internal/theory"
)

// TopologyNode is one device in the topology view.
type TopologyNode struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Powered   bool   `json:"powered"`
	IsGM      bool   `json:"isGm"`
	Identity  string `json:"identity"`
	Priority1 uint8  `json:"priority1"`
	Class     uint8  `json:"clockClass"`
	X         int    `json:"x"`
	Y         int    `json:"y"`
}

// TopologyEdge is one link with its directional delays and compensation.
type TopologyEdge struct {
	ID         string  `json:"id"`
	Source     string  `json:"source"`
	SourcePort int     `json:"sourcePort"`
	Target     string  `json:"target"`
	TargetPort int     `json:"targetPort"`
	DelayAB    int64   `json:"delayABNs"`
	DelayBA    int64   `json:"delayBANs"`
	AsymAB     float64 `json:"asymCompAB"`
	AsymBA     float64 `json:"asymCompBA"`
}

// TopologyView is the full topology panel payload.
type TopologyView struct {
	Nodes []TopologyNode `json:"nodes"`
	Edges []TopologyEdge `json:"edges"`
}

// PortView is the per-port state-machine snapshot.
type PortView struct {
	Device      string  `json:"device"`
	Port        int     `json:"port"`
	Role        string  `json:"role"`
	Foreign     int     `json:"foreignMasters"`
	PeerDelayNs float64 `json:"peerDelayNs,omitempty"`
	SyncRx      int64   `json:"syncRx"`
	AnnRx       int64   `json:"announceRx"`
	DelayReqTx  int64   `json:"delayReqTx"`
}

// ElectionView is one BMCA decision with the hand-calculation comparison.
type ElectionView struct {
	T           float64            `json:"tSec"`
	Device      string             `json:"device"`
	GM          string             `json:"grandmaster"`
	Steps       int                `json:"stepsRemoved"`
	Decision    string             `json:"decision"`
	Detail      string             `json:"detail"`
	TheoryTrace []theory.BMCATrace `json:"theoryTrace,omitempty"`
}

// OffsetCurve is one device's convergence series.
type OffsetCurve struct {
	Device string        `json:"device"`
	Points []OffsetPoint `json:"points"`
}

// OffsetPoint is one sampled point on a curve.
type OffsetPoint struct {
	TSec     float64 `json:"tSec"`
	Measured float64 `json:"measuredNs"`
	True     float64 `json:"trueNs"`
	FreqPPB  float64 `json:"freqPpb"`
}

// TimelineMsg is one row of the message timeline.
type TimelineMsg struct {
	TSec       float64 `json:"tSec"`
	Kind       string  `json:"kind"`
	Device     string  `json:"device"`
	Port       int     `json:"port"`
	Peer       string  `json:"peer,omitempty"`
	MsgType    string  `json:"msgType"`
	SeqID      uint16  `json:"seqId"`
	Correction float64 `json:"correctionNs,omitempty"`
	Residence  float64 `json:"residenceNs,omitempty"`
	Lost       bool    `json:"lost,omitempty"`
	Detail     string  `json:"detail,omitempty"`
}

// StateTransition is a port role change for the state panel.
type StateTransition struct {
	TSec   float64 `json:"tSec"`
	Device string  `json:"device"`
	Port   int     `json:"port"`
	From   string  `json:"from"`
	To     string  `json:"to"`
	Reason string  `json:"reason"`
}

// Snapshot is the complete dashboard payload for one point in (simulated)
// time. The server returns the whole run; the front-end scrubs the timeline
// client-side.
type Snapshot struct {
	SpecName    string            `json:"specName"`
	Topology    string            `json:"topology"`
	Seed        uint64            `json:"seed"`
	DurationSec float64           `json:"durationSec"`
	FinalGM     string            `json:"finalGm"`
	Topo        TopologyView      `json:"topologyView"`
	Ports       []PortView        `json:"ports"`
	Elections   []ElectionView    `json:"elections"`
	Curves      []OffsetCurve     `json:"offsetCurves"`
	Timeline    []TimelineMsg     `json:"timeline"`
	Transitions []StateTransition `json:"stateTransitions"`
	Summary     SummaryView       `json:"summary"`
	TheoryCheck *TheoryCheckView  `json:"theoryCheck,omitempty"`
}

// SummaryView carries the headline offset statistics.
type SummaryView struct {
	MeanAbsNs float64            `json:"meanAbsNs"`
	MaxAbsNs  float64            `json:"maxAbsNs"`
	RMSNs     float64            `json:"rmsNs"`
	PerDevice map[string]float64 `json:"perDeviceMean"`
	Passive   []string           `json:"passivePorts"`
}

// TheoryCheckView compares simulation against hand calculation.
type TheoryCheckView struct {
	PredictedGM      string             `json:"predictedGm"`
	ActualGM         string             `json:"actualGm"`
	GMMatches        bool               `json:"gmMatches"`
	PredictedPassive string             `json:"predictedPassive,omitempty"`
	ResidualFloorNs  float64            `json:"residualFloorNs"`
	Trace            []theory.BMCATrace `json:"bmcaTrace"`
}

// BuildSnapshot assembles a Snapshot from a completed run.
func BuildSnapshot(specName string, mesh *ptp.Mesh, duration engine.SimTime,
	theoryCheck *TheoryCheckView) Snapshot {
	snap := Snapshot{
		SpecName:    specName,
		Topology:    meshName(mesh),
		Seed:        mesh.Seed,
		DurationSec: duration.Seconds(),
		TheoryCheck: theoryCheck,
		Summary: SummaryView{
			PerDevice: map[string]float64{},
		},
	}
	gm := mesh.Grandmaster()
	if gm != nil {
		snap.FinalGM = gm.Name
	}
	snap.Topo = buildTopology(mesh)
	snap.Ports = buildPorts(mesh)
	snap.Elections = buildElections(mesh)
	snap.Curves = buildCurves(mesh)
	snap.Timeline = buildTimeline(mesh)
	snap.Transitions = buildTransitions(mesh)
	return snap
}

func meshName(mesh *ptp.Mesh) string {
	for _, d := range mesh.Devices() {
		_ = d
		break
	}
	return "ptp-mesh"
}

// Handler returns the HTTP mux for the dashboard. data is prebuilt.
func Handler(snap Snapshot) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/snapshot", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	})
	mux.Handle("/", http.FileServer(http.FS(staticFS())))
	return mux
}
