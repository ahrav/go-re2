package re2

// Concurrency test for the per-P sharded module pool across a range of
// GOMAXPROCS settings. numModShard is fixed at wasm-init time to the initial
// GOMAXPROCS, and shardIndex maps the running P with `pid % numModShard`, so:
//
//   - GOMAXPROCS below the shard count leaves some shards idle (must still be
//     correct),
//   - GOMAXPROCS above it makes multiple P's alias one shard via the modulo
//     (must still be correct),
//   - and running more goroutines than P's forces goroutine migration, so a
//     module freed on one P is popped on another — exercising cross-shard
//     stealing in popChildModule.
//
// Under every setting, many goroutines sharing one *Regexp must agree with the
// stdlib oracle. Run under -race to catch module-ownership / scratch aliasing.

import (
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestPoolDifferentialAcrossGOMAXPROCS(t *testing.T) {
	orig := runtime.GOMAXPROCS(0)
	defer runtime.GOMAXPROCS(orig)

	pat := `(?P<num>\d+)\.(?P<word>[a-z]+)`
	re := MustCompile(pat)
	std := regexp.MustCompile(pat)

	// Mix small inputs with large ones so worker goroutines grow scratch on
	// many distinct modules (maximizing cross-shard reuse pressure).
	inputs := make([]string, 96)
	for i := range inputs {
		switch i % 6 {
		case 0:
			inputs[i] = strconv.Itoa(i) + "." + strings.Repeat("a", i%23+1)
		case 1:
			inputs[i] = strings.Repeat("7", i%40+1) + ".zed"
		case 2:
			inputs[i] = "no-digits-here"
		case 3:
			inputs[i] = strings.Repeat("9", 4000) + "." + strings.Repeat("q", 4000) // large: grows scratch
		case 4:
			inputs[i] = ""
		case 5:
			inputs[i] = strings.Repeat("x", i%300) + " 123.abc " + strings.Repeat("y", i%17)
		}
	}

	settings := []int{1, 2, orig, orig * 4}
	for _, gmp := range settings {
		if gmp < 1 {
			continue
		}
		runtime.GOMAXPROCS(gmp)

		const workers = 64
		var wg sync.WaitGroup
		wg.Add(workers)
		errCh := make(chan string, workers)
		for w := 0; w < workers; w++ {
			go func(seed int) {
				defer wg.Done()
				for i := 0; i < 400; i++ {
					in := inputs[(seed*7+i)%len(inputs)]
					got := re.FindStringSubmatchIndex(in)
					want := std.FindStringSubmatchIndex(in)
					if !sameInts(got, want) {
						errCh <- "gmp=" + strconv.Itoa(gmp) + " input=" + strconv.Quote(in)
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
}
