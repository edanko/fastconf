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
| `DecodeStruct` (root + 1001 tenants) | 210 µs | 213,240 B | 9 |
| `DecodeTenantStruct` | 185 ns | 80 B | 1 |
| `DecodeTenantMap` | 208 ns | 672 B | 4 |
| `DecodeMap` | 237 µs | 754,721 B | 4,010 |
| `LoadDocument` (1001 tenants) | 1.19 ms | 2,444,890 B | 14,079 |
| `DecodeSmallStruct` | 91 ns | 32 B | 1 |
| `ViewGetScalar` | 11.7 ns | 0 B | 0 |
| `ViewDecodeTenant` | 168 ns | 80 B | 1 |
| `ViewTenantLookup` | 34 ns | 112 B | 1 |
| `LoaderGetScalar` | 46 ns | 48 B | 1 |

Decoder package benchmarks (`internal/decode`):

| Benchmark | time/op | bytes/op | allocs/op |
| --- | ---: | ---: | ---: |
| `DecodeTenantsMap` (1001 tenants) | 231 µs | 213 KB | 9 |
| `DecodeTenantStruct` | 185 ns | 80 B | 1 |
| `DecodeScalarStruct` | 121 ns | 48 B | 1 |

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
| `DecodeJSON1000Tenants` | 4.35 ms | 68 MB/s |

### Comparison with the measured baseline

The baseline is the layered configuration implementation that this library
replaces, measured on the same machine and with the same 1001-tenant fixture.
It is recorded here as the performance target; no baseline code or names are
present in this repository.

| Benchmark | baseline time | fastconf time | speedup | baseline allocs | fastconf allocs |
| --- | ---: | ---: | ---: | ---: | ---: |
| Root struct + 1001 tenants | 2.77 ms | 210 µs | 13.2× | 49,150 | 9 |
| Single tenant struct | 2.82 µs | 185 ns | 15.3× | 46 | 1 |
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

## Running

```bash
# All benchmarks, ten samples each.
go test -run '^$' -bench . -benchmem -count=10 ./... | tee bench.txt

# Compare two runs.
benchstat before.txt after.txt
```

## Explored and not adopted

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
