package re2

import (
	"strings"
	"testing"
)

// Benchmarks shaped like the betterleaks/gitleaks hot path: FindAllStringIndex
// over fragment-sized haystacks, dominated by non-matching scans.

var leaksPatterns = map[string]string{
	// The dominant CPU consumer in gitleaks configs.
	"generic": `(?i)[\w.-]{0,50}?(?:access|auth|api|credential|creds|key|passw(?:or)?d|secret|token)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=|>|:{1,3}=|\|\||:|=>|\?=|,)[\x60'"\s=]{0,5}([\w.=-]{10,150}|[a-z0-9][a-z0-9+/]{11,}={0,3})(?:[\x60'"\s;]|\\[nr]|$)`,
	// Anchored literal prefix, cheap prefilter inside RE2.
	"aws": `\b(A3T[A-Z0-9]|AKIA|ASIA|ABIA|ACCA)[A-Z0-9]{16}\b`,
	// Mid-complexity with literal prefix.
	"github-pat": `ghp_[0-9a-zA-Z]{36}`,
}

const codeLine = "func (d *Detector) detectFragment(fragment Fragment, currentRaw string) []report.Finding {\n\tvar findings []report.Finding\n\t// iterate rules and scan the fragment for candidate secrets\n"

func leaksHaystack(n int, withHit bool) string {
	var sb strings.Builder
	for sb.Len() < n {
		sb.WriteString(codeLine)
	}
	s := sb.String()[:n]
	if withHit {
		mid := n / 2
		return s[:mid] + `apiKey := "AKIAIOSFODNN7EXAMPLE"; secret_token = "ghp_0123456789abcdefghijABCDEFGHIJ456789"` + s[mid:]
	}
	return s
}

func BenchmarkLeaksScan(b *testing.B) {
	sizes := []struct {
		name string
		n    int
	}{
		{"1KB", 1 << 10},
		{"16KB", 16 << 10},
	}
	for name, pat := range leaksPatterns {
		re := MustCompileBenchmark(pat)
		for _, sz := range sizes {
			for _, hit := range []bool{false, true} {
				hs := leaksHaystack(sz.n, hit)
				label := name + "/" + sz.name + "/miss"
				if hit {
					label = name + "/" + sz.name + "/hit"
				}
				b.Run(label, func(b *testing.B) {
					b.SetBytes(int64(len(hs)))
					b.ReportAllocs()
					for range b.N {
						re.FindAllStringIndex(hs, -1)
					}
				})
			}
		}
	}
}

func BenchmarkLeaksMatchMiss(b *testing.B) {
	re := MustCompileBenchmark(leaksPatterns["generic"])
	hs := leaksHaystack(16<<10, false)
	b.SetBytes(int64(len(hs)))
	b.ReportAllocs()
	for range b.N {
		if re.MatchString(hs) {
			b.Fatal("unexpected match")
		}
	}
}
