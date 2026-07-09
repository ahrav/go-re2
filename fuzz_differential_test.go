package re2

// Differential and self-consistency fuzzing of the wazero fast path, targeting
// the persistent per-module scratch buffer, the per-P sharded module pool, and
// the reused callStack. All three make one goroutine's operation reuse memory
// (a wasm scratch buffer, a module, an arg/result buffer) that an earlier
// operation touched. The failure mode is a query whose result depends on what
// ran before it — most dangerously a smaller op reading stale tail bytes left
// by a larger one.
//
// Two oracles, deliberately complementary:
//
//   FuzzScratchSelfConsistency — the engine against ITSELF. A given (pattern,
//     input) must return the same result no matter what operation preceded it.
//     This is false-positive-free: it makes no assumption about regex semantics,
//     so it holds on the unmodified library (no reuse) and can only fail if
//     buffer/module reuse corrupts a result. This is the primary gate.
//
//   FuzzWazeroDifferential — the engine against the Go stdlib regexp oracle
//     (go-re2 is a drop-in for it). It fuzzes the INPUT strings against a
//     curated set of patterns known to agree with stdlib, and restricts inputs
//     to ASCII. Both restrictions dodge pre-existing RE2-vs-Go semantic
//     differences (invalid UTF-8 bytes, Unicode-property classes like \pC) that
//     exist in the unmodified library and are out of scope here. Input length
//     and match positions — not byte values — are what stress scratch reuse, so
//     ASCII inputs exercise the at-risk machinery fully. A broad correctness net
//     on top of the targeted self-consistency check.
//
// Run seed corpus every build:  go test -run 'FuzzScratchSelfConsistency|FuzzWazeroDifferential'
// Hunt new inputs:              go test -run x -fuzz FuzzScratchSelfConsistency

import (
	"regexp"
	"strings"
	"testing"
)

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func sameInts(a, b []int) bool {
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

func same2D(a, b [][]int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameInts(a[i], b[i]) {
			return false
		}
	}
	return true
}

// queryResult is the full observable result of the index-returning query set
// for one input. Index methods are chosen because their offsets are written
// into the wasm match-result array that lives in the reused scratch buffer, so
// a stale-byte or offset corruption surfaces here.
type queryResult struct {
	match       bool
	findIndex   []int
	submatchIdx []int
	allIndex    [][]int
	allSubIdx   [][]int
}

func query(re *Regexp, in string) queryResult {
	return queryResult{
		match:       re.MatchString(in),
		findIndex:   re.FindStringIndex(in),
		submatchIdx: re.FindStringSubmatchIndex(in),
		allIndex:    re.FindAllStringIndex(in, -1),
		allSubIdx:   re.FindAllStringSubmatchIndex(in, -1),
	}
}

func (r queryResult) equal(o queryResult) bool {
	return r.match == o.match &&
		sameInts(r.findIndex, o.findIndex) &&
		sameInts(r.submatchIdx, o.submatchIdx) &&
		same2D(r.allIndex, o.allIndex) &&
		same2D(r.allSubIdx, o.allSubIdx)
}

var fuzzSeeds = []struct{ pat, a, b, c string }{
	{`(\d+)-([a-z]+)`, strings.Repeat("9", 4096) + "-" + strings.Repeat("z", 4096), "1-a", ""},
	{`x*Y`, strings.Repeat("x", 50000) + "Y", "xY", "Y"},
	{`(?P<n>\d+)\.(?P<w>[a-z]+)`, "123.abc", "no dots here", "7.z"},
	{`a|b|c`, "abcabcabc", "", "zzz"},
	{`\b\w+\b`, "the quick brown fox 123", "x", "   "},
	{`(a+)(b+)?`, strings.Repeat("a", 1000) + "bbb", "ab", "b"},
	{`[^\n]*`, strings.Repeat("k", 8192), "line", ""},
	{`(?i)HELLO`, "hello HELLO HeLLo", "H", "goodbye"},
	{`(\w)(\w)(\w)`, "abcdef", "ab", "z"},
	{`^(foo)?(bar)$`, "foobar", "bar", "foo"},
}

