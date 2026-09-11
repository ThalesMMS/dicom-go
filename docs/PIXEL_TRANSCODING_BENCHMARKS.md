# Pixel Transcoding Benchmark Evidence

This report records the first pure-Go RLE encode profiles added with the
explicit encoder registry. The fixtures are deterministic synthetic pixels and
contain no patient data.

## Environment

- Recorded: 2026-08-08
- OS/architecture: macOS 26.5.2, darwin/arm64
- CPU: Apple M4
- Go: go1.26.4
- Command: `go test ./pixeldata/rle -run '^$' -bench 'BenchmarkEncoder(Mono8|RGB16)$' -benchmem -benchtime=100ms -count=1`

The `peak-heap-delta-bytes` metric samples `runtime.MemStats.HeapAlloc` every
100 microseconds while the benchmark runs and reports the largest increase
over a forced-GC baseline. It is a bounded in-process heap peak, not resident
set size.

| Profile | Time/op | Throughput | Allocated/op | Allocs/op | Peak heap delta |
| --- | ---: | ---: | ---: | ---: | ---: |
| RLE mono 512x512 8-bit | 455,521 ns | 575.48 MB/s | 270,851 B | 2 | 7,585,384 B |
| RLE RGB 512x512 16-bit | 2,669,000 ns | 589.31 MB/s | 1,589,772 B | 2 | 14,308,632 B |

These values are qualification evidence for this environment, not a portable
performance guarantee. Re-run the command for release platforms and record a
new report rather than overwriting this one.
