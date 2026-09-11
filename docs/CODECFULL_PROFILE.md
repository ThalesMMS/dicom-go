# Codecfull clinical release profile

Issue: [#554](https://github.com/ThalesMMS/go-dev/issues/554)

`codecfull` is the explicit, fail-closed release profile for DICOM still-image
pixel decoding. It keeps optional native dependencies out of the base
`dicom-go` module while qualifying them together in
`examples/codecfull`.

The canonical per-transfer-syntax decode/encode and dependency matrix is
[`CODEC_CAPABILITY_MATRIX.md`](CODEC_CAPABILITY_MATRIX.md). This document adds
the release evidence and packaging contract for that matrix.

From the `dicom-go` module, Twin Viewer codecfull production builds use:

```sh
cd ../Twin-Viewer
CODEC_TAGS=codecfull make build
```

The profile refuses to initialize unless all four audited runtimes are
available:

| Runtime | Qualified version | Override |
|---|---:|---|
| CharLS | 2.4.2 | `DICOM_GO_CHARLS_LIBRARY` |
| OpenJPEG `opj_decompress` | 2.5.4 | `DICOM_GO_OPENJPEG_DECOMPRESS` |
| OpenJPH `ojph_expand` | 0.31.0 | `DICOM_GO_OPENJPH_EXPAND` |
| libjxl `djxl` | 0.11.2 | `DICOM_GO_DJXL` |

When overrides are absent, packaged runtimes are resolved beside the executable
or in its `codec` resource directory. Missing or incompatible dependencies stop
codec registration; they never silently leave a production binary with partial
coverage. An optional `jpegxl-helper` worker may be packaged beside `djxl`; it
is not required to initialize `codecfull`. Fallback remains the qualified
`djxl` CLI. Every `ojph_expand` runtime also carries an adjacent
`<executable>.codecfull` qualification marker naming the audited OpenJPH version
and source commit, so stripped binary contents are not used as provenance.

## Qualified evidence

The redistribution-safe corpus is declared in
`pixeldata/codecfixture/testdata/codecfull/manifest.json`. It combines
de-identified pydicom/pydicom-data DICOM files with deterministic synthetic
JPEG XL codestreams and covers:

- JPEG 2000 Part 1 and Part 2, including lossless/lossy, signed CT, multiframe
  MR, and color ultrasound;
- HTJ2K lossless/lossy RGB plus independently encoded lossless RPCL;
- JPEG-LS lossless and near-lossless at 8 and 16 bits;
- JPEG XL lossless, lossy, and exact JPEG bitstream reconstruction;
- JPEG Baseline Process 1/SOF0 8-bit, JPEG Extended Process 2/SOF1 8-bit and
  Process 4/SOF1 12-bit unsigned monochrome, JPEG Lossless Process 14
  grayscale and full-resolution unsigned RGB/YBR_FULL, RLE
  RGB/grayscale/multiframe, and Encapsulated Uncompressed.

Exact native pairs and independent pydicom decoder reference points are checked
by `examples/codecfull/corpus_test.go`. Lossy cases declare their maximum
absolute error in the corpus manifest. Every published fixture and source
license is checksum/provenance gated.

The checked-in Windows/amd64 performance report records P50/P95/P99 decode time
and sampled peak working set for the Go process plus descendant codec
processes, using representative JPEG 2000, JPEG-LS, RLE, and HTJ2K studies:

```text
pixeldata/codecfixture/testdata/codecfull/performance/windows-amd64.json
```

Regenerate it from `examples/codecfull`:

```sh
go run -tags codecfull ./cmd/measure \
  -out ../../pixeldata/codecfixture/testdata/codecfull/performance/windows-amd64.json
```

## Release gates

Run from `dicom-go`:

```sh
make codec-manifest-check
make codecfull-check
go run ./cmd/dicom-codec-manifest -require-ready
```

The final command emits the auditable capability manifest and exits non-zero if
any capability, evidence, dependency, or packaging gate regresses.

Windows packaging downloads or builds checksum-pinned runtimes and verifies
their licenses. macOS and Linux packaging require explicit audited runtime and
license paths, preflight the complete profile, and copy those resources into
each application artifact. Every codecfull artifact includes
`codec-capabilities.json` and the four upstream license sets.

The checked-in Twin-Viewer package entrypoints gate the audited Windows/amd64
and macOS/arm64 profiles. The macOS entrypoint builds the same four exact
upstream versions before verifying runtime versions, transitive dependencies,
licenses, the corpus, and the manifest.
