package ptp

import "testing"

func ann(prio1, class, acc uint8, variance uint16, prio2 uint8, id ClockIdentity, steps uint16) *AnnounceFields {
	return &AnnounceFields{
		GrandmasterPriority1:    prio1,
		GrandmasterClockQuality: ClockQuality{ClockClass: class, ClockAccuracy: acc, OffsetScaledLogVariance: variance},
		GrandmasterPriority2:    prio2,
		GrandmasterIdentity:     id,
		StepsRemoved:            steps,
	}
}

// TestCompareDatasetsOrdering walks the 9.3.5 b) field sequence: the first
// differing field decides, lower value wins.
func TestCompareDatasetsOrdering(t *testing.T) {
	a := ann(10, 6, 0x20, 0x4000, 128, "aaa", 0)
	cases := []struct {
		name string
		b    *AnnounceFields
		want int // -1: a better, +1: b better
	}{
		{"priority1 decides", ann(11, 6, 0x20, 0x4000, 128, "aaa", 0), -1},
		{"class decides", ann(10, 7, 0x20, 0x4000, 128, "aaa", 0), -1},
		{"accuracy decides", ann(10, 6, 0x21, 0x4000, 128, "aaa", 0), -1},
		{"variance decides", ann(10, 6, 0x20, 0x4001, 128, "aaa", 0), -1},
		{"priority2 decides", ann(10, 6, 0x20, 0x4000, 129, "aaa", 0), -1},
		{"identity tie-break", ann(10, 6, 0x20, 0x4000, 128, "zzz", 0), -1},
		{"b better on priority1", ann(9, 6, 0x20, 0x4000, 128, "aaa", 0), 1},
		{"fully equal", ann(10, 6, 0x20, 0x4000, 128, "aaa", 0), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompareDatasets(a, tc.b); got != tc.want {
				t.Fatalf("CompareDatasets = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestStepsRemovedTieBreak: identical grandmaster datasets are ordered by
// topology distance first, then by the identity of the sending port.
func TestStepsRemovedTieBreak(t *testing.T) {
	base := func(steps uint16, port ClockIdentity) *EForeignMaster {
		f := ann(10, 6, 0x20, 0x4000, 128, "gm", steps)
		f.OriginPortIdentity = PortIdentity{Clock: port, Port: 1}
		return &EForeignMaster{Announce: f}
	}
	near := base(1, "relay-a")
	far := base(2, "relay-a")
	if !foreignBetter(near, far) {
		t.Fatal("fewer stepsRemoved must win on tied grandmaster data")
	}
	sameStepsA := base(1, "relay-a")
	sameStepsB := base(1, "relay-b")
	if !foreignBetter(sameStepsA, sameStepsB) {
		t.Fatal("lower origin port identity must win the final tie-break")
	}
}
