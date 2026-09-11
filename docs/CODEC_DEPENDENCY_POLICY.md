# Codec Dependency Policy

`dicom-go` is the reusable DICOM parser, transfer syntax, pixel data, DIMSE and
DICOMweb library used by Twin-Viewer. Its default build must remain pure Go,
permissively licensed, and usable without native codec libraries.

## License and dependency rules

- Default module dependencies must be pure Go and permissively licensed under
  MIT, BSD, Apache-2.0, or similarly permissive terms.
- Do not vendor, link, port, generate from, or copy LGPL, GPL, AGPL, or other
  reciprocal/copyleft codec implementations into `dicom-go` or Twin-Viewer.
- Native or CGO codec adapters are allowed only when they are the best
  production path, are permissively licensed, and live behind an explicit build
  tag or nested optional module.
- Optional adapters must register codecs explicitly. Importing the base module
  must never register JPEG-LS Near-Lossless, JPEG 2000 / HTJ2K, JPEG XL, or
  future native adapters implicitly. The dependency-free JPEG-LS Lossless
  subset belongs to the explicit `pixeldata/builtin` registry.
- Decompression and transcoding helpers use explicit caller-owned decoder and
  encoder registries. Decoder availability never implies encoder availability,
  and there is no mutable default encoder registry.
- The default module may include audited pure-Go encoders such as RLE Lossless
  and JPEG-LS Lossless. Encoders remain explicitly registered and must publish
  direction-specific capability evidence before an application enables them.
- Metadata and encapsulated Pixel Data parsing must keep working without an
  optional codec. Missing decoder support should return typed unsupported or
  dependency-unavailable errors, not panic.

## Build profiles

The per-transfer-syntax source of truth is
[`CODEC_CAPABILITY_MATRIX.md`](CODEC_CAPABILITY_MATRIX.md); the table below
summarizes build profiles and their gates.

| Profile | Scope | Dependency rule | Expected command |
|---------|-------|-----------------|------------------|
| `default` | Base parser, transfer syntax registry, native pixel data, Encapsulated Uncompressed frame assembly, bounded decompression/transcoding API, built-in pure-Go decoders including JPEG-LS Lossless, and explicit pure-Go RLE/JPEG-LS Lossless encoders. | No native codec libraries and no optional adapter module requirement. | `go test ./...`; `go build ./...`; `make check` |
| `jpeg2000` | Opt-in JPEG 2000 / HTJ2K still-image decoding for the controlled fixture-backed profile. | Adapter stays in `examples/codec-adapters/jpeg2000`; current dependency is pure Go, with documented clinical fallback gaps. | `make codec-jpeg2000-profile-check`; app-level `-tags jpeg2000` |
| `jpeg2000_openjpeg` | Opt-in OpenJPEG-backed JPEG 2000 Part 1 fallback for external/native codec-full validation; JPEG 2000 Part 2 and HTJ2K stay on the pure-Go fallback. | Adapter stays in `examples/codec-adapters/jpeg2000`; requires the OpenJPEG `opj_decompress` executable at runtime; default builds do not register it. | `make codec-jpeg2000-openjpeg-check`; app-level `-tags jpeg2000_openjpeg` |
| `jpegls_charls` | Optional JPEG-LS Near-Lossless and extended-mode adapter backed by a dynamically loaded CharLS shared library. | Nested optional module plus explicit `jpegls_charls` build tag; the native runtime must not enter the base module. | `make codec-jpegls-charls-check`; app-level `-tags jpegls_charls` |
| `jpegxl_djxl` | Opt-in JPEG XL still-image decoding through the `djxl` runtime from `libjxl`. An optional supervised `jpegxl-helper` worker may be started by Twin when packaged; it is not part of the base module. | Nested optional module plus explicit `jpegxl_djxl` build tag; requires the `djxl` executable at runtime; must not enter the base module. Current strategy is documented in [`JPEGXL_DECODER_STRATEGY.md`](JPEGXL_DECODER_STRATEGY.md). | `make codec-jpegxl-djxl-check`; app-level `-tags jpegxl_djxl` |
| `codecfull` | Fail-closed clinical/release profile combining all qualified still-image codecs. | Nested `examples/codecfull` module; requires CharLS 2.4.2, OpenJPEG 2.5.4, OpenJPH 0.31.0, and libjxl 0.11.2 at runtime. Packaging publishes licenses and `codec-capabilities.json`; the base module remains unchanged. | `make codecfull-check`; app-level `-tags codecfull` |

The nested `jpegls` adapter accepts an injected decoder. Applications that
start with `pixeldata/builtin` use `RegisterNearLossless` so the optional
backend cannot replace the qualified pure-Go Lossless decoder. `Register` and
`RegisterDefault` remain available to standalone adapter consumers that need
both JPEG-LS transfer syntax UIDs from the injected backend.

## Verification commands

Run these from `dicom-go/`:

```sh
# Default pure-Go module checks.
make check
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go build ./...

# Policy guardrails for module manifests and required profile documentation.
make codec-policy-check

# Controlled recursive-discovery regressions; optionally select Bash 3.2.
go test ./scripts -run TestCodecPolicyRecursiveManifests -count=1
# DICOM_GO_POLICY_BASH=/path/to/bash-3.2 go test ./scripts -count=1

# Optional adapter module tests.
make codec-optional-check

# Requires the audited runtime paths documented in CODECFULL_PROFILE.md.
make codecfull-check
```

`make check` intentionally does not require native codec libraries. Optional
codec checks are explicit so CI and application builds can opt into them without
changing the default profile.

The policy scanner supports Bash 3.2 and newer, including Git Bash. It inspects
`go.mod` and `go.sum` recursively under both codec example trees, including
hidden directories and paths containing spaces, without invoking `find`.
Directory symlinks are rejected with a diagnostic to avoid cycles and omitted
subtrees. These checks inspect dependency names; they do not replace license
review of an upstream dependency.

## Twin-Viewer relationship

Twin-Viewer may enable optional codecs with its own build tags, but the reusable
adapter, decoder, encoder, and transcoder boundaries belong in `dicom-go`.
Normal `make build` and
package-script builds leave `CODEC_TAGS` empty and must remain usable without
native codec libraries. Qualified clinical or codec-full builds opt in
explicitly with `CODEC_TAGS=codecfull`; other optional profiles likewise require
documented tags and commands.
