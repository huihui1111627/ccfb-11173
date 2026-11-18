// Package experiment runs scenarios to completion and evaluates the results:
// grandmaster elections vs. hand calculation, failover time, slave offset
// statistics, and byte-identical reproducibility for a fixed seed.
package experiment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"

	"ptpsim/internal/engine"
	"ptpsim/internal/ptp"
	"ptpsim/internal/scenario"
	"ptpsim/internal/trace"
)

// Sample is one ground-truth offset probe.
type Sample struct {
	T         engine.SimTime `json:"t"`
	Device    string         `json:"device"`
	TrueOffNs float64        `json:"trueOffsetNs"`
	GM        string         `json:"gm"`
	Role      string         `json:"role"`
}

// Result is the full evaluated outcome of one run.
type Result struct {
	SpecName        string         `json:"specName"`
	Topology        string         `json:"topology"`
	Seed            uint64         `json:"seed"`
	DurationNs      engine.SimTime `json:"durationNs"`
	FinalGM         string         `json:"finalGm"`
	InitialGM       string         `json:"initialGm"`
	Failover        *FailoverStats `json:"failover,omitempty"`
	Devices         []DeviceStat   `json:"devices"`
	SlaveSummary    SlaveSummary   `json:"slaveSummary"`
	TraceHash       string         `json:"traceHash"`
	ReproHash       string         `json:"reproHash"`
	Reproducible    bool           `json:"reproducible"`
	TrueSamples     []Sample       `json:"trueSamples"`
	ElectionCount   int            `json:"electionCount"`
	PassivePorts    []string       `json:"passivePorts"`
	FollowUpRounds  int            `json:"followUpRounds"`
	FollowUpDropped int            `json:"followUpDropped"`
}

// FailoverStats describes a grandmaster power-loss event.
type FailoverStats struct {
	FailAt       engine.SimTime `json:"failAt"`
	OldGM        string         `json:"oldGm"`
	NewGM        string         `json:"newGm"`
	ReelectionAt engine.SimTime `json:"reelectionAt"`
	SwitchTimeNs engine.SimTime `json:"switchTimeNs"`
}

// DeviceStat exposes final per-device state.
type DeviceStat struct {
	Name       string     `json:"name"`
	Type       string     `json:"type"`
	Powered    bool       `json:"powered"`
	GmIdentity string     `json:"gmIdentity"`
	Steps      int        `json:"stepsRemoved"`
	Ports      []PortStat `json:"ports"`
	FreqPPB    float64    `json:"freqPpb"`
	ServoSteps int        `json:"servoSteps"`
	ServoSlews int        `json:"servoSlews"`
}

// PortStat is a final port snapshot.
type PortStat struct {
	Index       int     `json:"index"`
	Role        string  `json:"role"`
	PeerDelayNs float64 `json:"peerDelayNs,omitempty"`
	Foreign     int     `json:"foreign"`
	SyncRx      int64   `json:"syncRx"`
	AnnounceRx  int64   `json:"announceRx"`
}

// SlaveSummary aggregates true offsets over the post-convergence window.
type SlaveSummary struct {
	WindowStartNs engine.SimTime     `json:"windowStartNs"`
	MeanAbsNs     float64            `json:"meanAbsNs"`
	MaxAbsNs      float64            `json:"maxAbsNs"`
	RMSNs         float64            `json:"rmsNs"`
	PerDeviceMean map[string]float64 `json:"perDeviceMean"`
	PerDeviceMax  map[string]float64 `json:"perDeviceMax"`
}

// Run executes one scenario spec.
func Run(s scenario.Spec) (*Result, *ptp.Mesh, error) {
	mesh, err := scenario.Build(s)
	if err != nil {
		return nil, nil, err
	}
	box := installSampler(mesh, s.Duration, engine.Ms(200))
	mesh.Eng.Run(s.Duration)
	samples := box.s

	res := evaluate(mesh, s, samples)
	return res, mesh, nil
}

// RunReproducible runs the same spec twice and compares canonical trace
// hashes. Both Result values are returned; the first is the primary report.
func RunReproducible(s scenario.Spec) (first, second *Result, err error) {
	first, _, err = Run(s)
	if err != nil {
		return nil, nil, err
	}
	second, _, err = Run(s)
	if err != nil {
		return nil, nil, err
	}
	second.ReproHash = first.TraceHash
	second.Reproducible = second.TraceHash == first.TraceHash
	first.ReproHash = second.TraceHash
	first.Reproducible = first.TraceHash == second.TraceHash
	return first, second, nil
}

// installSampler records ground-truth offsets every step until end. Samples
// are collected through a pointer so callbacks scheduled before the engine
// runs always mutate the same backing slice the caller receives.
// samplerBox lets Run read the post-run contents even though the collector
// is installed before the engine starts.
type samplerBox struct{ s []Sample }

func installSampler(mesh *ptp.Mesh, end, step engine.SimTime) *samplerBox {
	n := int(end / step)
	out := &samplerBox{s: make([]Sample, 0, n*8)}
	for t := step; t < end; t += step {
		t := t
		mesh.Eng.At(t, func() {
			gm := mesh.Grandmaster()
			gmName := ""
			if gm != nil {
				gmName = gm.Name
			}
			for _, d := range mesh.Devices() {
				if d.PoweredOff() || d.Type == ptp.TypeTC || d.SlavePort() == nil {
					continue
				}
				out.s = append(out.s, Sample{
					T:         t,
					Device:    d.Name,
					TrueOffNs: mesh.TrueOffsetNs(t, d),
					GM:        gmName,
					Role:      string(ptp.RoleSlave),
				})
			}
		})
	}
	return out
}

