//go:build re2_wazero

package internal

import (
	"context"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	wazero "github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/wasilibs/wazero-helpers/allocator"
)

var errFailedRead = errors.New("failed to read from wasm memory")

//go:embed wasm/libcre2.wasm
var libre2 []byte

// memoryWasm created by `wat2wasm --enable-threads internal/wasm/memory.wat -o internal/wasm/memory.wasm`
//
//go:embed wasm/memory.wasm
var memoryWasm []byte

var (
	wasmRT       wazero.Runtime
	wasmCompiled wazero.CompiledModule
	wasmMemory   api.Memory
	rootMod      api.Module

	wasmInitOnce sync.Once
	modCreateMu  sync.Mutex

	// modShards is a per-P sharded free-list of reusable child modules. A single
	// global LIFO mutex serialized every acquire/release; with per-op wasm
	// malloc/free now eliminated (persistent scratch buffers), that pool mutex is
	// the dominant remaining contention point (CPU profile: ~35% at 32 cores).
	// Sharding by the running P (procPin) sends distinct cores to distinct
	// shards, so each shard's mutex is effectively always uncontended. Modules
	// are interchangeable (shared linear memory; differ only in stack/TLS +
	// their own scratch buffer), so a module acquired from one shard may be
	// returned to another after goroutine migration — harmless. Like the old
	// LIFO, shards never drop modules, so the childModule finalizer (which frees
	// TLS + scratch and closes the module) never runs while a module is live —
	// the invariant a plain sync.Pool violates by dropping idle entries on GC.
	modShards   []modShard
	numModShard int
)

// modShard is one per-P free-list, padded so adjacent shards never share a
// cache line (false sharing would reintroduce the very cross-core traffic the
// sharding removes).
type modShard struct {
	mu   sync.Mutex
	free []*childModule
	_    [128 - (unsafe.Sizeof(sync.Mutex{})+unsafe.Sizeof([]*childModule(nil)))%128]byte
}

//go:linkname runtime_procPin runtime.procPin
func runtime_procPin() int

//go:linkname runtime_procUnpin runtime.procUnpin
func runtime_procUnpin()

// shardIndex returns the current P's shard index. The pin window is only long
// enough to read the P id.
func shardIndex() int {
	pid := runtime_procPin()
	runtime_procUnpin()
	if numModShard == 0 {
		return 0
	}
	return pid % numModShard
}

type libre2ABI struct {
	cre2New                   lazyFunction
	cre2Delete                lazyFunction
	cre2Match                 lazyFunction
	cre2NumCapturingGroups    lazyFunction
	cre2ErrorCode             lazyFunction
	cre2ErrorArg              lazyFunction
	cre2NamedGroupsIterNew    lazyFunction
	cre2NamedGroupsIterNext   lazyFunction
	cre2NamedGroupsIterDelete lazyFunction
	cre2OptNew                lazyFunction
	cre2OptDelete             lazyFunction
	cre2OptSetLongestMatch    lazyFunction
	cre2OptSetPosixSyntax     lazyFunction
	cre2OptSetCaseSensitive   lazyFunction
	cre2OptSetLatin1Encoding  lazyFunction
	cre2OptSetMaxMem          lazyFunction

	cre2SetNew     lazyFunction
	cre2SetAdd     lazyFunction
	cre2SetCompile lazyFunction
	cre2SetMatch   lazyFunction
	cre2SetDelete  lazyFunction

	malloc lazyFunction
	free   lazyFunction
}

type wasmPtr uint32

var nilWasmPtr = wasmPtr(0)

var prevTID uint32

