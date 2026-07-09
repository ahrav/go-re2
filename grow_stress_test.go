package re2

import (
	"strings"
	"sync"
	"testing"
)

// TestConcurrentGrowMatch stresses the shared-memory length cache: many
// goroutines compile fresh regexes (driving malloc/sbrk -> memory.grow) while
// others match against large inputs. A broken length cache would read past
// the committed region and trap or return wrong results.
func TestConcurrentGrowMatch(t *testing.T) {
	pat := `secret_token = "([A-Za-z0-9]+)"`
	base := MustCompile(pat)
	hay := strings.Repeat("noise line without any interesting content here;\n", 400)
	withHit := hay + `secret_token = "abcdef0123456789ABCDEF"` + "\n" + hay

	var wg sync.WaitGroup
	for g := 0; g < 48; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				if g%3 == 0 {
					// Force allocation churn -> heap growth.
					re := MustCompile(`token` + itoa(i%20+1) + `_?[a-z]{0,` + itoa(i%20+1) + `}[0-9]+`)
					_ = re.FindAllStringIndex(withHit, -1)
					_ = re
				} else {
					m := base.FindAllStringIndex(withHit, -1)
					if len(m) == 0 {
						t.Errorf("goroutine %d: expected match", g)
						return
					}
				}
			}
		}(g)
	}
	wg.Wait()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
