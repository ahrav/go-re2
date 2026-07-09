package re2

import (
	"testing"
)

// Parallel version of the leaks-shaped benchmark: betterleaks scans fragments
// from 32 git workers concurrently, so contention on shared wasm state
// (module pool, wasm malloc lock) is part of the real cost.

func BenchmarkLeaksScanParallel(b *testing.B) {
	re := MustCompile(leaksPatterns["generic"])
	hs := leaksHaystack(16<<10, false)
	b.SetBytes(int64(len(hs)))
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			re.FindAllStringIndex(hs, -1)
		}
	})
}

func BenchmarkLeaksMatchParallel(b *testing.B) {
	re := MustCompile(leaksPatterns["generic"])
	hs := leaksHaystack(4<<10, false)
	b.SetBytes(int64(len(hs)))
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if re.MatchString(hs) {
				b.Fatal("unexpected match")
			}
		}
	})
}

// Small haystack parallel: per-op overhead (malloc/free, pool) dominates over
// DFA scan time, worst case for the wazero backend.
func BenchmarkLeaksMatchParallelSmall(b *testing.B) {
	re := MustCompile(leaksPatterns["github-pat"])
	hs := leaksHaystack(256, false)
	b.SetBytes(int64(len(hs)))
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if re.MatchString(hs) {
				b.Fatal("unexpected match")
			}
		}
	})
}
