package re2

// Regression guard for the zero-allocation hot path established by the reused
// per-module callStack. After warmup (scratch grown, pool populated), a steady-
// state MatchString / Match must not allocate on the Go heap. If a future change
// reintroduces a per-op escape (as the pre-reuse `var callStack [8]uint64` did),
// this fails instead of silently regressing throughput under GC pressure.

import "testing"

func TestHotPathZeroAllocs(t *testing.T) {
	re := MustCompile(`\d+`)

	// Warm up: grow the scratch buffer and populate the module pool so the
	// measured runs are pure steady state.
	for i := 0; i < 200; i++ {
		re.MatchString("abc 12345 def")
		re.Match([]byte("abc 12345 def"))
	}

	if got := testing.AllocsPerRun(2000, func() {
		re.MatchString("abc 12345 def")
	}); got != 0 {
		t.Fatalf("MatchString hot path = %v allocs/op, want 0 (callStack/scratch reuse regressed?)", got)
	}

	b := []byte("abc 12345 def")
	if got := testing.AllocsPerRun(2000, func() {
		re.Match(b)
	}); got != 0 {
		t.Fatalf("Match hot path = %v allocs/op, want 0 (callStack/scratch reuse regressed?)", got)
	}
}
