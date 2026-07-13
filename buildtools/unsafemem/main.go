// Command unsafemem rewrites checked linear-memory accesses in selected hot
// functions of internal/wasm/libcre2.wasm.go into unchecked base+offset
// accesses through the helpers in internal/wasm/hotmem.go.
//
// Usage: go run ./buildtools/unsafemem -file internal/wasm/libcre2.wasm.go -fns fn586,fn587
//
// It must be re-run after regenerating the file (like wasm2go-postpatch.sh).
// The transformation is sound only with the guard reservation documented in
// internal/wasm/hotmem_fast.go; on other platforms the helpers fall back to
// checked accesses, so the rewrite is always behavior-preserving.
package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// maxConstOffset is the largest constant addend allowed in a rewritten
// access; anything larger could step past the 64 KiB guard tail and must
// keep its bounds check.
const maxConstOffset = 65536 - 8

var (
	reLoad    = regexp.MustCompile(`\b(load(?:8|16|32|64))\(\(\*m\.memory\)\[([^\[\]:]+):\]\)`)
	reStore   = regexp.MustCompile(`\b(store(?:8|16|32|64))\(\(\*m\.memory\)\[([^\[\]:]+):\], `)
	reAtomicL = regexp.MustCompile(`\batomic_load(32|64)\(\*m\.memory, `)
	reAtomicS = regexp.MustCompile(`\batomic_store(32|64)\(\*m\.memory, `)
	reByteSet = regexp.MustCompile(`^(\s*)\(\*m\.memory\)\[([^\[\]:]+)\] = (.+)$`)
	reByteGet = regexp.MustCompile(`\(\*m\.memory\)\[([^\[\]:]+)\]`)
	reConst   = regexp.MustCompile(`\+(\d+)$`)
)

// asOffset normalizes a wasm2go index expression to an int64-typed offset
// expression, and reports whether its constant addend is guard-safe.
func asOffset(expr string) (string, bool) {
	expr = strings.TrimSpace(expr)
	if m := reConst.FindStringSubmatch(expr); m != nil {
		c, err := strconv.Atoi(m[1])
		if err != nil || c > maxConstOffset {
			return "", false
		}
	}
	switch {
	case strings.HasPrefix(expr, "int64(") :
		return expr, true
	case strings.HasPrefix(expr, "uint32("):
		return "int64(" + expr + ")", true
	default:
		// Unrecognized shape: leave checked.
		return "", false
	}
}

// balancedArg scans s for one balanced expression ending at ", " or ")" at
// depth 0, returning the expression and the rest.
func balancedArg(s string, stops ...byte) (string, int) {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[':
			depth++
		case ')', ']':
			if depth == 0 {
				return s[:i], i
			}
			depth--
		case ',':
			if depth == 0 {
				return s[:i], i
			}
		}
	}
	return "", -1
}

