# Pure-Go backend autoresearch results

Date: 2026-07-13

## Outcome

The retained changes improve the 20-series serial benchmark geomean by 10.23%
for wasm2go and 21.12% for wazero. The unchanged stdlib control moved +0.11%
and cgo moved -0.61%, which bounds machine drift for the serial comparison.

The largest wins are:

- wasm2go short serial matches: 29-34% faster
- wasm2go short parallel matches: 63-67% faster
- wazero compile: 34-37% faster
- wazero 1-16 KiB realistic scans: 22-25% faster
- wazero short serial matches: 23-26% faster
- wazero stable parallel scans: 20-26% faster
- small Set matches: 7-15% faster on wasm2go and 32-33% faster on wazero

The final implementation keeps four optimization groups:

1. Reuse wazero's operation-owned call stack instead of allocating a
   uint64 slice for every match.
2. Capture wazero bounds-elision mode on first engine use, isolate compilation
   caches by mode, and enable it only with go-re2's full 4 GiB guarded
   reservation on 64-bit hosts.
3. Dispatch wazero Set matches through the checked-out operation module with a
   pre-resolved function and reusable stack.
4. Give each wasm2go child module a persistent, geometrically grown scratch
   arena and retain that module for the entire operation.

## Method

All performance decisions use process-level A/B samples, not repeated
sub-benchmarks in one process:

- 12 samples per benchmark and implementation
- rotating backend order for baseline/final measurements
- alternating baseline/candidate order for individual experiments
- serial work pinned to CPU 0 with GOMAXPROCS=1
- parallel work pinned to CPUs 0-7 with GOMAXPROCS=8
- fixed benchmark corpus and exact cross-backend output digest
- medians reported below; lower time is better

The primary serial matrix has 20 series spanning compile, short calls,
1-16 KiB realistic scans, and 32 KiB-1 MiB inputs. Parallel results distinguish
stable realistic scans from the deliberately contention-heavy worker-count
microbenchmarks, whose coefficients of variation are often 8-15%.

The baseline and final cgo binaries use the same private exact RE2/Abseil build.
The Wasm artifact was rebuilt with the pinned repository toolchain when tested.
The host did not expose the perf command, so profile evidence came from Go
pprof plus direct Linux perf_event_open counters.

## Final performance

### Change from baseline

| Matrix | stdlib | cgo | wasm2go | wazero |
|---|---:|---:|---:|---:|
| Serial, 20-series geomean | +0.11% | -0.61% | **-10.23%** | **-21.12%** |
| Parallel, 7-series geomean | +0.54% | -5.29% | **-48.61%** | **-16.07%** |
| Set, 4-series geomean | n/a | -0.13% | **-5.65%** | **-16.13%** |

The parallel geomean includes noisy worker-count stress cases. The stable
realistic series are more useful: wasm2go LeaksScanParallel improves 0.44%
and LeaksMatchParallel improves 1.81%; wazero improves 26.10% and 20.46%
respectively.

### Final absolute medians and remaining cgo gap

| Workload | cgo | wasm2go | gap | wazero | gap |
|---|---:|---:|---:|---:|---:|
| Find | 407.75 ns | 418.40 ns | +2.6% | 416.90 ns | +2.2% |
| Compile hard | 107.67 us | 279.90 us | +160.0% | 173.31 us | +61.0% |
| Generic 16 KiB miss | 33.43 us | 85.23 us | +154.9% | 58.93 us | +76.3% |
| Realistic parallel match | 1.12 us | 2.76 us | +145.6% | 2.26 us | +101.4% |
| Realistic parallel scan | 4.25 us | 10.96 us | +157.7% | 7.56 us | +77.6% |
| Set complex/small | 464.85 ns | 553.70 ns | +19.1% | 547.20 ns | +17.7% |
| Set complex/large | 32.81 us | 83.01 us | +153.0% | 80.67 us | +145.8% |

BenchmarkMatch can let RE2 reject an input without scanning it, especially
through cgo. It is useful for call/copy attribution but is not treated as an
end-to-end throughput score.

### RSS

RSS is the median of four fresh-process peak measurements.

| Workload | Backend | Baseline | Final |
|---|---|---:|---:|
| 32 MiB match | stdlib | 38.68 MiB | 38.68 MiB |
| 32 MiB match | cgo | 43.94 MiB | 43.95 MiB |
| 32 MiB match | wasm2go | 71.73 MiB | 71.73 MiB |
| 32 MiB match | wazero | 119.65 MiB | 117.44 MiB |
| Parallel short match | stdlib | 4.68 MiB | 4.68 MiB |
| Parallel short match | cgo | 9.88 MiB | 9.39 MiB |
| Parallel short match | wasm2go | 7.23 MiB | 7.20 MiB |
| Parallel short match | wazero | 32.49 MiB | 20.39 MiB |

The wasm2go scratch arena does not raise large-input RSS. Wazero's final RSS
also decreases; no memory-growth tradeoff was required for the throughput wins.

## Gap decomposition

### Fixed per-call cost

Before this work, wazero allocated a 64-byte argument slice per match and a
48-byte slice per Set call. Reusing the operation-owned stack removes both:
the final short Match and small Set benchmarks report 0 B/op and 0 allocs/op.

wasm2go repeatedly acquired modules and malloc/free'd Wasm memory inside one
logical operation. Holding one child module and its geometric scratch arena
through allocation, guest execution, and result reads removes that churn. This
is why its strongest improvements appear on short and concurrent calls while
large scans remain almost unchanged.

