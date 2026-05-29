# JPEG 2000 / HTJ2K Production Profile Decision

Date: 2026-06-22

Issue: [#135](https://github.com/ThalesMMS/go-dev/issues/135)

## Decision

The pure-Go `github.com/mrjoshuak/go-jpeg2000` adapter remains the supported
dependency-free opt-in JPEG 2000 / HTJ2K adapter for the controlled
fixture-backed `dicom-go` and Twin-Viewer `-tags jpeg2000` profile.

It is **not** sufficient by itself for the clinical `codecfull` production
profile. The qualified profile uses OpenJPEG 2.5.4 for JPEG 2000 Part 1/2 and
OpenJPH 0.31.0 for HTJ2K. Its external clinical fixtures, independent pixel
references, fail-closed runtime checks, and performance evidence are documented
in [`../../../docs/CODECFULL_PROFILE.md`](../../../docs/CODECFULL_PROFILE.md).
The packaged `ojph_expand` executable must have an adjacent
`<executable>.codecfull` sidecar containing exactly the qualified OpenJPH
version and source commit exposed by `QualifiedOpenJPHMarker`; this stable
packaging marker avoids depending on incidental strings retained in a stripped
binary.

## Covered Profile

The pure-Go `-tags jpeg2000` adapter profile covers:

- JPEG 2000 lossless and lossy still-image transfer syntaxes.
- JPEG 2000 Part 2 UIDs through raw profile smoke coverage where the current
  pure-Go encoder can produce valid codestreams.
- HTJ2K lossless, RPCL lossless and lossy still-image transfer syntaxes.
- Unsigned MONOCHROME1/MONOCHROME2 frames with `BitsAllocated` 8 or 16 and
  `BitsStored` up to `BitsAllocated` when `HighBit == BitsStored-1`.
- Unsigned RGB frames with `BitsAllocated` 8 or 16 and interleaved output bytes
  when `HighBit == BitsStored-1`.
- Single fragment per frame and single-frame multi-fragment assembly.
- Typed errors for malformed codestreams, unsupported fragment layouts,
  DICOM metadata mismatches and codestream metadata mismatches.
- Twin-Viewer `LoadDICOMPaths` coverage for JPEG 2000 lossless, JPEG 2000
  lossy and HTJ2K through `internal/viewer/study` when built with
  `-tags jpeg2000`.

The adapter validates codestream width, height, component count, component
precision against DICOM `BitsStored`, and signedness before converting through
Go `image.Image`, so RGBA, bit-depth or dimension mismatches cannot be silently
converted into DICOM-compatible native frames.

## Known Gaps

The current pure-Go backend is intentionally limited. These gaps do not
describe the separate `codecfull` release profile:

- External clinical and OpenJPEG/OpenJPH-generated fixtures are deliberately
  outside the pure-Go adapter's qualification boundary. They are checked in,
  hash-verified, and exercised by the separate `codecfull` profile with its
  qualified native runtimes.
- The dependency's tile-data path documents that one decode path only works for
  codestreams produced by its own encoder and returns no tile data for external
  T2 packet layouts (`github.com/mrjoshuak/go-jpeg2000@v1.2.1/decoder.go`).
  Therefore this pure-Go decision remains limited to its synthetic/self-encoded
  conformance profile; external-fixture coverage belongs to `codecfull` and
  does not qualify the pure-Go backend.
- Full JPX/JPEG 2000 Part 2 extension coverage is not proven; current Part 2
  coverage is raw-profile smoke coverage only.
- 16-bit lossless high-value fixtures decode within 1 LSB but are not exact near
  the upper stored-value range; this prevents claiming full lossless production
  exactness.
- JPIP, streaming transfer syntaxes, video transfer syntaxes, transcoding and
  progressive external retrieval are out of scope for this still-image adapter.

## Benchmark Evidence

Command used for the table below:

```sh
go test -bench BenchmarkDecodeJPEG2000Profile -benchmem ./...
```

The `make codec-jpeg2000-profile-check` target intentionally runs only a
`-benchtime=1x` benchmark smoke and is not comparable to this table.

Environment:

- `goos`: darwin
- `goarch`: arm64
- CPU: Apple M4

Latest local result:

| Case | Time | Memory | Allocations |
|------|------|--------|-------------|
| small/lossless | 23748 ns/op | 208741 B/op | 191 allocs/op |
| small/lossy | 18809 ns/op | 209080 B/op | 192 allocs/op |
| small/htj2k | 18492 ns/op | 208852 B/op | 193 allocs/op |
| medium/lossless | 360127 ns/op | 385342 B/op | 499 allocs/op |
| medium/lossy | 1031770 ns/op | 429550 B/op | 506 allocs/op |
| medium/htj2k | 371362 ns/op | 385356 B/op | 501 allocs/op |
| large/lossless | 6547368 ns/op | 1829722 B/op | 1351 allocs/op |
| large/lossy | 11416959 ns/op | 2505882 B/op | 1357 allocs/op |
| large/htj2k | 828175 ns/op | 802664 B/op | 1134 allocs/op |

## Verification Commands

Run these for this profile:

```sh
go test ./...
go test -bench BenchmarkDecodeJPEG2000Profile -benchmem ./...
```

From the `dicom-go-dev` root:

```sh
make codec-jpeg2000-profile-check
```

From the `Twin-Viewer` root:

```sh
make codec-profile-check
```