type childModule struct {
	mod        api.Module
	tlsBasePtr uint32
	functions  map[string]api.Function

	// scratchPtr/scratchLen is a persistent per-module scratch buffer in the
	// shared wasm linear memory. Every operation used to malloc a fresh buffer
	// (for the input string + match-result array) and free it at the end — two
	// wasm calls into the ONE shared C heap allocator per operation. Under 32
	// cores that shared allocator was the real serialization point (CPU profile:
	// malloc+free = ~37% cumulative, and the global pool mutex was accidentally
	// rate-limiting access to it). Instead each module keeps one scratch buffer,
	// grown geometrically on demand and never shrunk, so steady-state operations
	// do ZERO wasm malloc/free. A module is exclusively owned by one goroutine
	// between pool Get and Put, so its scratch buffer is single-owner while in
	// use — no aliasing across goroutines.
	scratchPtr uint32
	scratchLen uint32
}

// ensureScratch guarantees the module's scratch buffer is at least size bytes,
// growing geometrically (so total regrows over a module's life are O(log size))
// and never shrinking. The grow path is the only place this design touches the
// shared wasm heap, and it runs at most O(log maxInputSize) times per module.
func (cm *childModule) ensureScratch(abi *libre2ABI, size uint32) {
	if size <= cm.scratchLen {
		return
	}
	newLen := size
	if grown := cm.scratchLen * 2; grown > newLen {
		newLen = grown
	}
	if cm.scratchLen > 0 {
		freeOn(abi, cm, wasmPtr(cm.scratchPtr))
	}
	cm.scratchPtr = uint32(mallocOn(abi, cm, newLen))
	cm.scratchLen = newLen
}

func createChildModule(ctx context.Context, rt wazero.Runtime, root api.Module) *childModule {
	// Not executing function so is at end of stack
	stackPointer := root.ExportedGlobal("__stack_pointer").Get()
	tlsBase := root.ExportedGlobal("__tls_base").Get()

	// Thread-local-storage for the main thread is from __tls_base to __stack_pointer
	// For now, let's preserve the size but in the future we can probably use less.
	size := stackPointer - tlsBase

	malloc := root.ExportedFunction("malloc")

	// Allocate memory for the child thread stack
	res, err := malloc.Call(ctx, size)
	if err != nil {
		panic(err)
	}
	ptr := uint32(res[0])

	child, err := rt.InstantiateModule(ctx, wasmCompiled, wazero.NewModuleConfig().WithSysNanotime().WithSysWalltime().WithSysNanosleep().WithStdout(os.Stdout).WithStderr(os.Stderr).
		// Don't need to execute start functions again in child, it crashes anyways.
		WithStartFunctions().
		WithName(""))
	if err != nil {
		panic(err)
	}
	initTLS := child.ExportedFunction("__wasm_init_tls")
	if _, err := initTLS.Call(ctx, uint64(ptr)); err != nil {
		panic(err)
	}

	tid := atomic.AddUint32(&prevTID, 1)
	root.Memory().WriteUint32Le(ptr, ptr)
	root.Memory().WriteUint32Le(ptr+20, tid)
	if mg, ok := child.ExportedGlobal("__stack_pointer").(api.MutableGlobal); ok {
		mg.Set(uint64(ptr) + size)
	}

	ret := &childModule{
		mod:        child,
		tlsBasePtr: ptr,
		functions:  map[string]api.Function{},
	}
	runtime.SetFinalizer(ret, func(obj interface{}) {
		if cm, ok := obj.(*childModule); ok {
			free := cm.mod.ExportedFunction("free")
			if cm.scratchLen > 0 {
				if _, err := free.Call(ctx, uint64(cm.scratchPtr)); err != nil {
					panic(err)
				}
			}
			if _, err := free.Call(ctx, uint64(cm.tlsBasePtr)); err != nil {
				panic(err)
			}
			_ = cm.mod.Close(context.Background()) //nolint:contextcheck // don't want to capture in a finalizer
		}
	})
	return ret
}

