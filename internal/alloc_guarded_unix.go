//go:build unix && !tinygo.wasm && !re2_cgo && !re2_wasm2go

package internal

import (
	"fmt"
	"math"

	"github.com/tetratelabs/wazero/experimental"
	"golang.org/x/sys/unix"
)

// guardBytes is the extra PROT_NONE address space reserved beyond the wasm
// memory maximum. With it, any 32-bit wasm address plus a constant offset up
// to one wasm page lands inside the reservation, which lets the runtime elide
// per-access bounds checks: a stray access faults on the guard instead of
// touching host memory.
const guardBytes = 65536

// guardedAllocSupported reports whether the allocator actually over-reserves
// the guard region, making bounds-check elision sound.
const guardedAllocSupported = true

// newGuardedNonMovingAllocator is like wazero-helpers' NewNonMoving but
// over-reserves guardBytes of permanently-PROT_NONE address space past the
// maximum so guard-page bounds elision is sound.
func newGuardedNonMovingAllocator() experimental.MemoryAllocator {
	return experimental.MemoryAllocatorFunc(guardedAlloc)
}

var unixPageSize = uint64(unix.Getpagesize())

func guardedAlloc(_, max uint64) experimental.LinearMemory {
	rnd := unixPageSize - 1
	res := (max + rnd) &^ rnd
	total := res + guardBytes

	if total > math.MaxInt {
		total = math.MaxUint64
	}

	b, err := unix.Mmap(-1, 0, int(total), unix.PROT_NONE, unix.MAP_PRIVATE|unix.MAP_ANON)
	if err != nil {
		panic(fmt.Errorf("re2_wazero: failed to reserve guarded memory: %w", err))
	}
	// The usable slice is capped at the wasm maximum: Reallocate can never
	// commit into the trailing guard region, which stays PROT_NONE for the
	// mapping's lifetime. mapping retains the full reservation for Free.
	return &guardedMmappedMemory{buf: b[:0:res], mapping: b}
}

type guardedMmappedMemory struct {
	buf     []byte // committed..reserved (excludes guard)
	mapping []byte // the whole mmap including the guard region
}

func (m *guardedMmappedMemory) Reallocate(size uint64) []byte {
	com := uint64(len(m.buf))
	res := uint64(cap(m.buf))

	if com < size && size <= res {
		rnd := unixPageSize - 1
		newCap := (size + rnd) &^ rnd

		err := unix.Mprotect(m.buf[com:newCap], unix.PROT_READ|unix.PROT_WRITE)
		if err != nil {
			return nil
		}

		m.buf = m.buf[:newCap]
	}
	return m.buf[:size:len(m.buf)]
}

func (m *guardedMmappedMemory) Free() {
	err := unix.Munmap(m.mapping)
	if err != nil {
		panic(fmt.Errorf("re2_wazero: failed to release guarded memory: %w", err))
	}
	m.buf = nil
	m.mapping = nil
}
