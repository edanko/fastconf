# Benchmarks

This document records how `fastconf` is measured and the results that guided
its optimization.

## Methodology

- Every benchmark uses `b.Loop()` (Go 1.24+) so setup is excluded from timing.
- Allocations are reported with `b.ReportAllocs()` and `-benchmem`.
- Results are collected with `-count=10` and a median is reported.
- Load-time work (file parsing, merging, placeholder resolution) is benchmarked
  separately from decode-time work.
- Benchmarks run serially; concurrent runs on the same machine are avoided.
- The destination tree is warmed before timing so lazy plan and tree building
  is not part of the measurement.

## Workloads

The decode benchmarks use a document with 1000 tenants, each with four values
(`tenantCode`, `domain` and a `publicUris` object with three strings), plus the
implicit `default` tenant. This matches the shape of a realistic multi-tenant
microservice configuration.

| Benchmark | What it measures |
| --- | --- |
| `BenchmarkDecodeStruct` | decode the root configuration, including `map[string]Tenant` with 1001 tenants |
| `BenchmarkDecodeTenantStruct` | decode one tenant into a 3-field struct |
| `BenchmarkDecodeTenantMap` | deep copy of one tenant map |
| `BenchmarkDecodeMap` | deep copy of the whole configuration root |
| `BenchmarkLoadDocument` | full load pipeline: source merge, hierarchy collapse, tenant expansion, resolution |
| `BenchmarkDecodeSmallStruct` | decode a 3-field configuration |
| `BenchmarkViewGetScalar` | typed scalar read through a pinned view |
| `BenchmarkViewDecodeTenant` | decode one tenant through a pinned view |
| `BenchmarkLoaderGetScalar` | the same read through `Loader.Get` for comparison |

Package-level benchmarks additionally cover file decoding (`encoding`,
`layers`), expression resolution (`internal/expr`) and the struct decoder
(`internal/decode`).

## Environment

- CPU: AMD Ryzen 7 9700X 8-Core Processor
- OS: Linux, amd64
- Go: 1.27

## Results

Median values, `go test -run '^$' -bench . -benchmem -count=10`:

| Benchmark | time/op | bytes/op | allocs/op |
| --- | ---: | ---: | ---: |
| `DecodeStruct` (root + 1001 tenants) | 173 µs | 213,240 B | 9 |
| `DecodeTenantStruct` | 159 ns | 80 B | 1 |
| `DecodeTenantMap` | 208 ns | 672 B | 4 |
| `DecodeMap` | 237 µs | 754,721 B | 4,010 |
| `LoadDocument` (1001 tenants) | 0.73 ms | 1,281,116 B | 8,045 |
| `DecodeSmallStruct` | 91 ns | 32 B | 1 |
| `ViewGetScalar` | 11.7 ns | 0 B | 0 |
| `ViewDecodeTenant` | 142 ns | 80 B | 1 |
| `ViewTenantLookup` | 34 ns | 112 B | 1 |
| `LoaderGetScalar` | 46 ns | 48 B | 1 |

Decoder package benchmarks (`internal/decode`):

| Benchmark | time/op | bytes/op | allocs/op |
| --- | ---: | ---: | ---: |
| `DecodeTenantsMap` (1001 tenants) | 200 µs | 213 KB | 9 |
| `DecodeTenantStruct` | 159 ns | 80 B | 1 |
| `DecodeScalarStruct` | 107 ns | 48 B | 1 |

Load pipeline and HTTP API:

| Benchmark | time/op | bytes/op | allocs/op |
| --- | ---: | ---: | ---: |
| `NewSnapshot` (1001 tenants) | 89 µs | 71 KB | 9 |
| `LoadDocument` (full pipeline) | 0.73 ms | 1.28 MB | 8,045 |
| `ConfigEndpoint` (`/v1/config`) | 434 µs | 217 KB | 29 |
| `TenantEndpoint` (`/v1/tenants/{code}`) | 1.33 µs | 1.6 KB | 24 |

Runtime overrides rebuild the snapshot copy-on-write: only maps along override
paths are cloned and the rest of the pristine document is shared, so a
published snapshot is never mutated and memory does not double.

| Benchmark | time/op | bytes/op | allocs/op |
| --- | ---: | ---: | ---: |
| `SetOverride` (global, 1001 tenants) | 337 µs | 827 KB | 4,027 |
| `SetTenantOverride` (one tenant) | 115 µs | 155 KB | 26 |
| `DeleteOverride` (last override) | 92 µs | 71 KB | 11 |
| `SetOverrides` (10 global paths) | 2.02 ms | 4.8 MB | 27.1k |
| 10 separate `SetOverride` calls | 20.5 ms | 47.8 MB | 269k |

