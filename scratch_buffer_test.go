package re2

// Correctness tests for the persistent per-module scratch buffer.
//
// The scratch buffer is reused across operations and only ever grows, so the
// failure mode to guard against is a smaller, later operation reading STALE
// tail bytes left behind by an earlier, larger one. These tests use the stdlib
// regexp as a reference oracle over a deliberately adversarial large->small
// size schedule. Run under `go test -race` to also catch scratch aliasing.

import (
	"regexp"
	"strings"
	"testing"
)

func eqStr(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestScratchReuseStaleBytes is the key correctness probe for the persistent
// scratch buffer. It reuses ONE *Regexp across a deliberately adversarial size
// schedule: a large input (grows + fills the scratch), then a much smaller one
// (reuses the now-large scratch, whose tail still holds the previous input's
// bytes). If the engine ever read past the current input's length into stale
// scratch, the small-input result would disagree with the stdlib oracle.
func TestScratchReuseStaleBytes(t *testing.T) {
	// A submatch pattern so the match-result array (also in scratch) is exercised.
	pat := `(\d+)-([a-z]+)`

	big := strings.Repeat("9", 4096) + "-" + strings.Repeat("z", 4096)
	sizes := []string{
		big,
		"1-a",      // tiny, reuses the big scratch
		"",         // empty
		"42-hello", // small
		big,        // big again
		"7-x",      // tiny again
		strings.Repeat("5", 100) + "-" + strings.Repeat("q", 3),
		"no match here at all",
		"1-a",
	}
	re := MustCompile(pat)
	std := regexp.MustCompile(pat)
	for i, in := range sizes {
		if got, want := re.FindStringSubmatch(in), std.FindStringSubmatch(in); !eqStr(got, want) {
			t.Fatalf("iter %d input len=%d: FindStringSubmatch=%v want %v (stale scratch?)", i, len(in), got, want)
		}
		if got, want := re.FindString(in), std.FindString(in); got != want {
			t.Fatalf("iter %d input len=%d: FindString=%q want %q (stale scratch?)", i, len(in), got, want)
		}
	}
}

// TestScratchGrowMonotonic verifies repeated growth (ascending sizes) then
// stable reuse (descending) stays correct — the geometric-grow path.
func TestScratchGrowMonotonic(t *testing.T) {
	re := MustCompile(`x*Y`)
	std := regexp.MustCompile(`x*Y`)
	for _, n := range []int{1, 10, 100, 1000, 10000, 50000, 5, 1, 20000, 2} {
		in := strings.Repeat("x", n) + "Y"
		if got, want := re.FindString(in), std.FindString(in); got != want {
			t.Fatalf("n=%d: FindString len got=%d want=%d", n, len(got), len(want))
		}
	}
}
