package re2

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestBackendDigest is a compact, deterministic cross-backend behavior oracle.
// Keep the corpus and formatting stable so benchmark binaries can be compared
// byte-for-byte across stdlib, wasm2go, wazero, and cgo builds.
func TestBackendDigest(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
	}{
		{`a(a+b+)b`, "acbbaaabbdd"},
		{`(?m)^([[:alpha:]_][[:alnum:]_]*)\s*=\s*(.+)$`, "name = grotto\ncount=42\n"},
		{`(?:foo|bar|baz)+[0-9]{2,4}`, strings.Repeat("quux-", 256) + "foobaz2026"},
		{`[\p{Greek}]+`, "latin-Καλημέρα-world"},
		{leaksPatterns["generic"], leaksHaystack(16<<10, true)},
	}

	h := sha256.New()
	for i, tc := range tests {
		re := MustCompileBenchmark(tc.pattern)
		fmt.Fprintf(h, "case=%d match=%t index=%v all=%v sub=%v repl=%q split=%q\n",
			i,
			re.MatchString(tc.input),
			re.FindStringIndex(tc.input),
			re.FindAllStringIndex(tc.input, -1),
			re.FindStringSubmatchIndex(tc.input),
			re.ReplaceAllString(tc.input, "<$0>"),
			re.Split(tc.input, -1),
		)
	}
	digest := fmt.Sprintf("%x", h.Sum(nil))
	if os.Getenv("RE2_PRINT_DIGEST") == "1" {
		t.Logf("backend digest: %s", digest)
	}
	const want = "d0eb22c9d583275ded365c06269c8435bdd2db97775d96367553ff8a9cd5f4da"
	if digest != want {
		t.Fatalf("backend digest = %s, want %s", digest, want)
	}
}
