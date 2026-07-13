//go:build unix && !tinygo.wasm && !re2_cgo && !re2_wasm2go

package internal

import (
	"fmt"
	"math"
	"unsafe"

	"github.com/tetratelabs/wazero/experimental"
	"github.com/wasilibs/wazero-helpers/allocator"
	"golang.org/x/sys/unix"
)

// guardBytes is the extra PROT_NONE address space reserved beyond the wasm
// memory maximum. With it, any 32-bit wasm address plus a constant offset up
// to one wasm page lands inside the reservation, which lets the runtime elide
// per-access bounds checks: a stray access faults on the guard instead of
// touching host memory.
const (
	boundsElisionAddressSpace = uint64(1) << 32
	guardBytes                = uint64(65536)
)

// guardedAllocator reserves the complete 32-bit WebAssembly address space and
// an inaccessible trailing guard. The optional reservation method is the
// explicit trust boundary used by patched wazero versions; older wazero
// versions simply ignore the extra method.
type guardedAllocator struct {
	// Keep pointer identities unique: distinct zero-sized allocations may have
	// the same address in Go.
	_ byte
}

// newGuardedNonMovingAllocator is like wazero-helpers' NewNonMoving but
// over-reserves guardBytes of permanently-PROT_NONE address space past the
// maximum so guard-page bounds elision is sound.
func newGuardedNonMovingAllocator() experimental.MemoryAllocator {
	if unsafe.Sizeof(uintptr(0)) < 8 {
		return allocator.NewNonMoving()
	}
	return &guardedAllocator{}
}

var unixPageSize = uint64(unix.Getpagesize())

func (*guardedAllocator) Allocate(cap, max uint64) experimental.LinearMemory {
	return guardedAlloc(cap, max)
}

func (*guardedAllocator) UnsafeBoundsCheckElisionReservation() (addressSpace, guardSize uint64) {
	return boundsElisionAddressSpace, guardBytes
}

func guardedAlloc(_, max uint64) experimental.LinearMemory {
	rnd := unixPageSize - 1
	res := (max + rnd) &^ rnd
	total := boundsElisionAddressSpace + guardBytes

	if res < max || max > boundsElisionAddressSpace || total > math.MaxInt {
		total = math.MaxUint64
	}

	b, err := unix.Mmap(-1, 0, int(total), unix.PROT_NONE, unix.MAP_PRIVATE|unix.MAP_ANON)
	if err != nil {
		panic(fmt.Errorf("re2_wazero: failed to reserve guarded memory: %w", err))
	}
	// The usable slice is capped at the declared wasm maximum. Everything from
	// that maximum through the end of the address space and the trailing guard
	// stays PROT_NONE for the mapping's lifetime. mapping retains the full
	// reservation for Free.
	return &guardedMmappedMemory{buf: b[:0:res], mapping: b}
}

type guardedMmappedMemory struct {
	buf     []byte // committed..declared maximum
	mapping []byte // complete 32-bit address space plus guard
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
