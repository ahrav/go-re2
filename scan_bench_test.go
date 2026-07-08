package re2

import (
	"strings"
	"testing"
)

// Benchmarks that exercise RE2's byte-scanning inner loops on larger (~11KB)
// inputs, complementing the tiny-input BenchmarkParallelMatch (\d on 7 bytes)
// which is call-overhead bound. The zero-allocation property of the hot path is
// demonstrated by BenchmarkParallelMatch under -benchmem (0 allocs/op); these
// large-input cases measure scanning throughput (MB/s), not allocations.

func benchInputs() map[string]string {
	long := strings.Repeat("the quick brown fox jumps over the lazy dog 0123456789 ", 200) // ~11KB
	return map[string]string{
		"literal_scan_11k":   long,                       // scan for a literal late in text
		"charclass_scan_11k": long,                       // char-class scan
		"nomatch_11k":        strings.Repeat("x", 11000), // worst-case no-match scan
	}
}

func BenchmarkScanLargeParallel(b *testing.B) {
	cases := []struct{ name, pat, inputKey string }{
		{"literal", `jumps`, "literal_scan_11k"},
		{"charclass", `[0-9]{6}`, "charclass_scan_11k"},
		{"prefix_ci", `(?i)LAZY`, "literal_scan_11k"},
		{"nomatch_lit", `zzqqz`, "nomatch_11k"},
		{"altern", `fox|dog|cat|bird`, "literal_scan_11k"},
	}
	inputs := benchInputs()
	for _, c := range cases {
		re := MustCompile(c.pat)
		in := inputs[c.inputKey]
		b.Run(c.name, func(b *testing.B) {
			b.SetBytes(int64(len(in)))
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					re.MatchString(in)
				}
			})
		})
	}
}