### Bounds checks

go-re2 already reserves the entire 32-bit Wasm address space plus a guard tail
with PROT_NONE. The patched wazero compiler can therefore omit explicit
non-atomic bounds checks, but only when:

- the guarded allocator is supported,
- pointers are 64-bit,
- the opt-in mode was captured before engine compilation, and
- the compilation cache key records the same mode.

The previous package-init snapshot happened before an embedding library could
configure the environment. Capturing the mode at first engine use makes the
configuration effective without allowing the frontend and cache key to
disagree.

Atomic wait/notify paths retain explicit checks because they pass truncated
offsets to Go trampolines rather than performing a native access that would hit
the guard.

### Input copying and guest execution

Large wasm2go profiles spend 94-95% of sampled CPU time in runtime.memmove.
Large wazero profiles are dominated by the compiled guest matching loop. The
remaining large-input gap is therefore not Go wrapper bookkeeping: both pure-Go
backends must copy host input into Wasm linear memory before RE2 can inspect it,
while cgo can hand RE2 the original host pointer.

This explains the final shape:

- short calls approach cgo after eliminating allocation and module churn;
- compile still pays guest runtime/compiler overhead;
- large scans retain a 76-155% cgo gap because copying and guest execution
  dominate;
- SIMD, branchless Go wrappers, prefetching, and streaming stores do not target
  the measured bottleneck.

### Screened alternatives

Several requested design families did not justify an implementation after the
baseline/profile gate:

- A global compiled-pattern cache would change lifetime/resource semantics and
  does not help already-compiled Betterleaks matching. Callers can cache a
  Regexp without imposing a hidden process-wide policy.
- Fatter or batched internal calls do not remove the mandatory input copy. The
  retained operation ownership already removes the avoidable extra pool and
  dispatch crossings without changing the public API.
- Go PGO, inlining directives, branchless wrappers, and alternative Go data
  structures cannot materially affect profiles dominated by runtime.memmove or
  compiled guest code.
- Huge pages, madvise policy, and TLB-oriented changes would add platform and
  lifecycle complexity. The direct counters did not identify translation or
  host-memory stalls as the limiting Set mechanism, and large wasm2go time was
  in the copy itself.
- SWAR/SIMD copy loops and streaming stores were rejected at the design gate:
  Go's runtime copy is already architecture-tuned, and non-temporal writes are
  a poor fit because the guest immediately consumes the copied bytes.
- Cache blocking has no applicable host-side loop nest. Prefetching or assembly
  in Go would optimize outside the measured guest hotspot.

## Tournament record

The complete machine-readable record is
[2026-07-13-go-re2-autoresearch-results.tsv](./2026-07-13-go-re2-autoresearch-results.tsv).

Two negative results materially affected the final design:

- Hoisting the immutable shared-memory base in wazero changed frontend SSA but
  produced -0.00% serial, +0.01% Set, and -0.11% parallel changes. It was
  reverted because the machine backend already neutralized the opportunity.
- An early boolean-only CRE2 path initially improved wasm2go's selected serial
  matrix 1.16%. A later 12-block isolation against the pre-change artifact
  found that it made wazero generic scans 29-40% slower and realistic matching
  19% slower. The earlier wazero A/B had coincided with a host frequency
  bimodality, so this interaction was not visible until the final cross-backend
  gate. The change was reverted.

Wazero bounds elision has one retained tradeoff: large Set matching is
4.4-4.5% slower, despite small Set matching being 32-33% faster and the
4-series Set geomean improving 16.13%. Direct Set dispatch recovered the
avoidable allocation/call overhead; PMU measurements attribute the remaining
large-input cost to backend stalls in the unchecked guest loop, not branches,
I-cache misses, RSS, or Go dispatch. A shared-base SSA hoist did not change
machine performance, so no speculative special case was retained.

## Correctness and safety proof

Final validation:

- go test ./...
- go test -tags re2_wazero ./...
- go test -tags re2_cgo ./... against the private RE2 build
- full patched-wazero go test ./... with localhost access and umask 022
- exact digest across stdlib, cgo, wasm2go, and wazero:
  d0eb22c9d583275ded365c06269c8435bdd2db97775d96367553ff8a9cd5f4da
- targeted race runs for shared-regexp, distinct-regexp, scratch reuse,
  randomized property, and concurrent grow paths on both pure-Go backends
- seed differential and scratch-consistency corpora in normal tests
- four 5-second fuzz runs: both fuzz targets on both pure-Go backends
- linux/386 cross-compile of the wazero bounds gate
- final 12-sample timing matrix and four-sample RSS matrix

The fuzz campaigns completed without failures. The two slower wasm2go campaigns
executed fewer mutations because each input includes deliberate large/small
scratch-growth schedules; their complete seed corpora and the deterministic
randomized property test also run in the normal suite.

## Remaining opportunities

The next credible large-input improvement requires changing the host/Wasm data
boundary, not polishing wrapper control flow. Candidates would need to avoid or
amortize the mandatory host-to-linear-memory copy while preserving RE2's input
lifetime and guard invariants. That is an architectural project and should be
benchmarked as such.

For wazero large Set, the direct PMU result suggests a compiler-backend
scheduling or dependency-chain investigation. The frontend shared-base hoist
was insufficient, so any follow-up should begin from generated AArch64 and PMU
evidence rather than another source-level hoist.
