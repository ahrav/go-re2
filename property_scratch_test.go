package re2

// Always-on randomized property test for the persistent scratch buffer. Unlike
// the fuzz targets (which need `-fuzz` to explore beyond the seed corpus), this
// runs a large, reproducible batch of adversarial large->small size schedules on
// every `go test`, asserting two properties per input:
//
//   1. Self-consistency: the result does not depend on the preceding op (the
//      scratch may hold a previous, larger input's stale tail bytes).
//   2. Oracle agreement: the result matches the Go stdlib regexp (ASCII inputs).
//
// The seed is fixed so any failure reproduces exactly.

import (
	"math/rand"
	"regexp"
	"strings"
	"testing"
)

func TestScratchReuseProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping randomized property test in -short mode")
	}
	rng := rand.New(rand.NewSource(0x5EED_1234))

	// Patterns whose ASCII semantics match the stdlib oracle.
	patterns := []string{
		`(\d+)-([a-z]+)`,
		`x*Y`,
		`(?P<n>\d+)\.(?P<w>[a-z]+)`,
		`\b\w+\b`,
		`(a+)(b+)?`,
		`[A-Za-z0-9_.%+-]+@[A-Za-z0-9.-]+`,
		`(?i)hello`,
		`^(foo)?(bar)$`,
		`.*`,
	}
	alphabet := []byte("abcXYZ0129 .-@\n_")

	randInput := func(maxLen int) string {
		n := rng.Intn(maxLen + 1)
		b := make([]byte, n)
		for i := range b {
			b[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return string(b)
	}

	const iterations = 5000
	for iter := 0; iter < iterations; iter++ {
		pat := patterns[rng.Intn(len(patterns))]
		re := MustCompile(pat)
		std := regexp.MustCompile(pat)

		// The target input, and its stdlib reference result.
		target := randInput(64)
		want := queryResult{
			match:       std.MatchString(target),
			findIndex:   std.FindStringIndex(target),
			submatchIdx: std.FindStringSubmatchIndex(target),
			allIndex:    std.FindAllStringIndex(target, -1),
			allSubIdx:   std.FindAllStringSubmatchIndex(target, -1),
		}
		ref := query(re, target)
		if !ref.equal(want) {
			t.Fatalf("iter %d: go-re2 disagrees with stdlib; pat=%q input=%q", iter, pat, target)
		}

		// Pollute the shared scratch with a much larger input, then re-run the
		// target: the result must be unchanged.
		polluter := strings.Repeat(randInput(32)+"x", 200)
		_ = query(re, polluter)
		after := query(re, target)
		if !after.equal(ref) {
			t.Fatalf("iter %d: result changed after a larger preceding op (stale scratch); pat=%q input=%q", iter, pat, target)
		}
	}
}