func rewriteBody(name, body string) (string, int, error) {
	rewrites := 0

	// loadN((*m.memory)[EXPR:]) -> m.loadNu(mbase, OFF)
	body = reLoad.ReplaceAllStringFunc(body, func(s string) string {
		m := reLoad.FindStringSubmatch(s)
		off, ok := asOffset(m[2])
		if !ok {
			return s
		}
		rewrites++
		return "m." + m[1] + "u(mbase, " + off + ")"
	})

	// storeN((*m.memory)[EXPR:], VAL) -> m.storeNu(mbase, OFF, VAL)
	// The VAL tail (up to the closing paren) is untouched, so only the head
	// needs rewriting.
	body = reStore.ReplaceAllStringFunc(body, func(s string) string {
		m := reStore.FindStringSubmatch(s)
		off, ok := asOffset(m[2])
		if !ok {
			return s
		}
		rewrites++
		return "m." + m[1] + "u(mbase, " + off + ", "
	})

	// atomic_loadN(*m.memory, OFF) -> m.atomicLoadNu(mbase, OFF)
	for searchFrom := 0; ; {
		loc := reAtomicL.FindStringSubmatchIndex(body[searchFrom:])
		if loc == nil {
			break
		}
		start := searchFrom + loc[0]
		argsAt := searchFrom + loc[1]
		width := body[searchFrom+loc[2] : searchFrom+loc[3]]
		rest := body[argsAt:]
		offExpr, adv := balancedArg(rest)
		if adv < 0 || rest[adv] != ')' {
			return "", 0, fmt.Errorf("%s: unparsable atomic_load args near %q", name, rest[:min(len(rest), 60)])
		}
		off, ok := asOffset(offExpr)
		if !ok {
			searchFrom = argsAt
			continue
		}
		rewrites++
		body = body[:start] + "m.atomicLoad" + width + "u(mbase, " + off + ")" + rest[adv+1:]
		searchFrom = start
	}

	// atomic_storeN(*m.memory, OFF, VAL) -> m.atomicStoreNu(mbase, OFF, VAL)
	for {
		loc := reAtomicS.FindStringSubmatchIndex(body)
		if loc == nil {
			break
		}
		width := body[loc[2]:loc[3]]
		rest := body[loc[1]:]
		offExpr, adv := balancedArg(rest)
		if adv < 0 || rest[adv] != ',' {
			return "", 0, fmt.Errorf("%s: unparsable atomic_store args near %q", name, rest[:min(len(rest), 60)])
		}
		off, ok := asOffset(offExpr)
		if !ok {
			// Leave checked: neutralize the match to avoid an infinite loop.
			body = body[:loc[0]] + "atomic_storeKEEP" + width + "(*m.memory, " + body[loc[1]:]
			continue
		}
		rewrites++
		body = body[:loc[0]] + "m.atomicStore" + width + "u(mbase, " + off + "," + rest[adv+1:]
	}
	body = strings.ReplaceAll(body, "atomic_storeKEEP", "atomic_store")

	// Byte store lines: (*m.memory)[IDX] = VAL
	lines := strings.Split(body, "\n")
	for i, ln := range lines {
		if m := reByteSet.FindStringSubmatch(ln); m != nil {
			off, ok := asOffset(m[2])
			if !ok {
				continue
			}
			rewrites++
			lines[i] = m[1] + "m.store8u(mbase, " + off + ", " + m[3] + ")"
		}
	}
	body = strings.Join(lines, "\n")

	// Byte loads in expressions: (*m.memory)[IDX]
	body = reByteGet.ReplaceAllStringFunc(body, func(s string) string {
		m := reByteGet.FindStringSubmatch(s)
		off, ok := asOffset(m[1])
		if !ok {
			return s
		}
		rewrites++
		return "m.load8u(mbase, " + off + ")"
	})

	return body, rewrites, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func main() {
	file := flag.String("file", "internal/wasm/libcre2.wasm.go", "generated file to rewrite")
	fns := flag.String("fns", "", "comma-separated function names (e.g. fn586,fn587)")
	all := flag.Bool("all", false, "rewrite every method on *Module in the file")
	flag.Parse()

	srcBytes, err := os.ReadFile(*file)
	if err != nil {
		panic(err)
	}
	src := string(srcBytes)

	var targets []string
	if *all {
		for _, m := range regexp.MustCompile(`(?m)^func \(m \*Module\) (\w+)\(`).FindAllStringSubmatch(src, -1) {
			targets = append(targets, m[1])
		}
	} else if *fns != "" {
		targets = strings.Split(*fns, ",")
	} else {
		fmt.Fprintln(os.Stderr, "unsafemem: -fns or -all required")
		os.Exit(2)
	}

	total := 0
	for _, fn := range targets {
		fn = strings.TrimSpace(fn)
		marker := "func (m *Module) " + fn + "("
		i := strings.Index(src, marker)
		if i < 0 {
			fmt.Fprintf(os.Stderr, "unsafemem: %s not found\n", fn)
			os.Exit(1)
		}
		j := strings.Index(src[i:], "\nfunc ")
		if j < 0 {
			j = len(src) - i
		}
		body := src[i : i+j]
		hoisted := strings.Contains(body, "mbase := m.mbase()")
		newBody, n, err := rewriteBody(fn, body)
		if err != nil {
			fmt.Fprintln(os.Stderr, "unsafemem:", err)
			os.Exit(1)
		}
		if n == 0 {
			continue
		}
		if !hoisted {
			// Inject the invariant base hoist right after the signature line.
			nl := strings.IndexByte(newBody, '\n')
			newBody = newBody[:nl+1] + "\tmbase := m.mbase()\n" + newBody[nl+1:]
		}
		src = src[:i] + newBody + src[i+j:]
		total += n
		fmt.Printf("unsafemem: %s: %d accesses rewritten\n", fn, n)
	}
	if err := os.WriteFile(*file, []byte(src), 0o644); err != nil {
		panic(err)
	}
	fmt.Printf("unsafemem: total %d\n", total)
}
