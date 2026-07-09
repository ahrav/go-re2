package re2

// Concurrency tests for the 8-way striped module pool (cache-line-padded
// LIFO stripes selected by an atomic round-robin counter). Combined with the
// persistent per-module scratch arena, these stress module ownership and
// scratch aliasing under many goroutines. Run under `go test -race`.

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestConcurrentSharedRegexpDifferential hammers ONE shared *Regexp from many
// goroutines with varied inputs, each checking against the stdlib oracle. This
// stresses the sharded pool (modules acquired/returned/stolen across P's) and
// the per-module scratch (each op must own its module exclusively). Run under
// -race to catch aliasing.
func TestConcurrentSharedRegexpDifferential(t *testing.T) {
	pat := `(?P<num>\d+)\.(?P<word>[a-z]+)`
	re := MustCompile(pat)
	std := regexp.MustCompile(pat)

	inputs := make([]string, 64)
	for i := range inputs {
		switch i % 4 {
		case 0:
			inputs[i] = strconv.Itoa(i) + "." + strings.Repeat("a", i%17+1)
		case 1:
			inputs[i] = strings.Repeat("7", i%50+1) + ".zed"
		case 2:
			inputs[i] = "no-digits-here"
		case 3:
			inputs[i] = strings.Repeat("x", i%200) + " 123.abc " + strings.Repeat("y", i%13)
		}
	}

	const workers = 48
	var wg sync.WaitGroup
	wg.Add(workers)
	errCh := make(chan string, workers)
	for w := range workers {
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				in := inputs[(seed+i)%len(inputs)]
				got := re.FindStringSubmatch(in)
				want := std.FindStringSubmatch(in)
				if !eqStr(got, want) {
					errCh <- "input=" + strconv.Quote(in)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	if msg, bad := <-errCh; bad {
		t.Fatalf("concurrent differential mismatch: %s", msg)
	}
}

// TestConcurrentDistinctRegexps stresses many distinct patterns compiled and
// matched concurrently — exercises module reuse across different ABIs and the
// scratch grow path under contention.
func TestConcurrentDistinctRegexps(t *testing.T) {
	pats := []string{
		`\d+`, `[a-z]+`, `(foo|bar|baz)`, `^\s*#`, `\b\w{3,8}\b`,
		`(\d{1,3}\.){3}\d{1,3}`, `[A-Z][a-z]+`, `a+b+c+`,
	}
	var wg sync.WaitGroup
	errCh := make(chan string, len(pats)*8)
	for r := 0; r < 8; r++ {
		for _, p := range pats {
			wg.Add(1)
			go func(pat string) {
				defer wg.Done()
				re := MustCompile(pat)
				std := regexp.MustCompile(pat)
				for i := 0; i < 200; i++ {
					in := strings.Repeat("z", i%64) + " 10.0.0.1 Foo abc123 #comment " + strings.Repeat("q", i%9)
					if re.MatchString(in) != std.MatchString(in) {
						errCh <- pat
						return
					}
				}
			}(p)
		}
	}
	wg.Wait()
	close(errCh)
	if p, bad := <-errCh; bad {
		t.Fatalf("distinct-regexp mismatch for pattern %q", p)
	}
}
