//go:build !unix && !tinygo.wasm && !re2_cgo && !re2_wasm2go

package internal

import (
	"github.com/tetratelabs/wazero/experimental"
	"github.com/wasilibs/wazero-helpers/allocator"
)

const guardedAllocSupported = false

// Non-unix platforms fall back to the standard non-moving allocator; the
// guard-page bounds-elision fast path must not be enabled there.
func newGuardedNonMovingAllocator() experimental.MemoryAllocator {
	return allocator.NewNonMoving()
}
