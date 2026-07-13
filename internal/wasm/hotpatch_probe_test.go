package wasm2go

import (
	"math/rand"
	"testing"
)

// TestFn30MatchesGenerated differentially tests the memchr override against
// the transpiled original over randomized (offset, byte, length) cases.
func TestFn30MatchesGenerated(t *testing.T) {
	mem := NewHostMemoryWithMax(4)
	m := &Module{memory: mem.Slice()}
	mem.Grow(3, 0)
	buf := *m.memory
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 200000; i++ {
		off := rng.Intn(len(buf) - 512)
		n := rng.Intn(512)
		c := byte(rng.Intn(8)) // small alphabet: force hits
		for j := 0; j < n; j++ {
			buf[off+j] = byte(rng.Intn(8))
		}
		got := m.fn30(int32(off), int32(c), int32(n))
		want := m.fn30Generated(int32(off), int32(c), int32(n))
		if got != want {
			t.Fatalf("fn30(%d,%d,%d)=%d want %d", off, c, n, got, want)
		}
	}
}