// evaluate builds the Result from trace + mesh state.
func evaluate(mesh *ptp.Mesh, s scenario.Spec, samples []Sample) *Result {
	rec := mesh.Rec
	r := &Result{
		SpecName:      s.Name,
		Topology:      string(s.Kind),
		Seed:          s.Seed,
		DurationNs:    s.Duration,
		TrueSamples:   samples,
		ElectionCount: len(rec.Elections()),
	}

	// GM history / failover.
	elections := rec.Elections()
	generics := rec.Generics()
	if len(elections) > 0 {
		r.InitialGM = gmDeviceName(mesh, elections[0].GmIdentity)
	}
	gm := mesh.Grandmaster()
	if gm != nil {
		r.FinalGM = gm.Name
	}
	for _, g := range generics {
		if g.What == "power_off" {
			r.Failover = buildFailover(mesh, g, elections)
		}
	}

	// Passive ports (loop prevention evidence).
	for _, d := range mesh.Devices() {
		for _, p := range d.Ports() {
			if p.Role() == ptp.RolePassive {
				r.PassivePorts = append(r.PassivePorts,
					fmt.Sprintf("%s:p%d", d.Name, p.Index))
			}
		}
	}

	// Two-step accounting.
	msgs := rec.Messages()
	for _, m := range msgs {
		if m.MsgType == "Follow_Up" {
			r.FollowUpRounds++
		}
		if m.Kind == trace.KindDrop && (m.MsgType == "Follow_Up" ||
			m.MsgType == "Pdelay_Resp_Follow_Up") {
			r.FollowUpDropped++
		}
	}

	// Final device snapshots.
	for _, d := range mesh.Devices() {
		ds := DeviceStat{
			Name:    d.Name,
			Type:    string(d.Type),
			Powered: !d.PoweredOff(),
			FreqPPB: d.Osc().FreqPPB(mesh.Eng.Now()),
		}
		steps, slews := d.Servo().Stats()
		ds.ServoSteps, ds.ServoSlews = steps, slews
		if d.Parent() != nil {
			ds.GmIdentity = string(d.Parent().GrandmasterIdentity)
			ds.Steps = int(d.Parent().StepsRemoved)
		} else {
			ds.GmIdentity = string(d.Identity())
		}
		for _, p := range d.Ports() {
			peerDelay, _ := p.PeerDelay()
			ds.Ports = append(ds.Ports, PortStat{
				Index:       int(p.Index),
				Role:        string(p.Role()),
				PeerDelayNs: peerDelay,
				Foreign:     p.Counters.ForeignMaster,
				SyncRx:      p.Counters.SyncRx,
				AnnounceRx:  p.Counters.AnnounceRx,
			})
		}
		r.Devices = append(r.Devices, ds)
	}

	r.SlaveSummary = summarize(samples, s.Duration/2)
	r.TraceHash = hashTrace(rec)
	return r
}

func buildFailover(mesh *ptp.Mesh, off trace.GenericEvent, elections []trace.ElectionEvent) *FailoverStats {
	oldName := off.Device
	var newGM string
	var reElect engine.SimTime
	for _, e := range elections {
		if e.T <= off.T {
			continue
		}
		name := gmDeviceName(mesh, e.GmIdentity)
		if name != "" && name != oldName {
			newGM = name
			reElect = e.T
			break
		}
	}
	if newGM == "" {
		return &FailoverStats{FailAt: off.T, OldGM: oldName}
	}
	return &FailoverStats{
		FailAt:       off.T,
		OldGM:        oldName,
		NewGM:        newGM,
		ReelectionAt: reElect,
		SwitchTimeNs: reElect - off.T,
	}
}

func gmDeviceName(mesh *ptp.Mesh, id string) string {
	for _, d := range mesh.Devices() {
		if string(d.Identity()) == id {
			return d.Name
		}
	}
	return ""
}

// summarize computes mean/max/RMS over samples at or after windowStart.
func summarize(samples []Sample, windowStart engine.SimTime) SlaveSummary {
	out := SlaveSummary{
		WindowStartNs: windowStart,
		PerDeviceMean: map[string]float64{},
		PerDeviceMax:  map[string]float64{},
	}
	sum := map[string]float64{}
	cnt := map[string]int{}
	maxAbs := map[string]float64{}
	var globalSum, globalSq float64
	var globalN int
	var globalMax float64
	for _, sm := range samples {
		if sm.T < windowStart {
			continue
		}
		a := math.Abs(sm.TrueOffNs)
		sum[sm.Device] += a
		cnt[sm.Device]++
		if a > maxAbs[sm.Device] {
			maxAbs[sm.Device] = a
		}
		globalSum += a
		globalSq += sm.TrueOffNs * sm.TrueOffNs
		globalN++
		if a > globalMax {
			globalMax = a
		}
	}
	for name := range cnt {
		out.PerDeviceMean[name] = sum[name] / float64(cnt[name])
		out.PerDeviceMax[name] = maxAbs[name]
	}
	if globalN > 0 {
		out.MeanAbsNs = globalSum / float64(globalN)
		out.MaxAbsNs = globalMax
		out.RMSNs = math.Sqrt(globalSq / float64(globalN))
	}
	return out
}

// hashTrace produces a canonical hash over every recorded stream.
func hashTrace(rec *trace.Recorder) string {
	payload := struct {
		Messages []trace.MsgEvent       `json:"messages"`
		States   []trace.PortStateEvent `json:"states"`
		Elect    []trace.ElectionEvent  `json:"elections"`
		Offsets  []trace.OffsetSample   `json:"offsets"`
		Generic  []trace.GenericEvent   `json:"generic"`
	}{
		rec.Messages(), rec.PortStates(), rec.Elections(),
		rec.Offsets(), rec.Generics(),
	}
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
