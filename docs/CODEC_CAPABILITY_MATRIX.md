# Still-image codec capability matrix

This document is the canonical human-readable codec inventory. The executable
source of truth is `pixeldata/codecprofile.CodecFullManifest`; local checks verify this
document against that manifest. Native uncompressed transfer syntaxes do not
need a pixel codec and are outside this table. Video and JPIP data-bin transfer
syntaxes are also outside the still-image codec registry.

`D` means decode and `E` means encode. A capability is available only when the
named profile is selected and every runtime required by that profile passes
preflight.

| Transfer syntax | D | E | Profile and implementation | Dependency / license | Qualified platforms |
|---|:---:|:---:|---|---|---|
| Encapsulated Uncompressed Explicit VR Little Endian (`1.2.840.10008.1.2.1.98`) | yes | yes | default; in-tree frame assembly | none; project license | Go-supported platforms |
| JPEG Baseline Process 1 (`1.2.840.10008.1.2.4.50`) | yes (SOF0, 8-bit) | yes (SOF0, 8-bit) | default; Go standard-library JPEG | Go standard library / BSD-3-Clause | Go-supported platforms |
| JPEG Extended Process 2/4 (`1.2.840.10008.1.2.4.51`) | yes (Process 2 SOF1 8-bit; Process 4 SOF1 12-bit monochrome) | no | default; Go standard library for 8-bit, in-tree pure-Go decoder for 12-bit | Go standard library / BSD-3-Clause and project license | Go-supported platforms |
| JPEG Lossless Process 14 (`1.2.840.10008.1.2.4.57`) | yes | no | default; pure-Go decoder | none; project license | Go-supported platforms |
| JPEG Lossless Process 14 SV1 (`1.2.840.10008.1.2.4.70`) | yes | no | default; pure-Go decoder | none; project license | Go-supported platforms |
| RLE Lossless (`1.2.840.10008.1.2.5`) | yes | yes | default; pure-Go codec | none; project license | Go-supported platforms |
| JPEG-LS Lossless (`1.2.840.10008.1.2.4.80`) | yes | yes | default pure-Go decoder; explicit pure-Go encoder | none; project license | Go-supported platforms |
| JPEG-LS Near-Lossless (`1.2.840.10008.1.2.4.81`) | yes (qualified unsigned profiles) | yes (explicit pure-Go NEAR and lossy authorization, unsigned ILV=0) | default: pure-Go decoder; `codecfull` / `jpegls_charls`: CharLS dynamic adapter | base: none; optional CharLS 2.4.2 / BSD-3-Clause and github.com/ebitengine/purego v0.10.1 / Apache-2.0 | codecfull decode: Windows/amd64, macOS/arm64; new encode oracle: Windows/amd64 |
| JPEG 2000 Part 1 Lossless (`1.2.840.10008.1.2.4.90`) | yes | yes (explicit optional unsigned 8..16-bit encoder) | `codecfull`: OpenJPEG; `jpeg2000_openjpeg`: explicit encoder; optional `jpeg2000`: pure Go decode | OpenJPEG 2.5.4 / BSD-2-Clause; optional go-jpeg2000 / Apache-2.0 | decode codecfull: Windows/amd64, macOS/arm64; encode: Windows/amd64, Linux/amd64; pure-Go decode: Go-supported platforms |
| JPEG 2000 Part 1 Lossy (`1.2.840.10008.1.2.4.91`) | yes | no | `codecfull`: OpenJPEG; optional `jpeg2000`: pure Go | OpenJPEG 2.5.4 / BSD-2-Clause; optional go-jpeg2000 / Apache-2.0 | codecfull: Windows/amd64, macOS/arm64; pure-Go profile: Go-supported platforms |
| JPEG 2000 Part 2 Lossless (`1.2.840.10008.1.2.4.92`) | yes | no | `codecfull`: OpenJPEG; optional `jpeg2000`: pure Go | OpenJPEG 2.5.4 / BSD-2-Clause; optional go-jpeg2000 / Apache-2.0 | codecfull: Windows/amd64, macOS/arm64; pure-Go profile: Go-supported platforms |
| JPEG 2000 Part 2 Lossy (`1.2.840.10008.1.2.4.93`) | yes | no | `codecfull`: OpenJPEG; optional `jpeg2000`: pure Go | OpenJPEG 2.5.4 / BSD-2-Clause; optional go-jpeg2000 / Apache-2.0 | codecfull: Windows/amd64, macOS/arm64; pure-Go profile: Go-supported platforms |
| HTJ2K Lossless (`1.2.840.10008.1.2.4.201`) | yes | no | `codecfull`: OpenJPH; optional `jpeg2000`: pure Go | OpenJPH 0.31.0 / BSD-2-Clause; optional go-jpeg2000 / Apache-2.0 | codecfull: Windows/amd64, macOS/arm64; pure-Go profile: Go-supported platforms |
| HTJ2K Lossless RPCL (`1.2.840.10008.1.2.4.202`) | yes | no | `codecfull`: OpenJPH; optional `jpeg2000`: pure Go | OpenJPH 0.31.0 / BSD-2-Clause; optional go-jpeg2000 / Apache-2.0 | codecfull: Windows/amd64, macOS/arm64; pure-Go profile: Go-supported platforms |
| HTJ2K Lossy (`1.2.840.10008.1.2.4.203`) | yes | no | `codecfull`: OpenJPH; optional `jpeg2000`: pure Go | OpenJPH 0.31.0 / BSD-2-Clause; optional go-jpeg2000 / Apache-2.0 | codecfull: Windows/amd64, macOS/arm64; pure-Go profile: Go-supported platforms |
| JPEG XL Lossless (`1.2.840.10008.1.2.4.110`) | yes | no | `codecfull` / optional `jpegxl_djxl`; command adapter | libjxl djxl 0.11.2 / BSD-3-Clause with patent grant | codecfull: Windows/amd64, macOS/arm64 |
| JPEG XL JPEG Recompression (`1.2.840.10008.1.2.4.111`) | yes | no | `codecfull` / optional `jpegxl_djxl`; command adapter | libjxl djxl 0.11.2 / BSD-3-Clause with patent grant | codecfull: Windows/amd64, macOS/arm64 |
| JPEG XL Lossy (`1.2.840.10008.1.2.4.112`) | yes | no | `codecfull` / optional `jpegxl_djxl`; command adapter | libjxl djxl 0.11.2 / BSD-3-Clause with patent grant | codecfull: Windows/amd64, macOS/arm64 |

