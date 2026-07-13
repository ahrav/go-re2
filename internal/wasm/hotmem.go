package wasm2go

import (
	"math/bits"
	"sync/atomic"
	"unsafe"
)

// Unchecked linear-memory access helpers used by hot transpiled functions
// rewritten by buildtools/unsafemem (see also hotmem_fast.go / hotmem_safe.go
// for the platform gate and the soundness argument).
//
// When unsafeMemFast is false every helper falls back to the same checked
// slice accesses the generator emits, so behavior is identical on platforms
// without the guard reservation. The branches fold away at compile time.
//
// Offsets are the wasm 33-bit effective address: int64(uint32(dynamic)) + C
// with C asserted ≤ 64 KiB - 8 by the rewriter, matching guardBytes.

// mbase returns the invariant base address of linear memory. The unix memory
// implementation reserves the full max size up front and only mprotects to
// commit, so the base never moves for the life of the process.
func (m *Module) mbase() unsafe.Pointer {
	if !unsafeMemFast {
		return nil
	}
	return unsafe.Pointer(unsafe.SliceData(*m.memory))
}

//go:nosplit
func (m *Module) load8u(base unsafe.Pointer, off int64) byte {
	if unsafeMemFast {
		return *(*byte)(unsafe.Add(base, uintptr(off)))
	}
	return (*m.memory)[off]
}

//go:nosplit
func (m *Module) store8u(base unsafe.Pointer, off int64, v byte) {
	if unsafeMemFast {
		*(*byte)(unsafe.Add(base, uintptr(off))) = v
		return
	}
	(*m.memory)[off] = v
}

//go:nosplit
func (m *Module) load16u(base unsafe.Pointer, off int64) uint16 {
	if unsafeMemFast {
		v := *(*uint16)(unsafe.Add(base, uintptr(off)))
		if big {
			v = bits.ReverseBytes16(v)
		}
		return v
	}
	return load16((*m.memory)[off:])
}

//go:nosplit
func (m *Module) store16u(base unsafe.Pointer, off int64, v uint16) {
	if unsafeMemFast {
		if big {
			v = bits.ReverseBytes16(v)
		}
		*(*uint16)(unsafe.Add(base, uintptr(off))) = v
		return
	}
	store16((*m.memory)[off:], v)
}

//go:nosplit
func (m *Module) load32u(base unsafe.Pointer, off int64) uint32 {
	if unsafeMemFast {
		v := *(*uint32)(unsafe.Add(base, uintptr(off)))
		if big {
			v = bits.ReverseBytes32(v)
		}
		return v
	}
	return load32((*m.memory)[off:])
}

//go:nosplit
func (m *Module) store32u(base unsafe.Pointer, off int64, v uint32) {
	if unsafeMemFast {
		if big {
			v = bits.ReverseBytes32(v)
		}
		*(*uint32)(unsafe.Add(base, uintptr(off))) = v
		return
	}
	store32((*m.memory)[off:], v)
}

//go:nosplit
func (m *Module) load64u(base unsafe.Pointer, off int64) uint64 {
	if unsafeMemFast {
		v := *(*uint64)(unsafe.Add(base, uintptr(off)))
		if big {
			v = bits.ReverseBytes64(v)
		}
		return v
	}
	return load64((*m.memory)[off:])
}

//go:nosplit
func (m *Module) store64u(base unsafe.Pointer, off int64, v uint64) {
	if unsafeMemFast {
		if big {
			v = bits.ReverseBytes64(v)
		}
		*(*uint64)(unsafe.Add(base, uintptr(off))) = v
		return
	}
	store64((*m.memory)[off:], v)
}

// Atomic variants keep the wasm alignment trap but elide the bounds check.

//go:nosplit
func (m *Module) atomicLoad32u(base unsafe.Pointer, off int64) uint32 {
	if unsafeMemFast {
		if uint32(off)&3 != 0 {
			panic("unaligned atomic")
		}
		v := atomic.LoadUint32((*uint32)(unsafe.Add(base, uintptr(off))))
		if big {
			v = bits.ReverseBytes32(v)
		}
		return v
	}
	return atomic_load32(*m.memory, off)
}

//go:nosplit
func (m *Module) atomicStore32u(base unsafe.Pointer, off int64, v uint32) {
	if unsafeMemFast {
		if uint32(off)&3 != 0 {
			panic("unaligned atomic")
		}
		if big {
			v = bits.ReverseBytes32(v)
		}
		atomic.StoreUint32((*uint32)(unsafe.Add(base, uintptr(off))), v)
		return
	}
	atomic_store32(*m.memory, off, v)
}

//go:nosplit
func (m *Module) atomicLoad64u(base unsafe.Pointer, off int64) uint64 {
	if unsafeMemFast {
		if uint32(off)&7 != 0 {
			panic("unaligned atomic")
		}
		v := atomic.LoadUint64((*uint64)(unsafe.Add(base, uintptr(off))))
		if big {
			v = bits.ReverseBytes64(v)
		}
		return v
	}
	return atomic_load64(*m.memory, off)
}

//go:nosplit
func (m *Module) atomicStore64u(base unsafe.Pointer, off int64, v uint64) {
	if unsafeMemFast {
		if uint32(off)&7 != 0 {
			panic("unaligned atomic")
		}
		if big {
			v = bits.ReverseBytes64(v)
		}
		atomic.StoreUint64((*uint64)(unsafe.Add(base, uintptr(off))), v)
		return
	}
	atomic_store64(*m.memory, off, v)
}
