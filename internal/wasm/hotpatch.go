package wasm2go

import (
	"bytes"
	"unsafe"
)

// Hand-written overrides for hot wasi-libc primitives in the generated module.
// The generated counterparts are renamed with a "Generated" suffix by
// buildtools/wasm2go-postpatch.sh, which must be re-run after regenerating
// libcre2.wasm.go (see buildtools/wasm/Dockerfile pipeline).
//
// Override contract: bit-identical results and the same out-of-bounds
// behavior class (Go slice panic) as the transpiled original.

// fn30 is wasi-libc memchr(s, c, n): returns a pointer to the first
// occurrence of the byte c in [s, s+n), or 0 when absent. The transpiled
// original is a scalar SWAR loop; bytes.IndexByte uses SIMD.
func (m *Module) fn30(v0, v1, v2 int32) int32 {
	if v2 == 0 {
		return 0
	}
	var s []byte
	if unsafeMemFast {
		// Unchecked construction mirroring load8u: the scan window of any
		// well-formed memchr call lies inside the guarded reservation, and a
		// wild one faults there exactly like a direct access would.
		s = unsafe.Slice((*byte)(unsafe.Add(m.mbase(), uintptr(uint32(v0)))), uint32(v2))
	} else {
		mem := *m.memory
		s = mem[int64(uint32(v0)) : int64(uint32(v0))+int64(uint32(v2))]
	}
	if i := bytes.IndexByte(s, byte(v1)); i >= 0 {
		return v0 + int32(i)
	}
	return 0
}

// Silence the unused warning for the retained generated implementation while
// keeping it available as the reference for differential testing.
var _ = (*Module).fn30Generated