## Profiles and fail-closed behavior

- `default` is dependency-free at runtime. `pixeldata/builtin.NewRegistry`
  registers only the built-in decoders, including the qualified JPEG-LS
  Lossless subset; encoders are selected explicitly by transcode APIs. Missing
  compressed decoders return `pixeldata.ErrCodecNotFound`.
- Optional tags (`jpeg2000`, `jpeg2000_openjpeg`, `jpegls_charls`, and
  `jpegxl_djxl`) enable only their named adapters. A tag never proves that its
  external runtime is installed.
- `codecfull` is the release profile. Startup preflights CharLS, OpenJPEG,
  OpenJPH, and `djxl`, including qualified versions and packaging evidence. Any
  missing or incompatible runtime aborts registration; partial capability is
  never advertised.
- `codec-capabilities.json` is generated from the schema-v2 manifest and ships
  with codecfull packages. Consumers must use its `directions`, dependencies,
  evidence, and readiness fields instead of inferring support from build tags.

JPEG Baseline is the 8-bit Process 1/SOF0 path. JPEG Extended keeps 8-bit
Process 2/SOF1 decoding on the Go standard library and uses the in-tree pure-Go
Process 4/SOF1 path only for 12-bit, single-component unsigned samples. Process
4 requires BitsAllocated=16, BitsStored=12, HighBit=11,
PixelRepresentation=0, and MONOCHROME1 or MONOCHROME2. Signed samples and 9-11
bit precision are rejected. Frames may span multiple Items through the
[shared bounded assembly](ENCAPSULATED_FRAME_ASSEMBLY.md). JPEG Extended encoding
is not provided.

JPEG Lossless Process 14 supports one- and three-component SOF3 streams at 8
or 16 allocated bits. SOF3 precision must equal BitsStored and HighBit must be
BitsStored-1: 2-8 bits when 8 bits are allocated or 2-16 bits when 16 bits are
allocated; one-bit samples are outside the builtin subset. Three-component
objects are limited to unsigned RGB or YBR_FULL, full-resolution 1x1 component
sampling, PixelRepresentation=0, and PlanarConfiguration=0; decoded native
samples are interleaved. The codestream may use one interleaved
three-component scan or three single-component scans in arbitrary component
order. Predictors 1-7 and point transforms from zero through precision-1 are
accepted per scan; the SV1 transfer syntax requires predictor 1 in every scan.
Component identifiers, dimensions, sampling, scan coverage, and tables are
validated before decoded frames are published. JPEG Lossless encoding is not
provided. The shared assembly supports multiple Items per frame using BOT or
unambiguous codestream boundaries; EOT remains restricted to one Item per frame.

The builtin JPEG-LS Lossless decoder supports NEAR=0 and ILV=0, plus unsigned
RGB ILV=1/2 with 2-16 stored bits in 8/16-bit containers, PlanarConfiguration=0,
matching precision and MAXVAL=2^precision-1. Default parameters and LSE ID=1
are accepted; see [the qualified profiles and full corpus](JPEGLS_INTERLEAVE.md).
Restart markers, NEAR>0, and color transforms remain outside this subset. DICOM
frame assembly is shared with the other JPEG codecs and optional CharLS adapter,
with populated Basic Offset Tables,
normative Extended Offset Table/Lengths pairs, and empty Basic Offset Tables
only when complete SOI/terminal-EOI boundaries make every frame unambiguous.
Independent pydicom/GDCM fixtures are compared bit-exactly; the pure-Go encoder
is additionally checked by a CharLS interoperability gate.

The builtin `.81` decoder separately qualifies unsigned MONOCHROME1/2 ILV=0
and RGB ILV=0/1/2, with explicit NEAR and source bounds. The `.80` codec remains
strictly lossless. See [Near-Lossless evidence and exclusions](JPEGLS_NEAR_LOSSLESS.md).
The separate [explicit Near-Lossless encoder](JPEGLS_NEAR_LOSSLESS_ENCODER.md)
uses the caller's NEAR bound and lossy policy; it is qualified for unsigned
MONOCHROME1/2 and RGB ILV=0 with all samples independently decoded by CharLS.

## Reproducible checks

Run from `dicom-go`:

```sh
make codec-policy-check          # dependency and license guardrails
make codec-optional-check        # dependency-free optional-module tests
make codec-conformance-check     # synthetic codec corpus
make codec-manifest-check        # schema, evidence and readiness
make codecfull-check             # all runtimes; requires codecfull environment
go run ./cmd/dicom-codec-manifest -require-ready
```

`make check` includes every check that does not require native runtimes. See
[`CODECFULL_PROFILE.md`](CODECFULL_PROFILE.md) for pinned runtime acquisition,
packaging, platform gates, evidence, and performance reports.

The [full-sample corpus contract](PIXEL_CORPUS.md) describes fixture metadata,
hashes, independent reconstruction, per-component/spatial metrics and explicit
base/optional exclusions. Corpus presence alone does not qualify a decoder.
