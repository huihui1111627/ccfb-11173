// Package engine implements a single-threaded deterministic discrete-event
// simulator. Time is an integer number of nanoseconds since the simulation
// epoch. Ties on event time are broken by a monotonically increasing sequence
// number so that, for a given scenario seed, the execution order is exactly
// reproducible.
package engine

import (
	"container/heap"
	"math/rand/v2"
)

// SimTime is simulated time in nanoseconds.
type SimTime int64

// Seconds converts a SimTime to a floating-point number of seconds.
func (t SimTime) Seconds() float64 { return float64(t) / 1e9 }

// Ns is a convenience constructor.
func Ns(n int64) SimTime { return SimTime(n) }

// Us builds a SimTime from microseconds.
func Us(us float64) SimTime { return SimTime(us * 1e3) }

// Ms builds a SimTime from milliseconds.
func Ms(ms float64) SimTime { return SimTime(ms * 1e6) }

// Sec builds a SimTime from seconds.
func Sec(s float64) SimTime { return SimTime(s * 1e9) }

type event struct {
	time SimTime
	seq  int64
	run  func()
}

type eventHeap []event

func (h eventHeap) Len() int { return len(h) }
func (h eventHeap) Less(i, j int) bool {
	if h[i].time != h[j].time {
		return h[i].time < h[j].time
	}
	return h[i].seq < h[j].seq
}
func (h eventHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *eventHeap) Push(x any)   { *h = append(*h, x.(event)) }
func (h *eventHeap) Pop() any {
	old := *h
	n := len(old)
	e := old[n-1]
	*h = old[:n-1]
	return e
}

// Engine drives the event loop.
type Engine struct {
	now  SimTime
	seq  int64
	q    eventHeap
	stop bool
}

// New creates an empty engine.
func New() *Engine {
	e := &Engine{}
	heap.Init(&e.q)
	return e
}

// Now returns the current simulated time.
func (e *Engine) Now() SimTime { return e.now }

// Schedule registers cb to run after delay d (d >= 0).
func (e *Engine) Schedule(d SimTime, cb func()) {
	if d < 0 {
		d = 0
	}
	e.seq++
	heap.Push(&e.q, event{time: e.now + d, seq: e.seq, run: cb})
}

// At registers cb to run at absolute time t.
func (e *Engine) At(t SimTime, cb func()) {
	e.Schedule(t-e.now, cb)
}

// Stop requests the loop to terminate after the current event completes.
func (e *Engine) Stop() { e.stop = true }

// Pending reports how many events are queued.
func (e *Engine) Pending() int { return e.q.Len() }

// Run processes events until maxTime is reached, the queue drains, or Stop is
// called. Returns the final simulated time.
func (e *Engine) Run(maxTime SimTime) SimTime {
	for e.q.Len() > 0 && !e.stop {
		ev := heap.Pop(&e.q).(event)
		if ev.time > maxTime {
			// Put it back: nothing later can be earlier.
			heap.Push(&e.q, ev)
			e.now = maxTime
			return e.now
		}
		e.now = ev.time
		ev.run()
	}
	if e.now < maxTime && e.stop {
		// leave now as-is
	}
	return e.now
}

// Rng builds an independent deterministic random source from two seeds.
// Every stochastic component (oscillator drift, phase noise, loss) must take
// its source from here rather than the global rand, which keeps runs
// reproducible and independent of scheduling order.
func Rng(seed1, seed2 uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed1, seed2))
}

// Split derives two non-overlapping sub-seeds from a master seed pair.
func Split(seed1, seed2, salt uint64) (uint64, uint64) {
	// mix function (splitmix64-style)
	mix := func(x uint64) uint64 {
		x += 0x9e3779b97f4a7c15
		x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
		x = (x ^ (x >> 27)) * 0x94d049bb133111eb
		return x ^ (x >> 31)
	}
	return mix(seed1 ^ (salt * 0x9e3779b97f4a7c15)), mix(seed2 ^ (salt * 0xd1b54a32d192ed03))
}