func getChildModule(ctx context.Context) *childModule {
	wasmInitOnce.Do(func() {
		initWASM(ctx)
	})
	if cm := popChildModule(); cm != nil {
		return cm
	}

	modCreateMu.Lock()
	defer modCreateMu.Unlock()
	if cm := popChildModule(); cm != nil {
		return cm
	}
	return createChildModule(ctx, wasmRT, rootMod)
}

func putChildModule(cm *childModule) {
	s := &modShards[shardIndex()]
	s.mu.Lock()
	s.free = append(s.free, cm)
	s.mu.Unlock()
}

// popChildModule pops from the current P's shard first (the common, uncontended
// case), then steals from other shards so a module freed on a different P is
// still reused rather than forcing a fresh, expensive instantiation.
func popChildModule() *childModule {
	start := shardIndex()

	if cm := modShards[start].tryPop(); cm != nil {
		return cm
	}
	for off := 1; off < numModShard; off++ {
		if cm := modShards[(start+off)%numModShard].tryPop(); cm != nil {
			return cm
		}
	}
	return nil
}

func (s *modShard) tryPop() *childModule {
	s.mu.Lock()
	n := len(s.free)
	if n == 0 {
		s.mu.Unlock()
		return nil
	}
	cm := s.free[n-1]
	s.free[n-1] = nil
	s.free = s.free[:n-1]
	s.mu.Unlock()
	return cm
}

func initWASM(ctx context.Context) {
	// One shard per P so the common case is a P hitting its own private shard.
	numModShard = runtime.GOMAXPROCS(0)
	if numModShard < 1 {
		numModShard = 1
	}
	modShards = make([]modShard, numModShard)

	ctx = experimental.WithMemoryAllocator(ctx, allocator.NewNonMoving())

	rtCfg := wazero.NewRuntimeConfig().WithCoreFeatures(api.CoreFeaturesV2 | experimental.CoreFeaturesThreads)
	uc, err := os.UserCacheDir()
	if err == nil {
		cache, err := wazero.NewCompilationCacheWithDir(filepath.Join(uc, "com.github.wasilibs"))
		if err == nil {
			rtCfg = rtCfg.WithCompilationCache(cache)
		}
	}

	maxPages := defaultMaxPages
	if unsafe.Sizeof(uintptr(0)) < 8 {
		// On a 32-bit system. anything close to 4GB will fail (part of 4GB is already used by the rest of the process).
		// We go ahead and cap to 1GB to be extra conservative.
		maxPagesLimit := uint32(65536 / 4)
		if maxPages > maxPagesLimit {
			maxPages = maxPagesLimit
		}
	}
	rtCfg = rtCfg.WithMemoryLimitPages(maxPages)

	rt := wazero.NewRuntimeWithConfig(ctx, rtCfg)

	wasi_snapshot_preview1.MustInstantiate(ctx, rt)

	if _, err := rt.InstantiateWithConfig(ctx, memoryWasm, wazero.NewModuleConfig().WithName("env")); err != nil {
		panic(err)
	}

	code, err := rt.CompileModule(ctx, libre2)
	if err != nil {
		panic(err)
	}
	wasmCompiled = code

	// In some situations (eg, running as a service on windows)
	// Stdout and Stderr may not be available.
	// In this case, use io.Discard to avoid InstantiateModule returning an error.
	var stdout, stderr io.Writer = os.Stdout, os.Stderr

	if _, err := os.Stdout.Stat(); err != nil {
		stdout = io.Discard
	}
	if _, err := os.Stderr.Stat(); err != nil {
		stderr = io.Discard
	}

	wasmRT = rt
	root, err := wasmRT.InstantiateModule(ctx, wasmCompiled, wazero.NewModuleConfig().WithSysWalltime().WithSysNanotime().WithSysNanosleep().WithStdout(stdout).WithStderr(stderr).WithStartFunctions("_initialize").WithName(""))
	if err != nil {
		panic(err)
	}
	wasmMemory = root.Memory()
	rootMod = root
}

