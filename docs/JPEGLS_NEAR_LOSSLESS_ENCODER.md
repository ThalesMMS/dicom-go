# Explicit pure-Go JPEG-LS Near-Lossless encoding

`NewEncoder()` and the zero `Encoder` remain lossless `.80`. The existing
encoder now accepts an immutable `EncoderOptions` through
`NewEncoderWithOptions`: `Near=0` selects the unchanged lossless policy;
positive `Near` selects `.81` and requires `AllowLossy=true`. Nothing chooses
NEAR automatically. `RegisterEncoder` still registers only `.80`;
`RegisterNearLosslessEncoder` separately requires positive NEAR and authorization.
No fallback, package initialization, network negotiation or runtime failure
registers or selects this lossy encoder.

```go
encoders := pixeldata.NewMemoryEncoderRegistry()
err := jpegls.RegisterNearLosslessEncoder(encoders, jpegls.EncoderOptions{
    Near: 1, // example only; the application chooses its stored-sample bound
    AllowLossy: true,
})
if err != nil { return err }
derived, report, err := pixeldata.TranscodeDataSet(ctx, original,
    transfer.ExplicitVRLittleEndian, transfer.JPEGLSNearLossless,
    pixeldata.TranscodeOptions{EncoderRegistry: encoders, AllowLossy: true})
```

The frame encoder and the dataset transcoder require separate explicit
authorization because a registry can be shared across operations with different
policies. Frame encoding returns a codestream; use the transcoder for DICOM
metadata and identity. `TranscodePath` additionally needs the target decoder in
its caller-owned registry to validate the staged output before publication.
The same encoder registry can be supplied to `net/storetranscode`.

## Qualified subset and units

- Unsigned MONOCHROME1/MONOCHROME2 and RGB; RGB uses canonical sample-interleaved
  native input with PlanarConfiguration=0. Output JPEG-LS scans use ILV=0.
- BitsAllocated 8 or 16, BitsStored 2..BitsAllocated, HighBit=BitsStored-1;
  MAXVAL is the full stored precision range. Dimensions are positive 16-bit
  DICOM dimensions and native frame byte length must match exactly.
- NEAR is an integer in `1..min(255, MAXVAL/2)`. No implicit clipping of an
  invalid parameter, signed input, palette, YBR or incompatible metadata.
- Default JPEG-LS thresholds and RESET=64, no custom MAXVAL, mapping tables,
  color transforms, restart markers or ILV=1/2 encoding.

The error bound is the absolute difference between **unsigned stored samples**
before and after this encoding step. It is not a general HU, display intensity,
colorimetric or clinical-quality bound. For a linear rescale alone, the error
in rescaled units is at most `abs(slope)*NEAR`; nonlinear LUTs, windowing and
other transformations require their own analysis. Signed input is rejected
because crossing a two's-complement discontinuity can violate a signed-value
error claim. Lossless signed `.80` support remains available.

The implementation extends the existing LOCO-I encoder and shares its state,
bit writer, prediction, context adaptation and run handling. It adds prediction
error quantization and retains reconstructed samples for subsequent predictions,
following [T.87 (1998), A.4.4, A.7 and C.2.4](https://www.itu.int/rec/T-REC-T.87-199806-I/en).
No external implementation code was copied. DICOM mappings follow
[PS3.5 2026c, 8.2.3](https://dicom.nema.org/medical/dicom/current/output/chtml/part05/sect_8.2.3.html)
and [A.4.3](https://dicom.nema.org/medical/dicom/current/output/chtml/part05/sect_A.4.3.html).
The standard permits NEAR=0 under `.81`; the encoder API deliberately chooses
`.80` for zero error. Both pure-Go and optional CharLS decoders accept that
valid zero-error `.81` input.

## Identity, history and resources

The existing transcoder encapsulates each frame separately, generates offset
tables, selects `.81`, sets Lossy Image Compression to `01`, records
`ISO_14495_1` and the measured ratio, sets derived Image Type and generates a new
SOP Instance UID. It appends prior method/ratio history. Decompression does not
erase that history. A near-lossless step is recorded as lossy even when a
particular synthetic image happens to reconstruct exactly.

The encoder owns its working buffers and never changes input bytes. It uses
the existing frame size/output checks and checks cancellation before work and
during scan rows. The caller's `TranscodeLimits` bound whole-operation input,
output, elements, frames and duration. This is materialized frame encoding,
not a streaming encoder. A later-frame codec failure or cancellation returns
no partial dataset; transactional path tests prove that no partial destination
or temporary file remains and the original bytes are unchanged.

## Independent qualification and reproduction

`codecfixture.JPEGLSNearEncoderCases` supplies 108 synthetic profiles with
2..16-bit unsigned grayscale/RGB, NEAR=1/3/7 where valid and every precision's
upper bound, odd 129x33 dimensions, extremes, long/short runs, changing neighbors
and quantization boundaries. Each has two distinct frames. The optional
CharLS test decodes the **actual transcoder output** and checks all **1,839,024
samples in 216 frames** against their original source bounds.

The approved `jpegls-encode-near/` corpus stores every encoded stream, original
sample buffer and complete CharLS reconstruction with SHA256 and per-entry
provenance in the shared #902 manifest. Always-on tests compare encoder bytes
to the qualified streams, then require exact equality with the independent
reconstruction and separately check NEAR against every source sample. These
two checks do not use NEAR as tolerance between decoders. Signed inputs are
negative tests, not advertised support.

CharLS is pinned to 2.4.2, commit
`36dd3307e070d8fbc765c3ba890b7e681046fa39` (BSD-3-Clause). Native code remains
in the optional adapter module. The live gate was executed on Windows/amd64;
the base tests also run on 386. This does not qualify every image, platform or
clinical application. This feature uses the complete CharLS reconstruction as
its independent oracle.

```sh
go test ./pixeldata/jpegls ./pixeldata/codecfixture ./pixeldata -count=1
go test -race ./pixeldata/jpegls ./pixeldata/codecfixture ./pixeldata -count=1
go test ./pixeldata/jpegls -run '^$' -fuzz '^FuzzNearEncoderBound$' -fuzztime=20s -parallel=2
(cd examples/codec-adapters/jpegls && DICOM_GO_CHARLS_LIBRARY=/path/to/CharLS-2.4.2 go test -race -tags jpegls_charls ./... -count=1)
```

For candidate regeneration, set `DICOM_GO_JPEGLS_ENCODER_CANDIDATES` to a fresh
directory and run only `TestCharLSDecodesPureGoNearLosslessEncoderFullSamples`
with the native profile. It writes candidates only after exact runtime
validation and writes the manifest only after all source bounds pass. Review
hashes and provenance before replacing the approved corpus. Ordinary tests
never rewrite fixtures or install runtimes.

The CharLS adapter now normalizes ILV=0 component planes to the canonical native
sample order; ILV=2 remains supported and ILV=1 remains outside that adapter's
qualified RGB subset. Its prior rejection of valid NEAR=0 under `.81` was also
corrected and has a native regression test.
