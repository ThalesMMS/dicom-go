# Large-dataset benchmark workflow

The scalable parser and Part 10 benchmarks are informational performance
evidence. They report `ns/op`, `B/op`, `allocs/op`, processed bytes and
throughput across stable sub-benchmarks for many elements, deep sequences,
large native and encapsulated Pixel Data, streaming thresholds, deferred
locations, reads, writes and round trips.

Large payloads are generated in memory or in temporary files; no bulky corpus
is committed. Encapsulated cases measure Part 10 container, fragment and
deferred-I/O behavior with structural synthetic bytes. They do not invoke or
qualify an RLE/JPEG decoder; codec conformance remains in the codec-specific
gates and fixture suites.

Wall-clock measurements depend on CPU model, power state, operating-system
scheduling, compiler version and background load. `make check` therefore does
not apply a percentage threshold to these results. Correctness, resource-limit
and allocation-ownership tests remain deterministic gates in their package
tests; this workflow is a separate regression investigation tool.

## Comparable runs

Compare commits only when both contain the same benchmark suite. Use the same
machine, Go toolchain, working-tree state and Make variables for both runs. Run
the baseline immediately before the candidate, close unrelated workloads and
repeat in the opposite order when a small difference matters.

From each commit's `dicom-go` directory:

```sh
make benchmark-large BENCH_COUNT=6 BENCH_TIME=250ms BENCH_CPU=1 > /tmp/baseline.txt
make benchmark-large BENCH_COUNT=6 BENCH_TIME=250ms BENCH_CPU=1 > /tmp/candidate.txt
```

Each command executes exactly these top-level benchmark families:

- parser: `BenchmarkReaderNextScalable*` and
  `BenchmarkReadDataSetScalable*`;
- object: `BenchmarkScalablePart10Read`, `BenchmarkScalablePart10Write`,
  `BenchmarkScalablePart10RoundTrip`, and
  `BenchmarkScalablePart10DeferredRead`.

The sub-benchmark path is part of the baseline identity. Do not compare renamed,
added or removed sizes as though they were paired samples.

## Pinned benchstat comparison

Install the repository-pinned `benchstat` into a disposable tool directory and
compare the two result files:

```sh
tools=$(mktemp -d)
make benchstat-install BENCHSTAT_INSTALL_DIR="$tools"
make benchmark-compare \
  BENCHSTAT="$tools/benchstat" \
  BASE_BENCH=/tmp/baseline.txt \
  CANDIDATE_BENCH=/tmp/candidate.txt
```

`benchmark-compare` first normalizes away only Go's trailing CPU suffix and
requires the complete sub-benchmark names and sample counts to match exactly.
It then runs `golang.org/x/perf/cmd/benchstat` at the pinned revision declared by
`BENCHSTAT_VERSION` in the Makefile. A statistically significant difference is
evidence to investigate, not an automatic merge failure; inspect throughput,
allocations and the affected size/family together.

Run these comparisons locally on the same controlled host. GitHub Actions is
outside this repository's benchmark workflow. For separately qualified cohorts
outside this module, see [equivalent-work measurements](../../dicom-go-validation/docs/LIBRARY_BENCHMARKS.md); they
reuse the same pinned benchstat and exact-name/sample-count checks. The scalable
local baseline commands above remain unchanged.
