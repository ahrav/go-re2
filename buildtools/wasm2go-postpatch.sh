#!/usr/bin/env bash
# Re-applies hand-written hot-function overrides after regenerating
# internal/wasm/libcre2.wasm.go (see internal/wasm/hotpatch.go).
# Renames each overridden generated function to a *Generated suffix so the
# override in hotpatch.go takes its place at every call site.
set -euo pipefail
cd "$(dirname "$0")/.."
GEN=internal/wasm/libcre2.wasm.go

# fn30 = memchr
sed -i 's/^func (m \*Module) fn30(v0, v1, v2 int32) int32 {$/func (m *Module) fn30Generated(v0, v1, v2 int32) int32 { \/\/ overridden in hotpatch.go/' "$GEN"

grep -q 'fn30Generated' "$GEN" && echo "postpatch: OK"
