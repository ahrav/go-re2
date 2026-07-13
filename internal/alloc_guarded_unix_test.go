//go:build unix && !tinygo.wasm && !re2_cgo && !re2_wasm2go

package internal

import (
	"testing"
	"unsafe"

	"github.com/tetratelabs/wazero/experimental"
)

type boundsElisionAllocator interface {
	experimental.MemoryAllocator
	UnsafeBoundsCheckElisionReservation() (addressSpace, guardSize uint64)
}

func TestGuardedAllocatorBoundsElisionContract(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) < 8 {
		t.Skip("guarded address-space reservation requires a 64-bit host")
	}

	allocator := newGuardedNonMovingAllocator()
	capability, ok := allocator.(boundsElisionAllocator)
	if !ok {
		t.Fatal("64-bit Unix allocator does not expose bounds-elision capability")
	}
	addressSpace, guardSize := capability.UnsafeBoundsCheckElisionReservation()
	if addressSpace != boundsElisionAddressSpace || guardSize != guardBytes {
		t.Fatalf("reservation = (%d, %d), want (%d, %d)", addressSpace, guardSize, boundsElisionAddressSpace, guardBytes)
	}

	const declaredMaximum = uint64(2 * 65536)
	memory := allocator.Allocate(0, declaredMaximum)
	guarded, ok := memory.(*guardedMmappedMemory)
	if !ok {
		t.Fatalf("memory type = %T, want *guardedMmappedMemory", memory)
	}
	defer guarded.Free()
	if got, want := uint64(len(guarded.mapping)), addressSpace+guardSize; got != want {
		t.Fatalf("mapped reservation = %d, want %d", got, want)
	}
	if got := uint64(cap(guarded.buf)); got != declaredMaximum {
		t.Fatalf("usable capacity = %d, want declared maximum %d", got, declaredMaximum)
	}

	first := guarded.Reallocate(65536)
	base := &first[0]
	second := guarded.Reallocate(declaredMaximum)
	if &second[0] != base {
		t.Fatal("linear-memory base address moved while growing")
	}
}

func TestGuardedAllocatorIdentityIsUnique(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) < 8 {
		t.Skip("guarded address-space reservation requires a 64-bit host")
	}

	first := newGuardedNonMovingAllocator()
	second := newGuardedNonMovingAllocator()
	if first == second {
		t.Fatal("distinct guarded allocators share an identity")
	}
}
