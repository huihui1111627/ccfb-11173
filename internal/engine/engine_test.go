package engine

import "testing"

// TestEventOrdering: same-time events fire in insertion (sequence) order, and
// the heap always delivers the earliest event first.
func TestEventOrdering(t *testing.T) {
	var got []int
	e := New()
	e.Schedule(Ns(100), func() { got = append(got, 3) })
	e.Schedule(Ns(10), func() { got = append(got, 1) })
	e.Schedule(Ns(10), func() { got = append(got, 2) })
	e.Schedule(Ns(50), func() { got = append(got, 4) })
	e.Run(Ns(200))
	want := []int{1, 2, 4, 3}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// TestRunMaxTime: events strictly after maxTime remain pending and Now() is
// clamped to the requested horizon.
func TestRunMaxTime(t *testing.T) {
	e := New()
	fired := false
	e.Schedule(Ns(200), func() { fired = true })
	e.Schedule(Ns(50), func() {})
	final := e.Run(Ns(100))
	if final != Ns(100) {
		t.Fatalf("final time = %v, want 100", final)
	}
	if fired {
		t.Fatal("event past maxTime must not run")
	}
	if e.Pending() != 1 {
		t.Fatalf("pending = %d, want 1", e.Pending())
	}
	// Continuing the run drains the remaining event.
	e.Run(Ns(300))
	if !fired {
		t.Fatal("deferred event never ran after extending the horizon")
	}
}

// TestRngDeterminism: identical seed pairs yield identical streams.
func TestRngDeterminism(t *testing.T) {
	a := Rng(123, 456)
	b := Rng(123, 456)
	for i := 0; i < 100; i++ {
		if a.Float64() != b.Float64() {
			t.Fatal("identical RNG seeds diverged")
		}
	}
}