func newABI() *libre2ABI {
	abi := &libre2ABI{
		cre2New:                   newLazyFunction("cre2_new"),
		cre2Delete:                newLazyFunction("cre2_delete"),
		cre2Match:                 newLazyFunction("cre2_match"),
		cre2NumCapturingGroups:    newLazyFunction("cre2_num_capturing_groups"),
		cre2ErrorCode:             newLazyFunction("cre2_error_code"),
		cre2ErrorArg:              newLazyFunction("cre2_error_arg"),
		cre2NamedGroupsIterNew:    newLazyFunction("cre2_named_groups_iter_new"),
		cre2NamedGroupsIterNext:   newLazyFunction("cre2_named_groups_iter_next"),
		cre2NamedGroupsIterDelete: newLazyFunction("cre2_named_groups_iter_delete"),
		cre2OptNew:                newLazyFunction("cre2_opt_new"),
		cre2OptDelete:             newLazyFunction("cre2_opt_delete"),
		cre2OptSetLongestMatch:    newLazyFunction("cre2_opt_set_longest_match"),
		cre2OptSetPosixSyntax:     newLazyFunction("cre2_opt_set_posix_syntax"),
		cre2OptSetCaseSensitive:   newLazyFunction("cre2_opt_set_case_sensitive"),
		cre2OptSetLatin1Encoding:  newLazyFunction("cre2_opt_set_latin1_encoding"),
		cre2OptSetMaxMem:          newLazyFunction("cre2_opt_set_max_mem"),
		cre2SetNew:                newLazyFunction("cre2_set_new"),
		cre2SetAdd:                newLazyFunction("cre2_set_add"),
		cre2SetCompile:            newLazyFunction("cre2_set_compile"),
		cre2SetMatch:              newLazyFunction("cre2_set_match"),
		cre2SetDelete:             newLazyFunction("cre2_set_delete"),
		malloc:                    newLazyFunction("malloc"),
		free:                      newLazyFunction("free"),
	}

	return abi
}

func (abi *libre2ABI) startOperation(memorySize int) allocation {
	return abi.reserve(uint32(memorySize))
}

func (abi *libre2ABI) endOperation(a allocation) {
	a.free()
}

func newRE(abi *libre2ABI, pattern cString, opts CompileOptions) wasmPtr {
	ctx := context.Background()
	optPtr := uint32(0)
	res, err := abi.cre2OptNew.Call0(ctx)
	if err != nil {
		panic(err)
	}
	optPtr = uint32(res)
	defer func() {
		if _, err := abi.cre2OptDelete.Call1(ctx, uint64(optPtr)); err != nil {
			panic(err)
		}
	}()

	_, err = abi.cre2OptSetMaxMem.Call2(ctx, uint64(optPtr), uint64(maxSize))
	if err != nil {
		panic(err)
	}

	if opts.Longest {
		_, err = abi.cre2OptSetLongestMatch.Call2(ctx, uint64(optPtr), 1)
		if err != nil {
			panic(err)
		}
	}
	if opts.Posix {
		_, err = abi.cre2OptSetPosixSyntax.Call2(ctx, uint64(optPtr), 1)
		if err != nil {
			panic(err)
		}
	}
	if opts.CaseInsensitive {
		_, err = abi.cre2OptSetCaseSensitive.Call2(ctx, uint64(optPtr), 0)
		if err != nil {
			panic(err)
		}
	}
	if opts.Latin1 {
		_, err = abi.cre2OptSetLatin1Encoding.Call1(ctx, uint64(optPtr))
		if err != nil {
			panic(err)
		}
	}

	res, err = abi.cre2New.Call3(ctx, uint64(pattern.ptr), uint64(pattern.length), uint64(optPtr))
	if err != nil {
		panic(err)
	}
	return wasmPtr(res)
}

