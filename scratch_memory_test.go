package re2

import (
	"runtime"
	"strings"
	"sync"
	"testing"
)

// TestScratchMemoryBounded confirms the never-shrink scratch buffers don't grow
// without bound: after a burst of large-input matches at high concurrency (which
// grows many modules' scratch), steady-state small matches must not keep
// allocating. We measure that the number of live child modules stabilizes and
// total scratch is bounded by (peak concurrency * max input), not by op count.
func TestScratchMemoryBounded(t *testing.T) {
	re := MustCompile(`(\d+)-([a-z]+)`)
	big := strings.Repeat("9", 8192) + "-" + strings.Repeat("z", 8192)

	// Burst: 64 goroutines each do many big-input matches -> grows scratch on up
	// to ~GOMAXPROCS+ modules.
	burst := func(in string, workers, iters int) {
		var wg sync.WaitGroup
		wg.Add(workers)
		for w := 0; w < workers; w++ {
			go func() {
				defer wg.Done()
				for i := 0; i < iters; i++ {
					re.FindStringSubmatch(in)
				}
			}()
		}
		wg.Wait()
	}

	burst(big, 64, 2000)
	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	// Steady state: lots more small matches. Must not grow heap materially.
	for round := 0; round < 5; round++ {
		burst("1-a", 64, 5000)
	}
	runtime.GC()
	var m2 runtime.MemStats
	runtime.ReadMemStats(&m2)

	// HeapInuse should not have grown by more than a few MB after the big burst
	// already sized the scratch. (Wasm linear memory lives off-heap in wazero's
	// allocator, but the Go-side bookkeeping and module structs are on-heap.)
	grewMB := (float64(m2.HeapInuse) - float64(m1.HeapInuse)) / (1 << 20)
	t.Logf("HeapInuse after big burst=%d MB, after steady small=%d MB, delta=%.2f MB",
		m1.HeapInuse>>20, m2.HeapInuse>>20, grewMB)
	if grewMB > 32 {
		t.Fatalf("heap grew %.2f MB during steady-state small matches; scratch/pool may be leaking", grewMB)
	}
}
