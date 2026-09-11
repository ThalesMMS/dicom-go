# Campaign summary (local host)

Recorded on Apple M4 Pro, darwin/arm64, Go 1.27.0, `GOMAXPROCS=12`.
`djxl` 0.12.0 and `libjxl` 0.12.0 via pkg-config (codecfull pin remains 0.11.2).
Warm-up: 1 discarded iteration. Timed warm samples: 4, interleaved across backends.
Clock overhead: 21334 ns.
OS page cache hot; no disk-cache drop.
Power/frequency scaling was not pinned.

## Decision

**recommend-helper** for JPEG XL.

Helper first-frame p95 was lower than CLI on `jpegxl-gray16-warm` and
`jpegxl-series-switch`, and helper cold first-frame was within one CLI spawn of
CLI cold. `cmd.Wait` is not treated as algorithm-only.

This is the decision prototype for a later production helper. It does not
implement that helper. The production worker is documented in
[`HELPER.md`](HELPER.md) (issue `#924`).

## Measured first-frame (milliseconds)

| Cohort | Backend | p50 | p95 | prepare | launch | execute/Wait | convert | launches | peak child RSS |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| jpegxl-gray16-warm | jpegxl-cli | 9.166 | 9.360 | 0.211 | 0.917 | 7.926 | 0.063 | 1 | 2.8 MiB |
| jpegxl-gray16-warm | jpegxl-pipe | 9.207 | 9.241 | 0.012 | 0.870 | 8.207 | 0.014 | 1 | 2.7 MiB |
| jpegxl-gray16-warm | jpegxl-helper-proto | 0.384 | 0.386 | 0.001 | 0.010 | 0.280 | 0.007 | 0 | 7.0 MiB |
| jpegxl-series-switch | jpegxl-cli | 13.428 | 13.428 | 1.392 | 1.108 | 8.440 | 0.063 | 1 | 1.2 MiB |
| jpegxl-series-switch | jpegxl-helper-proto | 0.591 | 0.591 | 0.001 | 0.008 | 0.449 | 0.018 | 0 | 7.3 MiB |
| jpegxl-gray16-cold | jpegxl-cli | 8.968 | 8.968 | 0.310 | 0.815 | 7.717 | 0.058 | 1 | 2.7 MiB |
| jpegxl-gray16-cold | jpegxl-helper-proto | 0.321 | 0.321 | 0.002 | 0.008 | 0.251 | 0.004 | 0 | 6.4 MiB |
| jpegxl-gray16-cache-hit | cache | 0.010 | 0.011 | 0.002 | 0.003 | 0 | 0.005 | 0 | 0 |
| jpegxl-long-series | jpegxl-cli | 8.996 | 9.531 | 0.214 | 0.856 | 7.803 | 0.058 | 1 | 3.4 MiB |
| jpegxl-long-series | jpegxl-helper-proto | 0.348 | 0.372 | 0.001 | 0.010 | 0.262 | 0.004 | 0 | 7.2 MiB |
| jpegxl-large-gray16-warm | jpegxl-cli | 10.852 | 12.041 | 0.273 | 0.852 | 9.184 | 0.400 | 1 | 2.8 MiB |
| jpegxl-large-gray16-warm | jpegxl-helper-proto | 2.143 | 2.145 | 0.003 | 0.133 | 1.761 | 0.005 | 0 | 9.2 MiB |

Pipe (stdin/stdout) reduced prepare relative to temp files but still launched
`djxl` per frame. Cache-hit after a decoded-frame hit did not relaunch.
After helper `Close`, the helper PID was gone and no `dicom-go-codec-cost-*`
temp directories remained.

Child RSS is sampled while the process is alive. Go heap (`allocBytes` /
`heapInuseBytes`) is not native process memory.

## Skips

- `jpegxl-multiframe`: still-image JPEG XL only.
- `pacs-horos`: `192.168.100.62:4007` unreachable.
- JPEG-LS CharLS: already in-process; not a CLI helper candidate here.
- OpenJPEG: same CLI pattern as JPEG XL; helper comparison was JPEG XL only.

## Twin first-frame

`DICOM_Example/dicom_series_example.zip`: 1 study, 1 series, 234 slices,
Explicit VR Little Endian. `EnsurePixelBytes` was 375 ns because native pixels
were already resident after load. First present 1.553 ms; second present
250 ns was a render-cache hit. Native pixels, not JPEG XL.

## Files

- `raw/report.json`
- `raw/twin-first-frame.json`
- `raw/cpu.pprof`, `raw/heap.pprof`, `raw/trace.out` (local, no HTTP)