A global override clones every tenant map because each tenant receives the
value; a tenant-scoped override clones one tenant and shares the other 1000.
Deleting the last override republishes the pristine snapshot without copying.
Batch calls validate every path, then apply all of them in a single rebuild.
They are about 10x faster than sequential single-path calls, which reapply the
growing override set on every rebuild (note the superlinear cost above: each
call rebuilds the whole snapshot). Mutations are administrative operations:
decoding after an override costs the same as before (+3%), while `Map` grows
with the extra returned data (357 µs vs 249 µs with one global override).

Expression resolution (`internal/expr`):

| Benchmark | time/op | bytes/op | allocs/op |
| --- | ---: | ---: | ---: |
| `CompileCached` | 16 ns | 0 B | 0 |
| `ResolveNoMarkers` | 1.4 µs | 24 B | 1 |
| `ResolveTypical` | 3.3 µs | 1,076 B | 45 |
| `ResolveLargeDocument` (1001 tenants) | 1.16 ms | 731 KB | 26.7k |

Source decoding (`layers`):

| Benchmark | time/op | throughput |
| --- | ---: | ---: |
| `DecodeYAML1000Tenants` | 16.9 ms | 31 MB/s |
| `DecodeJSON1000Tenants` | 1.46 ms | 193 MB/s |