func reError(abi *libre2ABI, rePtr wasmPtr) (int, string) {
	ctx := context.Background()
	res, err := abi.cre2ErrorCode.Call1(ctx, uint64(rePtr))
	if err != nil {
		panic(err)
	}
	code := int(res)
	if code == 0 {
		return 0, ""
	}

	res, err = abi.cre2ErrorArg.Call1(ctx, uint64(rePtr))
	if err != nil {
		panic(err)
	}
	msg := copyCString(wasmPtr(res))
	return code, msg
}

func numCapturingGroups(abi *libre2ABI, rePtr wasmPtr) int {
	ctx := context.Background()
	res, err := abi.cre2NumCapturingGroups.Call1(ctx, uint64(rePtr))
	if err != nil {
		panic(err)
	}
	return int(res)
}

func deleteRE(abi *libre2ABI, rePtr wasmPtr) {
	ctx := context.Background()
	if _, err := abi.cre2Delete.Call1(ctx, uint64(rePtr)); err != nil {
		panic(err)
	}
}

func release(re *Regexp) {
	deleteRE(re.abi, re.ptr)
}

func match(alloc *allocation, re *Regexp, s cString, matchesPtr wasmPtr, nMatches uint32) bool {
	ctx := context.Background()
	res, err := re.abi.cre2Match.Call8On(ctx, alloc.mod, uint64(re.ptr), uint64(s.ptr), uint64(s.length), 0, uint64(s.length), 0, uint64(matchesPtr), uint64(nMatches))
	if err != nil {
		panic(err)
	}

	return res == 1
}

func matchFrom(alloc *allocation, re *Regexp, s cString, startPos int, matchesPtr wasmPtr, nMatches uint32) bool {
	ctx := context.Background()
	res, err := re.abi.cre2Match.Call8On(ctx, alloc.mod, uint64(re.ptr), uint64(s.ptr), uint64(s.length), uint64(startPos), uint64(s.length), 0, uint64(matchesPtr), uint64(nMatches))
	if err != nil {
		panic(err)
	}

	return res == 1
}

func readMatch(alloc *allocation, cs cString, matchPtr wasmPtr, dstCap []int) []int {
	matchBuf := alloc.read(matchPtr, 8)
	subStrPtr := binary.LittleEndian.Uint32(matchBuf)
	sLen := binary.LittleEndian.Uint32(matchBuf[4:])
	sIdx := subStrPtr - uint32(cs.ptr)

	return append(dstCap, int(sIdx), int(sIdx+sLen))
}

func readMatches(alloc *allocation, cs cString, matchesPtr wasmPtr, n int, deliver func([]int) bool) {
	var dstCap [2]int

	matchesBuf := alloc.read(matchesPtr, 8*n)
	for i := range n {
		subStrPtr := binary.LittleEndian.Uint32(matchesBuf[8*i:])
		if subStrPtr == 0 {
			if !deliver(append(dstCap[:0], -1, -1)) {
				break
			}
			continue
		}
		sLen := binary.LittleEndian.Uint32(matchesBuf[8*i+4:])
		sIdx := subStrPtr - uint32(cs.ptr)
		if !deliver(append(dstCap[:0], int(sIdx), int(sIdx+sLen))) {
			break
		}
	}
}

func namedGroupsIter(abi *libre2ABI, rePtr wasmPtr) wasmPtr {
	ctx := context.Background()

	res, err := abi.cre2NamedGroupsIterNew.Call1(ctx, uint64(rePtr))
	if err != nil {
		panic(err)
	}

	return wasmPtr(res)
}

