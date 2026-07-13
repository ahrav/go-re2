//go:build unix && (amd64 || arm64)

package wasm2go

// unsafeMemFast enables unchecked base+offset linear-memory addressing.
// Sound only when all of the following hold (see hotmem.go):
//   - 64-bit so the full 4 GiB wasm32 address space is reservable
//   - the memory package reserves max-size + guardBytes with a PROT_NONE
//     tail (mem_unix.go), so any uint32 offset + small constant either lands
//     in linear memory or faults on a protected page
//   - the architecture supports unaligned loads/stores (amd64, arm64)
//
// On these platforms a wild guest access becomes a fatal SIGSEGV instead of
// a recoverable Go panic; RE2 itself never makes such accesses (exercised by
// the differential fuzz corpus and exhaustive suites).
const unsafeMemFast = true
