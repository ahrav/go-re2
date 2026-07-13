# Plan: Close go-re2's pure-Go performance gap

**Date**: 2026-07-13
**Status**: Confirmed
**Task**: Measure, explain, and reduce the wasm2go and wazero gap to the cgo backend without changing the public API or matching semantics.

## Chosen Design

Use an evidence-first experimental pipeline. Establish one reproducible multi-backend harness, profile the representative workload classes, and test one isolated mechanism at a time. Start every candidate at rung 1 (the smallest existing-code diff); climb to a new helper, generated artifact, or wazero compiler change only when a measured bottleneck cannot be tested at the lower rung.

**Rejected rungs**:

- Rung 0 (configuration only): rejected because the existing branch already uses pooling, scratch reuse, direct dispatch, non-moving memory, and guarded bounds elision, yet their current relative value has not been established against all four backends.
- Rung 4 (new abstraction): not selected up front because no profile yet proves that a new public or internal abstraction is required.
- Rung 5 (new dependency): not selected because the repository and Go toolchain already provide benchmark, pprof, trace, fuzz, and compiler diagnostics.
- Rung 6 (new subsystem): not selected because the optimization target is the existing in-process engine boundary.

**Kill-shot for the chosen design**: a candidate wins a narrow microbenchmark but fails repeated representative workloads, exact differential output, RSS/allocation limits, concurrency, race, or the affected wazero test suite.

## Assumptions

- The current go-re2 and wazero branch tips are hypotheses, not the baseline truth; every inherited optimization remains subject to measurement — verified from git history and live code.
- Go 1.26.5 is the available local toolchain and the modules declare Go 1.25 with a Go 1.26.4 workspace — verified from module files and the local mise installation.
- The target host is a 64-core aarch64 Linux VM with one NUMA node, 64 KiB L1d per core, 1 MiB L2 per core, and 32 MiB shared L3 — verified from `lscpu` and `uname`.
- The tracked Wasm artifact is the final shipped input to both pure-Go backends; generated wasm2go code is derived from it with wasm2go v0.4.11 `-unsafe` after Binaryen `-O3` — verified from `buildtools/wasm/Dockerfile`.
- Public API changes and semantic deviations are forbidden — user-confirmed in the objective.

## Open Unknowns

- Native cgo prerequisites are not installed on the host — resolve before the four-backend baseline by locating or building the exact RE2 version from the Wasm build manifest.
- Hardware PMU access and `perf` availability are unknown — resolve when profiling begins; fall back to Go pprof, compiler diagnostics, objdump, and controlled mechanism sweeps if unavailable.
- The correct primary aggregate metric is not yet fixed because workload ratios may differ materially — resolve after the baseline; retain per-benchmark results and use an aggregate only for autoresearch keep/discard decisions.
- RSS measurement scope for short benchmarks is not yet calibrated — resolve while building the harness by comparing process peak RSS and sampled steady-state RSS.

## Implementation Steps

1. Finish reading the backend, build, generated-memory, benchmark, correctness, and wazero compiler paths — verify by a file/symbol inventory tied to each requested overhead category.
2. Build a pinned external `go.work` connection to the isolated wazero worktree and dry-run all backend build tags — verify with compile-only tests for wasm2go, wazero, stdlib, and cgo.
3. Define a fixed benchmark manifest covering small/large, simple/complex, hit/miss, submatch/FindAll, Set, and parallel workloads — verify that each backend emits the same benchmark names and correctness digest.
4. Establish repeated balanced process-level baselines for ns/op, B/op, allocs/op, RSS, and exact differential results — verify with parsed numeric output and stored raw logs.
5. Profile wasm2go and wazero separately, attributing boundary calls, copies, allocator activity, bounds/address translation, module/scratch pooling, generated-code quality, GC, and RE2 work — verify each conclusion with a matching profile, compiler diagnostic, or mechanism sweep.
6. Run isolated candidates. Commit each candidate before verification; retain wins and revert losses while logging hypothesis, mechanism, benchmark, correctness, and decision — verify against the fixed primary matrix and hard guard.
7. Run the full completion gates on the winning stack, including all requested go-re2 suites, race checks in every affected backend, and affected wazero tests — verify from clean commands and final git status.
8. Produce the final gap decomposition, four-backend table, correctness evidence, rejected candidates, remaining experiments, and risks — verify every claim against preserved raw results.

## Escalation

Use the performance pipeline after the first profile to dispatch only evidence-backed specialists. Use ASM/SIMD/VM/THP techniques only when their respective measured gate fires.
