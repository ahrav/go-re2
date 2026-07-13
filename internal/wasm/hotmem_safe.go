//go:build !unix || !(amd64 || arm64)

package wasm2go

// Portable fallback: all linear-memory accesses stay bounds-checked.
const unsafeMemFast = false