func namedGroupsIterNext(abi *libre2ABI, iterPtr wasmPtr) (string, int, bool) {
	ctx := context.Background()

	// Not on the hot path so don't bother optimizing this yet.
	ptrs := malloc(abi, 8)
	defer free(abi, ptrs)
	namePtrPtr := ptrs
	indexPtr := namePtrPtr + 4

	res, err := abi.cre2NamedGroupsIterNext.Call3(ctx, uint64(iterPtr), uint64(namePtrPtr), uint64(indexPtr))
	if err != nil {
		panic(err)
	}

	if res == 0 {
		return "", 0, false
	}

	namePtr, ok := wasmMemory.ReadUint32Le(uint32(namePtrPtr))
	if !ok {
		panic(errFailedRead)
	}

	name := copyCString(wasmPtr(namePtr))

	index, ok := wasmMemory.ReadUint32Le(uint32(indexPtr))
	if !ok {
		panic(errFailedRead)
	}

	return name, int(index), true
}

func namedGroupsIterDelete(abi *libre2ABI, iterPtr wasmPtr) {
	ctx := context.Background()

	_, err := abi.cre2NamedGroupsIterDelete.Call1(ctx, uint64(iterPtr))
	if err != nil {
		panic(err)
	}
}

func newSet(abi *libre2ABI, opts CompileOptions) wasmPtr {
	ctx := context.Background()
	optPtr := uint32(0)
	res, err := abi.cre2OptNew.Call0(ctx)
	if err != nil {
		panic(err)
	}
	optPtr = uint32(res)
	defer func() {
		if _, err := abi.cre2OptDelete.Call1(ctx, uint64(optPtr)); err != nil {
			panic(err)
		}
	}()

	_, err = abi.cre2OptSetMaxMem.Call2(ctx, uint64(optPtr), uint64(maxSize))
	if err != nil {
		panic(err)
	}

	if opts.Longest {
		_, err = abi.cre2OptSetLongestMatch.Call2(ctx, uint64(optPtr), 1)
		if err != nil {
			panic(err)
		}
	}
	if opts.Posix {
		_, err = abi.cre2OptSetPosixSyntax.Call2(ctx, uint64(optPtr), 1)
		if err != nil {
			panic(err)
		}
	}
	if opts.CaseInsensitive {
		_, err = abi.cre2OptSetCaseSensitive.Call2(ctx, uint64(optPtr), 0)
		if err != nil {
			panic(err)
		}
	}
	if opts.Latin1 {
		_, err = abi.cre2OptSetLatin1Encoding.Call1(ctx, uint64(optPtr))
		if err != nil {
			panic(err)
		}
	}

	res, err = abi.cre2SetNew.Call2(ctx, uint64(optPtr), 0)
	if err != nil {
		panic(err)
	}
	return wasmPtr(res)
}

func setAdd(set *Set, s cString) string {
	ctx := context.Background()
	res, err := set.abi.cre2SetAdd.Call3(ctx, uint64(set.ptr), uint64(s.ptr), uint64(s.length))
	if err != nil {
		panic(err)
	}
	if res == 0 {
		return unknownCompileError
	}
	msgPtr := wasmPtr(res)
	msg := copyCString(msgPtr)
	if msg != "ok" {
		free(set.abi, msgPtr)
		return "error parsing regexp: " + msg
	}
	return ""
}

func setCompile(set *Set) int32 {
	ctx := context.Background()
	res, err := set.abi.cre2SetCompile.Call1(ctx, uint64(set.ptr))
	if err != nil {
		panic(err)
	}
	return int32(res)
}

func setMatch(set *Set, cs cString, matchedPtr wasmPtr, nMatch int) int {
	ctx := context.Background()
	res, err := set.abi.cre2SetMatch.Call5(ctx, uint64(set.ptr), uint64(cs.ptr), uint64(cs.length), uint64(matchedPtr), uint64(nMatch))
	if err != nil {
		panic(err)
	}
	return int(res)
}

func deleteSet(abi *libre2ABI, setPtr wasmPtr) {
	ctx := context.Background()
	_, err := abi.cre2SetDelete.Call1(ctx, uint64(setPtr))
	if err != nil {
		panic(err)
	}
}

type cString struct {
	ptr    wasmPtr
	length int
}

type cStringArray struct {
	ptr wasmPtr
}