// FuzzScratchSelfConsistency proves a query's result is independent of any
// operation that preceded it on the shared scratch/module/callStack. For each
// input it records the first-seen result, then interleaves a scratch-polluting
// large input and re-runs; any divergence from the first result is a reuse bug.
func FuzzScratchSelfConsistency(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s.pat, s.a, s.b, s.c)
	}
	f.Fuzz(func(t *testing.T, pat, a, b, c string) {
		re, err := Compile(pat)
		if err != nil {
			return
		}
		const maxIn = 1 << 20
		if len(a) > maxIn || len(b) > maxIn || len(c) > maxIn {
			return
		}

		// A large input to grow+fill a module's scratch and leave stale tail
		// bytes behind for the smaller inputs to (incorrectly) read.
		big := a
		if n := len(a); n > 0 && n < 4096 {
			big = strings.Repeat(a, 64)
		}
		if len(big) > maxIn {
			big = a
		}

		inputs := []string{a, b, c, ""}
		reference := make(map[string]queryResult, len(inputs))
		for _, in := range inputs {
			reference[in] = query(re, in)
		}

		// Adversarial schedule: pollute with big, then re-run each input; also
		// run each input back-to-back (idempotence). Every result must match the
		// reference captured above. Pollution slots are marked explicitly rather
		// than by value equality: when big == a (empty or >=4096-byte a), a
		// value check would silently skip the reference input's own comparison.
		type step struct {
			in      string
			pollute bool
		}
		schedule := []step{
			{big, true},
			{a, false},
			{a, false},
			{b, false},
			{big, true},
			{c, false},
			{"", false},
			{b, false},
			{big, true},
			{a, false},
		}
		for _, s := range schedule {
			if s.pollute {
				_ = query(re, s.in) // pollute; big's own result need not be in reference
				continue
			}
			got := query(re, s.in)
			if !got.equal(reference[s.in]) {
				t.Fatalf("result for input %q changed after preceding op (scratch/module reuse bug); pat=%q", s.in, pat)
			}
		}
	})
}

// diffPatterns are patterns whose ASCII-input semantics are identical in go-re2
// and the stdlib. They cover literals, char classes, alternation, anchors,
// groups/submatches, quantifiers, case-insensitivity, and word boundaries —
// the constructs that drive the match-offset machinery in the reused scratch.
var diffPatterns = []string{
	`(\d+)-([a-z]+)`,
	`x*Y`,
	`(?P<n>\d+)\.(?P<w>[a-z]+)`,
	`a|b|c`,
	`\b\w+\b`,
	`(a+)(b+)?`,
	`[^\n]*`,
	`(?i)hello`,
	`(\w)(\w)(\w)`,
	`^(foo)?(bar)$`,
	`\s+`,
	`[A-Za-z0-9_.%+-]+@[A-Za-z0-9.-]+`,
	`(ab)+`,
	`.*`,
}

// FuzzWazeroDifferential compares the wazero fast path to the stdlib regexp
// oracle over a size schedule that grows then reuses scratch. It fuzzes the
// input strings (ASCII) against the curated diffPatterns (see file header).
func FuzzWazeroDifferential(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s.a, s.b, s.c)
	}
	f.Add("email me@host.com and you@a.b", "no-at-sign", "a@b")
	f.Add(strings.Repeat("ababab", 500), "abc", "")

	f.Fuzz(func(t *testing.T, a, b, c string) {
		if !isASCII(a) || !isASCII(b) || !isASCII(c) {
			return
		}
		const maxIn = 1 << 20
		if len(a) > maxIn || len(b) > maxIn || len(c) > maxIn {
			return
		}
		big := a
		if n := len(a); n > 0 && n < 4096 {
			big = strings.Repeat(a, 64)
		}
		if len(big) > maxIn {
			big = a
		}
		schedule := []string{big, a, b, c, a, "", c, b}

		for _, pat := range diffPatterns {
			std := regexp.MustCompile(pat)
			re := MustCompile(pat)
			for _, in := range schedule {
				if len(in) > maxIn {
					continue
				}
				g, w := query(re, in), queryResult{
					match:       std.MatchString(in),
					findIndex:   std.FindStringIndex(in),
					submatchIdx: std.FindStringSubmatchIndex(in),
					allIndex:    std.FindAllStringIndex(in, -1),
					allSubIdx:   std.FindAllStringSubmatchIndex(in, -1),
				}
				if !g.equal(w) {
					t.Fatalf("go-re2 disagrees with stdlib on input %q; pat=%q", in, pat)
				}
			}
		}
	})
}
