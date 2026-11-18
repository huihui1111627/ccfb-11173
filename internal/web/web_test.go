package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ptpsim/internal/engine"
	"ptpsim/internal/experiment"
	"ptpsim/internal/ptp"
	"ptpsim/internal/scenario"
)

// TestSnapshotAndHandler builds a run through the public experiment API,
// renders every dashboard view, and asserts the HTTP layer serves both the
// JSON snapshot and the embedded static page.
func TestSnapshotAndHandler(t *testing.T) {
	s := scenario.Spec{
		Name: "web", Kind: scenario.LinearOC, Seed: 31,
		Duration: engine.Sec(8), Delay: ptp.DelayE2E, TwoStep: true,
		LinkDelay: engine.Us(15), ChainDepth: 1,
	}
	_, mesh, err := experiment.Run(s)
	if err != nil {
		t.Fatal(err)
	}
	snap := BuildSnapshot(s.Name, mesh, s.Duration, nil)
	if len(snap.Topo.Nodes) == 0 || len(snap.Topo.Edges) == 0 {
		t.Fatal("topology view is empty")
	}
	if len(snap.Ports) == 0 {
		t.Fatal("port view is empty")
	}
	if len(snap.Timeline) == 0 {
		t.Fatal("timeline view is empty")
	}
	if len(snap.Elections) == 0 {
		t.Fatal("election view is empty")
	}
	if snap.FinalGM == "" {
		t.Fatal("no elected grandmaster in snapshot")
	}

	// JSON must round-trip without error (the front-end contract).
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var back Snapshot
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if back.FinalGM != snap.FinalGM {
		t.Fatal("snapshot did not round-trip")
	}

	srv := httptest.NewServer(Handler(snap))
	defer srv.Close()

	r, err := http.Get(srv.URL + "/api/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("snapshot status = %d", r.StatusCode)
	}
	var viaHTTP Snapshot
	if err := json.NewDecoder(r.Body).Decode(&viaHTTP); err != nil {
		t.Fatal(err)
	}
	if viaHTTP.SpecName != s.Name {
		t.Fatal("HTTP snapshot mismatch")
	}

	// Static index is served at the root.
	ir, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer ir.Body.Close()
	if ir.StatusCode != http.StatusOK {
		t.Fatalf("index status = %d", ir.StatusCode)
	}
}