func (a cStringArray) free() {
	// We pool allocation and don't need to explicitly free.
}

func malloc(abi *libre2ABI, size uint32) wasmPtr {
	if res, err := abi.malloc.Call1(context.Background(), uint64(size)); err != nil {
		panic(err)
	} else {
		return wasmPtr(res)
	}
}

func free(abi *libre2ABI, ptr wasmPtr) {
	if _, err := abi.free.Call1(context.Background(), uint64(ptr)); err != nil {
		panic(err)
	}
}

// mallocOn / freeOn run on a specific pinned module (no pool op). Used only by
// the module's own scratch-buffer grow path.
func mallocOn(abi *libre2ABI, mod *childModule, size uint32) wasmPtr {
	if res, err := abi.malloc.Call1On(context.Background(), mod, uint64(size)); err != nil {
		panic(err)
	} else {
		return wasmPtr(res)
	}
}

func freeOn(abi *libre2ABI, mod *childModule, ptr wasmPtr) {
	if _, err := abi.free.Call1On(context.Background(), mod, uint64(ptr)); err != nil {
		panic(err)
	}
}

func copyCString(ptr wasmPtr) string {
	res := strings.Builder{}
	for {
		b, ok := wasmMemory.ReadByte(uint32(ptr))
		if !ok {
			panic(errFailedRead)
		}
		if b == 0 {
			break
		}
		res.WriteByte(b)
		ptr++
	}
	return res.String()
}

type allocation struct {
	size    uint32
	bufPtr  wasmPtr
	nextIdx uint32
	abi     *libre2ABI
	// mod is the child module pinned for this operation's lifetime. All wasm
	// calls in the operation (match, and any cold-path opt calls) run on it via
	// the *On helpers, and the operation's bump-allocation buffer is this
	// module's persistent scratch — so a steady-state operation performs no wasm
	// malloc/free at all and touches the module pool exactly twice.
	mod *childModule
}

func (abi *libre2ABI) reserve(size uint32) allocation {
	mod := getChildModule(context.Background())
	mod.ensureScratch(abi, size)
	return allocation{
		size:    size,
		bufPtr:  wasmPtr(mod.scratchPtr),
		nextIdx: 0,
		abi:     abi,
		mod:     mod,
	}
}

func (a *allocation) free() {
	// The scratch buffer stays allocated on the module for reuse; we only return
	// the module to the pool. No wasm free on the hot path.
	putChildModule(a.mod)
}

func (a *allocation) allocate(size uint32) wasmPtr {
	if a.nextIdx+size > a.size {
		panic("not enough reserved shared memory")
	}

	ptr := uint32(a.bufPtr) + a.nextIdx
	a.nextIdx += size
	return wasmPtr(ptr)
}

func (a *allocation) read(ptr wasmPtr, size int) []byte {
	buf, ok := wasmMemory.Read(uint32(ptr), uint32(size))
	if !ok {
		panic(errFailedRead)
	}
	return buf
}

func (a *allocation) write(b []byte) wasmPtr {
	ptr := a.allocate(uint32(len(b)))
	wasmMemory.Write(uint32(ptr), b)
	return ptr
}

func (a *allocation) writeString(s string) wasmPtr {
	ptr := a.allocate(uint32(len(s)))
	wasmMemory.WriteString(uint32(ptr), s)
	return ptr
}

func (a *allocation) newCString(s string) cString {
	ptr := a.writeString(s)
	return cString{
		ptr:    ptr,
		length: len(s),
	}
}

func (a *allocation) newCStringFromBytes(s []byte) cString {
	ptr := a.write(s)
	return cString{
		ptr:    ptr,
		length: len(s),
	}
}

func (a *allocation) newCStringArray(n int) cStringArray {
	ptr := a.allocate(uint32(n * 8))
	return cStringArray{ptr: ptr}
}

type lazyFunction struct {
	name string
}

