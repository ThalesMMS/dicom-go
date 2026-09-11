# Codec per-frame cost measurement

This harness attributes JPEG XL decode cost by exclusive pipeline stage so a
helper process is adopted only when measured overhead dominates, not because a
native library exists in another product.

The production `djxl` adapter remains the default `Register()` backend.
Issue `#924` implements the session-owned `jpegxl-helper` worker. Measurement
still lives in `examples/codec-cost`; that prototype is not the production helper.

## Command

From `dicom-go`:

```sh
go run ./examples/codec-cost/cmd/measure \
  -compile-helper \
  -iterations 8 \
  -warmup 1 \
  -require jpegxl-cli \
  -out docs/benchmarks/2026-09-09-codec-frame-cost/raw/report.json \
  -cpuprofile docs/benchmarks/2026-09-09-codec-frame-cost/raw/cpu.pprof \
  -memprofile docs/benchmarks/2026-09-09-codec-frame-cost/raw/heap.pprof \
  -trace docs/benchmarks/2026-09-09-codec-frame-cost/raw/trace.out
```

`make codec-cost-measure` writes a new dated campaign directory, or `OUT=<path>`.
It refuses the historical `docs/benchmarks/2026-09-09-codec-frame-cost/` path and
any existing destination. `make check` only
runs the offline smoke tests (fake CLI, skip/fail-closed missing runtimes,
promotion rules). A long campaign is never a silent pass: `-require jpegxl-cli`
exits non-zero when `djxl` is absent.

Twin first-frame presentation (lazy decode, first present, second present
against the render cache, no Fyne window):

```sh
cd ../Twin-Viewer
go run ./cmd/codec-frame-cost \
  -in ../DICOM_Example/dicom_series_example.zip \
  -out ../dicom-go/docs/benchmarks/2026-09-09-codec-frame-cost/raw/twin-first-frame.json
```

## Stages

Exclusive, non-overlapping spans:

| Stage | What it includes | What it is not |
| --- | --- | --- |
| `preflight` | executable resolution | decode |
| `admission` | optional wait before launch | decode |
| `prepare` | temp directory and codestream write, or stdin setup | `cmd.Run` |
| `launch` | `cmd.Start` only | `Wait` |
| `execute` | `cmd.Wait` / in-helper decode | process create |
| `convert` | PNM parse and endian conversion | spawn |
| `deliver` | copy of native frame bytes to the caller | UI paint |

`firstFrame` is inclusive wall-clock from `Start` through `deliver`. It is
stored separately and is **not** summed with the exclusive spans. Production
`DecodeFrame` still uses `cmd.Run`; the harness splits `Start`/`Wait`. If a
path cannot split them, it must attach `CombinedLaunchExecuteNote()` and must
not label the span `execute`.

Warm-up iterations are discarded before warm percentiles. Clock overhead is
recorded as `instrumentation.clockOverheadNanoseconds`. Warm timed samples are
interleaved across backends. Subprocess RSS is sampled once while the child is
alive; sampling after `Wait` is not used because the PID is already gone.
`cmd.Wait` is never labeled algorithm-only.

## Backends

- `jpegxl-cli` — current temp-file CLI path (measurement clone, not a rewrite of the adapter).
- `jpegxl-pipe` — local optimization prototype: `djxl - - --output_format pgm|ppm`.
- `jpegxl-helper-proto` — persistent process, length-prefixed stdio, libjxl C API. Decision prototype only; production helper is out of scope.

JPEG 2000 OpenJPEG remains a per-frame CLI in production; this campaign does
not promote a helper for it. JPEG-LS CharLS is already in-process. JPEG XL
multiframe is skipped: still-image transfer syntaxes only.

## Cohorts and modes

Synthetic / codecfull non-PHI fixtures only (`jxl/gray16-lossless.jxl`,
`jxl/rgb8-lossy.jxl`, optional `cjxl` 512² gray). Modes: cold, warm, cache-hit
(decoded pixels reused, zero launches), cache-miss, series-switch (gray then
RGB), cancel-before-launch, and a 16-frame sequential gray series.

Lossless backends must match pixels exactly before timing is kept. Lossy RGB
uses the codecfull max-abs-error limit of 80. OS page cache is hot; this does
not drop disk cache. `djxl` 0.12.x on a host is recorded as-is; it may differ
from the 0.11.2 codecfull pin.

## Promotion criteria (fixed before comparison)

1. Recommend helper only if the helper ran, helper first-frame p95 is lower
   than CLI on `jpegxl-gray16-warm` and `jpegxl-series-switch`, and helper cold
   first-frame is no worse than CLI cold plus one CLI spawn (`launch+prepare`).
   `cmd.Wait` is **not** treated as algorithm-only: it includes child process
   initialization.
2. Otherwise point-optimize if `prepare` is the unique exclusive maximum on
   every warm JPEG XL size cohort.
3. Otherwise keep the CLI.

No invented gain percentages. No OsiriX log comparison.

## Results

Raw JSON, pprof/trace files, and the decision summary:

`docs/benchmarks/2026-09-09-codec-frame-cost/`

On the recording host the campaign chose **recommend-helper** for JPEG XL.
See [`benchmarks/2026-09-09-codec-frame-cost/SUMMARY.md`](benchmarks/2026-09-09-codec-frame-cost/SUMMARY.md).

Profiles stay local, are not uploaded, and must not include PHI. The JPEG XL
fixture tree used for adapter inventory is not a measurement corpus when it
carries clinical identifiers.

## Network

This campaign does not require PACS. A live node at AE Title `HOROS` /
`192.168.100.62:4007` is optional and recorded as skipped when unreachable.
