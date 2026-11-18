// Command ptpsim runs a PTP scenario, evaluates it against hand calculations,
// and serves the synchronization dashboard.
//
// Usage:
//
//	go run ./cmd/ptpsim -scenario failover -http :8080
//
// Scenarios: linear, ring, tc, failover, asymmetry, foldup, p2p.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"ptpsim/internal/clock"
	"ptpsim/internal/engine"
	"ptpsim/internal/experiment"
	"ptpsim/internal/ptp"
	"ptpsim/internal/scenario"
	"ptpsim/internal/theory"
	"ptpsim/internal/web"
)

func main() {
	scn := flag.String("scenario", "failover", "scenario name: linear|ring|tc|failover|asymmetry|foldup|p2p")
	seed := flag.Uint64("seed", 20261119, "master random seed (same seed => identical run)")
	addr := flag.String("http", ":8080", "dashboard listen address")
	noHTTP := flag.Bool("no-http", false, "run and print results without serving the dashboard")
	duration := flag.Float64("duration", 0, "override scenario duration in seconds (0 = built-in)")
	flag.Parse()

	spec := buildSpec(*scn, *seed, *duration)
	res, mesh, err := experiment.Run(spec)
	if err != nil {
		log.Fatalf("scenario run failed: %v", err)
	}

	// Reproducibility self-check: rerun and compare trace hashes.
	res2, _, err := experiment.Run(spec)
	if err != nil {
		log.Fatalf("reproducibility rerun failed: %v", err)
	}
	repro := res.TraceHash == res2.TraceHash
	fmt.Printf("trace hash      : %s\n", res.TraceHash)
	fmt.Printf("reproducible    : %v\n", repro)
	fmt.Printf("elected GM      : initial=%s final=%s\n", res.InitialGM, res.FinalGM)
	fmt.Printf("slave offsets   : mean=%.3f ns max=%.3f ns rms=%.3f ns\n",
		res.SlaveSummary.MeanAbsNs, res.SlaveSummary.MaxAbsNs, res.SlaveSummary.RMSNs)
	if res.Failover != nil {
		fmt.Printf("failover        : %s -> %s, switch in %.3f s\n",
			res.Failover.OldGM, res.Failover.NewGM, res.Failover.SwitchTimeNs.Seconds())
	}
	if !repro {
		fmt.Fprintln(os.Stderr, "ERROR: identical seed produced different traces")
		os.Exit(1)
	}

	check := buildTheoryCheck(mesh, spec, res)
	snap := web.BuildSnapshot(spec.Name, mesh, spec.Duration, check)
	snap.Summary = web.SummaryView{
		MeanAbsNs: res.SlaveSummary.MeanAbsNs,
		MaxAbsNs:  res.SlaveSummary.MaxAbsNs,
		RMSNs:     res.SlaveSummary.RMSNs,
		PerDevice: res.SlaveSummary.PerDeviceMean,
		Passive:   res.PassivePorts,
	}
	if *noHTTP {
		fmt.Println("dashboard       : skipped (-no-http)")
		return
	}

	fmt.Printf("dashboard       : http://localhost%s\n", *addr)
	log.Fatal(http.ListenAndServe(*addr, web.Handler(snap)))
}

// buildSpec maps a scenario name to a fully specified experiment.
func buildSpec(name string, seed uint64, durOverride float64) scenario.Spec {
	base := func(kind scenario.Kind, dur float64) scenario.Spec {
		s := scenario.Spec{
			Name: name, Kind: kind, Seed: seed,
			Duration:  engine.Sec(dur),
			LinkDelay: engine.Us(25),
			Servo:     clock.DefaultPIConfig(),
		}
		if durOverride > 0 {
			s.Duration = engine.Sec(durOverride)
		}
		return s
	}
	switch name {
	case "linear":
		s := base(scenario.LinearOC, 20)
		s.Delay, s.TwoStep, s.ChainDepth = ptp.DelayE2E, true, 2
		return s
	case "ring":
		s := base(scenario.RingBC, 18)
		s.Delay, s.TwoStep, s.RingSize = ptp.DelayE2E, true, 5
		return s
	case "tc":
		s := base(scenario.TransparentTC, 18)
		s.Delay, s.TwoStep, s.ChainDepth = ptp.DelayP2P, true, 2
		return s
	case "p2p":
		s := base(scenario.LinearOC, 18)
		s.Delay, s.TwoStep, s.ChainDepth = ptp.DelayP2P, true, 2
		return s
	case "failover":
		s := base(scenario.LinearOC, 28)
		s.Delay, s.TwoStep, s.ChainDepth = ptp.DelayE2E, false, 2
		s.Events = []scenario.Event{{
			At: engine.Sec(10), Kind: scenario.EvPowerOffGM, Device: "gm",
		}}
		return s
	case "asymmetry":
		s := base(scenario.LinearOC, 20)
		s.Delay, s.TwoStep, s.ChainDepth = ptp.DelayE2E, false, 1
		s.LinkDelay = engine.Us(40)
		s.AsymLinks = map[string]scenario.AsymLink{
			"gm-bc1": {Forward: engine.Us(80), Backward: engine.Us(20), Comp: true},
		}
		return s
	case "foldup":
		s := base(scenario.LinearOC, 22)
		s.Delay, s.TwoStep, s.ChainDepth = ptp.DelayE2E, true, 0
		s.Events = []scenario.Event{
			{At: engine.Sec(6), Kind: scenario.EvFollowUpLoss, Device: "gm", Port: 1},
			{At: engine.Sec(11), Kind: scenario.EvClearFollowLoss, Device: "gm", Port: 1},
		}
		return s
	default:
		log.Fatalf("unknown scenario %q", name)
		return scenario.Spec{}
	}
}

// buildTheoryCheck runs the independent hand calculation and compares it with
// the simulated election.
func buildTheoryCheck(mesh *ptp.Mesh, spec scenario.Spec, res *experiment.Result) *web.TheoryCheckView {
	powered := func(d *ptp.Device, t engine.SimTime) bool {
		// A device powered off by a scenario event stays off for the
		// prediction at any later time.
		for _, ev := range spec.Events {
			if ev.Kind == scenario.EvPowerOffGM && ev.Device == d.Name && t >= ev.At {
				return false
			}
		}
		return true
	}
	attrs := theory.SpecAttrs(mesh, powered)
	predicted, traceSteps, err := theory.PredictGrandmaster(attrs, spec.Duration-engine.Ns(1))
	if err != nil {
		return &web.TheoryCheckView{ActualGM: res.FinalGM, GMMatches: false}
	}
	view := &web.TheoryCheckView{
		PredictedGM: predicted,
		ActualGM:    res.FinalGM,
		GMMatches:   predicted == res.FinalGM,
		Trace:       traceSteps,
	}
	if spec.Kind == scenario.RingBC {
		var order []string
		for _, d := range mesh.Devices() {
			order = append(order, d.Name)
		}
		gmIdx := 0
		for i, n := range order {
			if n == predicted {
				gmIdx = i
				break
			}
		}
		dev, port := theory.RingPassivePort(predicted, order, gmIdx)
		view.PredictedPassive = fmt.Sprintf("%s:p%d", dev, port)
	}
	// Residual floor from the configured oscillator phase noise.
	oscCfg := mesh.Devices()[0].Config().Osc
	view.ResidualFloorNs = theory.SteadyStateResidual(oscCfg.PhaseSigmaNs,
		oscCfg.TimeResolutionNs)
	return view
}
