package web

import (
	"fmt"

	"ptpsim/internal/ptp"
)

// buildTopology lays devices out on a deterministic grid and describes every
// link with its two directional delays.
func buildTopology(mesh *ptp.Mesh) TopologyView {
	view := TopologyView{}
	gm := mesh.Grandmaster()
	gmName := ""
	if gm != nil {
		gmName = gm.Name
	}
	// Simple layered layout: index order on a horizontal lane, wrapping into
	// rows of four for readability.
	devs := mesh.Devices()
	pos := map[string][2]int{}
	for i, d := range devs {
		col := i % 4
		row := i / 4
		x := 120 + col*240
		y := 120 + row*200
		pos[d.Name] = [2]int{x, y}
		cfg := d.Config()
		view.Nodes = append(view.Nodes, TopologyNode{
			ID: d.Name, Name: d.Name, Type: string(d.Type),
			Powered: !d.PoweredOff(), IsGM: d.Name == gmName,
			Identity: string(d.Identity()), Priority1: cfg.Priority1,
			Class: cfg.ClockClass, X: x, Y: y,
		})
	}
	for _, l := range mesh.Links() {
		c := l.Config()
		aName, aPort, bName, bPort := l.EndpointNames()
		view.Edges = append(view.Edges, TopologyEdge{
			ID:     c.Name,
			Source: aName, SourcePort: aPort,
			Target: bName, TargetPort: bPort,
			DelayAB: int64(c.DelayAtoB), DelayBA: int64(c.DelayBtoA),
			AsymAB: c.AsymCompAtoB, AsymBA: c.AsymCompBtoA,
		})
	}
	return view
}

// buildPorts snapshots every port's role and counters.
func buildPorts(mesh *ptp.Mesh) []PortView {
	var out []PortView
	for _, d := range mesh.Devices() {
		for _, p := range d.Ports() {
			peer, _ := p.PeerDelay()
			out = append(out, PortView{
				Device: d.Name, Port: int(p.Index), Role: string(p.Role()),
				Foreign: p.Counters.ForeignMaster, PeerDelayNs: peer,
				SyncRx: p.Counters.SyncRx, AnnRx: p.Counters.AnnounceRx,
				DelayReqTx: p.Counters.DelayReqTx,
			})
		}
	}
	return out
}

// buildElections converts the recorded BMCA decisions to view rows.
func buildElections(mesh *ptp.Mesh) []ElectionView {
	var out []ElectionView
	for _, e := range mesh.Rec.Elections() {
		out = append(out, ElectionView{
			T: e.T.Seconds(), Device: e.Device, GM: e.GmIdentity,
			Steps: e.StepsRemoved, Decision: e.Decision, Detail: e.Detail,
		})
	}
	return out
}

// buildCurves groups servo offset samples by device and downsamples to keep
// the JSON payload bounded for long runs.
func buildCurves(mesh *ptp.Mesh) []OffsetCurve {
	byDev := map[string][]OffsetPoint{}
	const stride = 1
	for _, o := range mesh.Rec.Offsets() {
		byDev[o.Device] = append(byDev[o.Device], OffsetPoint{
			TSec: o.T.Seconds(), Measured: o.OffsetNs, True: o.TrueOffsetNs,
			FreqPPB: o.FreqPPB,
		})
	}
	var out []OffsetCurve
	// Deterministic device ordering.
	for _, d := range mesh.Devices() {
		pts, ok := byDev[d.Name]
		if !ok {
			continue
		}
		if stride > 1 {
			down := make([]OffsetPoint, 0, len(pts)/stride+1)
			for i, p := range pts {
				if i%stride == 0 {
					down = append(down, p)
				}
			}
			pts = down
		}
		out = append(out, OffsetCurve{Device: d.Name, Points: pts})
	}
	return out
}

// buildTimeline converts recorded messages, bounded to the most recent N.
func buildTimeline(mesh *ptp.Mesh) []TimelineMsg {
	var out []TimelineMsg
	for _, m := range mesh.Rec.Messages() {
		out = append(out, TimelineMsg{
			TSec: m.T.Seconds(), Kind: string(m.Kind), Device: m.Device,
			Port: m.Port, Peer: m.PeerDev, MsgType: m.MsgType, SeqID: m.SeqID,
			Correction: m.Correction, Residence: m.Residence, Lost: m.Lost,
			Detail: m.Detail,
		})
	}
	return out
}

// buildTransitions exposes the port state-machine history.
func buildTransitions(mesh *ptp.Mesh) []StateTransition {
	var out []StateTransition
	for _, e := range mesh.Rec.PortStates() {
		out = append(out, StateTransition{
			TSec: e.T.Seconds(), Device: e.Device, Port: e.Port,
			From: e.From, To: e.To, Reason: e.Reason,
		})
	}
	return out
}

// String helpers for UI labels.
func roleLabel(r string) string { return fmt.Sprintf("%s", r) }