func newLazyFunction(name string) lazyFunction {
	return lazyFunction{name: name}
}

func (f *lazyFunction) Call0(ctx context.Context) (uint64, error) {
	var callStack [1]uint64
	return f.callWithStack(ctx, callStack[:])
}

func (f *lazyFunction) Call1(ctx context.Context, arg1 uint64) (uint64, error) {
	var callStack [1]uint64
	callStack[0] = arg1
	return f.callWithStack(ctx, callStack[:])
}

func (f *lazyFunction) Call2(ctx context.Context, arg1 uint64, arg2 uint64) (uint64, error) {
	var callStack [2]uint64
	callStack[0] = arg1
	callStack[1] = arg2
	return f.callWithStack(ctx, callStack[:])
}

func (f *lazyFunction) Call3(ctx context.Context, arg1 uint64, arg2 uint64, arg3 uint64) (uint64, error) {
	var callStack [3]uint64
	callStack[0] = arg1
	callStack[1] = arg2
	callStack[2] = arg3
	return f.callWithStack(ctx, callStack[:])
}

func (f *lazyFunction) Call5(ctx context.Context, arg1 uint64, arg2 uint64, arg3 uint64, arg4 uint64, arg5 uint64) (uint64, error) {
	var callStack [5]uint64
	callStack[0] = arg1
	callStack[1] = arg2
	callStack[2] = arg3
	callStack[3] = arg4
	callStack[4] = arg5
	return f.callWithStack(ctx, callStack[:])
}

func (f *lazyFunction) Call8(ctx context.Context, arg1 uint64, arg2 uint64, arg3 uint64, arg4 uint64, arg5 uint64, arg6 uint64, arg7 uint64, arg8 uint64) (uint64, error) {
	var callStack [8]uint64
	callStack[0] = arg1
	callStack[1] = arg2
	callStack[2] = arg3
	callStack[3] = arg4
	callStack[4] = arg5
	callStack[5] = arg6
	callStack[6] = arg7
	callStack[7] = arg8
	return f.callWithStack(ctx, callStack[:])
}

func (f *lazyFunction) callWithStack(ctx context.Context, callStack []uint64) (uint64, error) {
	modH := getChildModule(ctx)
	defer putChildModule(modH)
	return f.callWithStackOn(ctx, modH, callStack)
}

// callWithStackOn runs the function on an already-acquired child module,
// skipping the pool Get/Put. Used on the hot path where the operation has
// pinned one module for its whole lifetime (see allocation.mod).
func (f *lazyFunction) callWithStackOn(ctx context.Context, modH *childModule, callStack []uint64) (uint64, error) {
	fun := modH.functions[f.name]
	if fun == nil {
		fun = modH.mod.ExportedFunction(f.name)
		modH.functions[f.name] = fun
	}

	if err := fun.CallWithStack(ctx, callStack); err != nil {
		return 0, fmt.Errorf("re2_wazero: calling function: %w", err)
	}
	return callStack[0], nil
}

// Call1On / Call8On are the pinned-module counterparts of Call1 / Call8 for the
// hot path, avoiding a pool Get/Put per wasm call.
func (f *lazyFunction) Call1On(ctx context.Context, modH *childModule, arg1 uint64) (uint64, error) {
	var callStack [1]uint64
	callStack[0] = arg1
	return f.callWithStackOn(ctx, modH, callStack[:])
}

func (f *lazyFunction) Call8On(ctx context.Context, modH *childModule, arg1, arg2, arg3, arg4, arg5, arg6, arg7, arg8 uint64) (uint64, error) {
	var callStack [8]uint64
	callStack[0] = arg1
	callStack[1] = arg2
	callStack[2] = arg3
	callStack[3] = arg4
	callStack[4] = arg5
	callStack[5] = arg6
	callStack[6] = arg7
	callStack[7] = arg8
	return f.callWithStackOn(ctx, modH, callStack[:])
}