JSON uses `encoding/json/v2` when the toolchain enables `goexperiment.jsonv2`
(the module's target 1.27 toolchain does). With `GOEXPERIMENT=none` the v1
fallback decodes the same document in 2.08 ms (135 MB/s). Before this change
the v1 decoder, which delegates to v2 when the experiment is enabled, took
3.91 ms.

### Comparison with the measured baseline

The baseline is the layered configuration implementation that this library
replaces, measured on the same machine and with the same 1001-tenant fixture.
It is recorded here as the performance target; no baseline code or names are
present in this repository.

| Benchmark | baseline time | fastconf time | speedup | baseline allocs | fastconf allocs |
| --- | ---: | ---: | ---: | ---: | ---: |
| Root struct + 1001 tenants | 2.77 ms | 173 µs | 16.0× | 49,150 | 9 |
| Single tenant struct | 2.82 µs | 159 ns | 17.7× | 46 | 1 |
| Single tenant map | 252 ns | 208 ns | 1.21× | 4 | 4 |
| Whole configuration map | 282 µs | 237 µs | 1.19× | 4,019 | 4,010 |

Read views are a new API with no baseline equivalent; they exist because map
copies are allocation-bound. A pinned view resolves a typed scalar in 11.7 ns
without allocating, and a tenant decode through a view is ~9% faster than the
equivalent loader call because it skips the snapshot and tenant lookup.

Struct decoding is where the rewrite pays off: a 1001-tenant decode went from
49,150 allocations to 9. The map-returning methods are deep copies by contract,
so their cost is proportional to the size of the returned map; they are at
parity with the baseline on allocations and slightly faster on time.

## Optimizations applied

Each optimization was validated with pprof and a before/after benchmark.

1. **Immutable value tree for decoding.** Documents are converted once into
   `internal/value`, where scalars are stored unboxed and objects keep parallel
   key/value slices. Decoding reads the tree without locking; a 1001-tenant
   struct decode dropped from 2.77 ms to 595 µs with this change alone.
2. **Cached struct plans.** Field tags, embedded/squash flattening and the
   case-insensitive name table are computed once per `reflect.Type` and cached
   in a `sync.Map` with two atomic hot slots. The decode loop does no tag
   parsing and no per-object name maps.
3. **Single map-entry temporary.** `decodeMap` allocates one addressable
   temporary for entries and one for keys, reuses them across entries and lets
   `SetMapIndex` copy the value. A 1001-tenant map decode went from ~4,000
   allocations to 9.
4. **No type inspection on the common scalar path.** Plain `string`→`string`,
   numeric→numeric and bool→bool assignments bypass `reflect.Type.PkgPath`,
   time/duration/URL checks and decode hooks. `PkgPath` disappeared from CPU
   profiles and root decode fell from 595 µs to ~215 µs.
5. **Direct validation.** Self-validation uses a direct interface assertion on
   the destination pointer. The previous reflective element copy allocated the
   whole destination struct on every decode.
6. **Fast placeholder resolution.** Strings without `${`/`#{` markers are not
   parsed, compiled templates are cached, and resolution mutates the document
   in place. `Compile` of a cached template is 16 ns with zero allocations.
7. **Bulk map cloning for copies.** `Map` and `TenantMap` clone each object with
   `maps.Clone` and then replace only nested containers, instead of inserting
   every key into a fresh map. Whole-document copy fell from 259 us to 237 us
   and a tenant copy from 230 ns to 208 ns with the same allocation count; the
   remaining cost is the mandatory map allocation per nested object.
8. **Zero-copy read views.** `Loader.View` pins one immutable snapshot and
   exposes typed getters, tenant subviews and zero-copy struct decoding. Path
   segments are collected in a stack buffer, so `view.String("domain")` is
   11.7 ns with zero allocations. `Get` still copies objects and lists by
   design; `Map` and `TenantMap` remain the explicit mutable-copy APIs.
9. **Allocation-free hierarchy merge.** `internal/maps` implements deep merge
   and copy with direct type switches instead of reflection-based merging, and
   the file hierarchy reads and remaps each directory document once.

10. **Exact scalar fast paths in the decoder.** `decode` now assigns predeclared
    string, int, int64, bool and float64 destinations directly instead of going
    through named-type inspection, the generic assignment dispatch, hook checks
    and special-type checks. Struct decode dropped from 210 us to 192 us, a
    single tenant from 185 ns to 164 ns, and `map[string]Tenant` decode from
    231 us to 205 us; the scalar struct benchmark went from 121 ns to 112 ns.
    A later layout change (item 13) brought these to 173 us and 159 ns.
11. **Canonical JSON fast path.** `encoding/json` already produces canonical
    objects and lists, so the JSON decoder is registered as canonical and
    `encoding.Decode` skips the normalization traversal for it. YAML and TOML,
    which can produce `map[any]any` or typed slices, still normalize, and so do
    decoders installed with `encoding.Register`. JSON document decoding fell
    from 4.35 ms to 3.91 ms and file loading from 5.02 ms to 4.61 ms.

12. **Fused tenant expansion.** Tenant expansion no longer deep-copies the whole
    root for every tenant and then merges the tenant into that copy. Root
    defaults are collected once; each merged object adopts the tenant's own
    containers (the load owns the document and replaces `tenants[code]`) and
    fills only missing defaults, recursing when both sides hold objects. The
    full load went from 1.19 ms to 0.73 ms, bytes from 2.44 MB to 1.28 MB and
    allocations from 14,079 to 8,045. The previous algorithm is kept in tests as
    a reference implementation.
13. **80-byte Value.** `Value` stored mutually exclusive `int64` and `float64`
    payloads in separate words. Packing them into one `uint64` (integer value or
    `math.Float64bits`) shrinks it from 88 to 80 bytes, and it is copied by
    value through every decode call and object access: struct decode −9.4%,
    view tenant decode −9.1%, tenant struct decode −7.1%, with identical
    allocation counts. Reordering fields alone measured exactly zero gain.
14. **Snapshot preallocation.** `newSnapshot` knows the tenant count but built
    its tenant map and code slice without hints. Pre-sizing them cut snapshot
    construction from 122.7 us to 88.5 us, from 141 KB to 70 KB and from 34 to 9
    allocations.
15. **Snapshot streaming in the API.** The config API previously called
    `Map`/`TenantMap`, whose deep copies exist only to protect callers from
    mutating the snapshot, and then encoded the copy. `View.WriteJSON` streams
    the pinned immutable snapshot directly; the handler prefers it through an
    optional interface and keeps the copying path for other `Source`
    implementations. `/v1/config` fell from 706 us to 430 us and from 4,042 to
    28 allocations; `/v1/tenants/{code}` fell by 15%.

16. **encoding/json/v2 for JSON sources.** When the toolchain enables the
    `jsonv2` experiment, JSON documents are decoded by `jsonv2.UnmarshalRead`
    instead of the v1 decoder, which itself delegates to v2 with v1 options and
    is considerably slower. JSON document decoding fell from 3.91 ms to 1.46 ms
    (193 MB/s), bytes/op from 3.79 MB to 2.38 MB and allocations from 61.3k to
    41.1k. A `!goexperiment.jsonv2` file keeps the v1 decoder for builds where
    the experiment is off; both modes pass the full suite. v2 runs with its
    strict defaults: duplicate member names and invalid UTF-8 are rejected
    rather than silently tolerated, and enabling v1-compatible tolerance
    options would cost about 2.3x in decoding time.

## Running

```bash
# All benchmarks, ten samples each.
go test -run '^$' -bench . -benchmem -count=10 ./... | tee bench.txt

# Compare two runs.
benchstat before.txt after.txt
```

## Explored and not adopted

### go-optimization-guide

The techniques in
[astavonin/go-optimization-guide](https://github.com/astavonin/go-optimization-guide)
were evaluated against this codebase. Adopted: batch/fuse copies (item 12),
payload packing instead of field reordering (item 13), collection preallocation
(item 14) and sharing immutable data instead of copying it (item 15).

Rejected with measurements: building maps with `make`+assign instead of
`maps.Clone` is 12.6% slower for a 1001-key map in Go 1.27 because Swiss-map
clone copies groups in bulk (the guide's advice predates that); reordering
`Value` fields without shrinking the payload gives exactly zero (82-byte
payload rounds to 88 bytes); `sync.Pool` for response buffers and encoders
changes nothing (619 vs 611 ns, same allocations); `weak`/`unique` caches add
cost to a 16 ns, allocation-free template cache; and unsafe string/pointer
tricks are excluded by the safety-first design. The guide's claim that boxing
always allocates regardless of size is also outdated: small integers use
`runtime.staticuint64s`.

Two Go memory features were evaluated with the same fixtures and deliberately
kept out of the hot path.

### `unique` string interning

Interning object keys as `unique.Handle[string]` was prototyped on 1001
objects with four keys each:

| Variant | build time | retained memory | key access |
| --- | ---: | ---: | ---: |
| plain string keys | 105 µs | 159 KB | 1.02 ms |
| interned handles | 155 µs | 114 KB | 1.06 ms |

Interning saves 28% of retained key memory but costs 48% more build time and
about 4% on key access. It is not adopted because the value tree already
shares key bytes with the decoded document map: `Object` copies string headers
that point at the parser's bytes. Interning would copy the bytes into the
intern table while the raw document kept its own copies, increasing total
retained memory. The tree is also built lazily, so Map-only workloads would pay
the interning cost without ever decoding.

### `weak` pointers

`weak.Make` plus `weak.Pointer.Value` costs about 16 ns per access in isolation.
Every cache in the library is small and must stay warm to be useful:

- struct plans are looked up per type on every decoded value; a weak plan
  cache would turn nanosecond hits into rebuilds;
- compiled placeholder templates are 16 ns per cached evaluation; weak values
  would drop them under GC and force recompilation;
- the value tree costs 701 µs and 1.84 MB to build for 1001 tenants, so it must
  stay strongly referenced once a caller decodes; making it weak would rebuild
  it after a GC instead of serving a decode.

A weak cache would therefore add overhead without reducing a meaningful
working set. It is not adopted.

### `unsafe` field assignment

A prototype decoded 1001 tenants with six string fields using
`reflect.Value.SetString` versus `unsafe.Pointer` offsets:

| Layer | reflect | unsafe |
| --- | ---: | ---: |
| field writes only | 15.4 µs | 2.0 µs (7.5× faster) |
| full generic map decode | 46 µs | 35 µs (23% faster) |

The gap shrinks in a full decode because map allocation and
`reflect.Value.SetMapIndex` cannot be replaced by field-offset writes, and in
the real decoder field writes are a small share of per-tenant cost. The safe
exact-type fast paths above delivered a comparable 9-11% improvement without
unsafe. The approach is not adopted: it does not produce a large end-to-end
win, it sacrifices memory safety and portability, and it would need careful
offset handling for embedded, squashed and pointer fields.

### json-iterator (`github.com/json-iterator/go`)

Evaluated as a potential JSON codec replacement. The repository is archived on
GitHub (last code commit 2022) and implements its speed with `unsafe` and
`github.com/modern-go/reflect2` field access. Benchmarks on the same machine
and a 1000-tenant JSON fixture:

| Operation | encoding/json | jsoniter | allocations (stdlib → jsoniter) |
| --- | ---: | ---: | ---: |
| decode to `map[string]any` | 1.36 ms | 0.57 ms | 25,195 → 28,035 |
| decode to structs | 0.50 ms | 0.37 ms | 5,025 → 14,025 |
| encode `map[string]any` | 0.39 ms | 1.00 ms | 7 → 23,052 |

Decoding to generic maps is 2.4× faster, but every other shape allocates more,
and encoding—what the bundled configuration API server does—is 2.6× slower.
The library's JSON work is load-time only, so it is not adopted, especially
because the dependency is archived and unsafe-based. Applications that do want
it can install it through `encoding.Register`; registered decoders are
normalized automatically.

### Single value representation

Keeping only the value tree and materialising `Map` from it was also measured:

| Operation | from raw document | from value tree |
| --- | ---: | ---: |
| whole config map | 268 µs, 754 KB, 4,008 allocs | 237 µs, 834 KB, 9,009 allocs |
| one tenant map | 197 ns, 672 B, 4 allocs | 187 ns, 752 B, 9 allocs |

Tree conversion is slightly faster for the whole document but doubles the
allocation count and increases bytes, because every object is rebuilt as a Go
map. Building the tree eagerly would also add 701 µs to every load. The library
therefore keeps the raw document for copies and builds the decode tree lazily.
