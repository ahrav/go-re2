//go:build unix

package memory

import (
	"fmt"
	"math"
	"sync"

	"golang.org/x/sys/unix"
)

type Memory struct {
	Buf []byte
	Max int64
	com int
	// mapping is the full reservation including the guard tail; Buf's cap is
	// limited to the usable region so commits can never touch the guard.
	mapping []byte
	mu      sync.Mutex
}

// guardBytes is a PROT_NONE tail reserved past the maximum linear-memory
// size. Together with a full max-size reservation it makes any
// base+uint32-offset+small-constant access either land in the wasm memory or
// fault on a protected page, which is what lets the transpiled module elide
// explicit bounds checks on 64-bit unix (see internal/wasm/hotmem.go).
const guardBytes = 65536

func (m *Memory) Slice() *[]byte {
	return &m.Buf
}

func (m *Memory) Grow(delta, _ int64) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.Buf == nil {
		m.allocate(uint64(m.Max) << 16)
	}

	sz := len(m.Buf)
	old := int64(sz >> 16)
	if delta == 0 {
		return old
	}
	newPages := old + delta
	if newPages > m.Max {
		return -1
	}
	m.reallocate(uint64(newPages) << 16)
	return old
}

func (m *Memory) allocate(maxSz uint64) {
	// Round up to the page size.
	rnd := uint64(unix.Getpagesize() - 1)
	res := (maxSz + rnd) &^ rnd

	// Reserve a guard tail past the usable maximum (never committed).
	total := res + guardBytes
	if total > math.MaxInt || total < res {
		// This ensures int(total) overflows to a negative value,
		// and unix.Mmap returns EINVAL.
		total = math.MaxUint64
	}

	// Reserve total bytes of address space, to ensure we won't need to move it.
	// A protected, private, anonymous mapping should not commit memory.
	b, err := unix.Mmap(-1, 0, int(total), unix.PROT_NONE, unix.MAP_PRIVATE|unix.MAP_ANON)
	if err != nil {
		panic(err)
	}
	m.mapping = b
	// Usable capacity excludes the guard so reallocate can never commit it.
	m.Buf = b[:0:res]
}

func (m *Memory) reallocate(size uint64) {
	com := uint64(m.com)
	res := uint64(cap(m.Buf))
	if com < size && size <= res {
		// Grow geometrically, round up to the page size.
		rnd := uint64(unix.Getpagesize() - 1)
		newSz := com + com>>3
		newSz = min(max(size, newSz), res)
		newSz = (newSz + rnd) &^ rnd

		// Commit additional memory up to new bytes.
		err := unix.Mprotect(m.Buf[m.com:newSz], unix.PROT_READ|unix.PROT_WRITE)
		if err != nil {
			panic(err)
		}
		m.com = int(newSz)
	}
	buf := m.Buf[:size]
	atomicStoreSliceLen(&m.Buf, len(buf))
}

func (m *Memory) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	err := unix.Munmap(m.mapping)
	m.mapping = nil
	m.Buf = nil
	m.com = 0
	return fmt.Errorf("memory: unmap failed: %w", err)
}
