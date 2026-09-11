# Full-sample pixel corpus

The existing `pixeldata/codecfixture/testdata/codecfull/manifest.json` is the
fixture inventory. `Case` remains the synthetic fixture API; `codecprofile`
remains the codec/runtime authority. The
[separate validation inventory](../../dicom-go-validation/docs/CAPABILITY_AUDIT.md)
projects this same manifest without decoded samples. Inventory presence is
structural evidence; an execution report records which exact tests ran.

Entries record input Transfer Syntax, dimensions, components, allocated/stored
bits, HighBit, signedness, photometry, planar configuration, frames, expected
outcome, generator, source, license and no-PHI declaration. Files have full SHA256
hashes. Synthetic `caseName` entries hash deterministic Part 10 serialization and
all expected frame bytes; metadata, outcome and policy are checked against
`CorpusCases`. Regeneration is explicit; no codec or clinical qualification is
inferred merely from an entry's presence.

The [JPEG-LS interleave extension](JPEGLS_INTERLEAVE.md) adds 57 CharLS 2.4.2
streams covering every precision from 2 to 16 bits, ILV=0/1/2, distinct-channel
runs, odd geometry, edges and LSE thresholds. All 651,483 samples are checked
through the public builtin registry against the complete CharLS reconstruction.

The [Near-Lossless extension](JPEGLS_NEAR_LOSSLESS.md) adds 116 synthetic
CharLS streams, complete decoder oracles, and separate original source hashes
with explicit NEAR bounds, including four NEAR=127 default-threshold cases.

## Comparison contract

`CompareSamples` visits every sample in every frame. Canonical order is frame,
row, column, component; coordinates are zero-based. It normalizes declared byte
order and planar storage, extracts BitsStored using HighBit, and sign-extends
before comparing. It never guesses a decoder's layout, converts color spaces,
rescales intensities or rounds floating pixels. `Case.DecodedLayout` and
`Case.ReferenceLayout` explicitly describe storage differing from input metadata.
Frames are never discarded or reordered.

Packed one-bit storage ignores unused bits after the final sample. Odd native
frame lengths may carry one zero byte for even-length padding. Geometry,
precision, signedness, missing frames, nonzero padding and other lengths are
rejected. Sample arithmetic supports 8/12/16/32-bit unsigned/signed integers
without byte-wise tolerance or 32-bit subtraction overflow.

Reports include the first changed sample, all changed samples (`mismatches`),
samples outside the maximum error (`outside_limit`), and per-component maximum,
MAE, RMSE and PSNR. Infinite exact PSNR is represented as omitted/null, not invalid
JSON infinity. Spatial error is the worst 8x8 tile mean independently per frame
and component; edge tiles use their actual size. Working memory is constant.
Traversal order cannot change the first reported raster coordinate.

Lossless and same-codestream JPEG-LS reconstruction require exact samples. Lossy
qualification requires maximum sample error, minimum PSNR per component and
maximum tile mean, with a codec-specific rationale. Legacy `Tolerance` remains
compatible but is now in sample units; alone it does not qualify a lossy result.
`Result.Comparison.Qualified` makes this explicit.

The manifest holds codecfull policies. JPEG Extended 12-bit shares the complete
libjpeg-turbo 3.1.0 reconstruction with decoder conformance tests. JPEG XL's
synthetic discontinuous color pattern has larger blue/chroma errors: libjxl
0.11.2 measured blue RMSE 11.322 for distance-1 coding and 13.434 across JPEG pixel
decoders; worst blue tile means were 19.094 and 17.032. The documented regression
limits are max-abs 80, PSNR >=25 dB per component, and tile mean <=20. Restoring the
original JPEG bitstream remains separately exact. These are fixture-specific
bounds, not clinical image-quality thresholds or generic decoder tolerances.

## Independent reconstruction and boundaries

- JPEG-LS NEAR=2, 8/16 bits: full approved reconstruction from the direct C API of
  CharLS 2.4.2, commit `36dd3307e070d8fbc765c3ba890b7e681046fa39`.
  NEAR bounds encoder/source error, never differences between decoders.
  The CharLS adapter is also checked against the full references.
- RLE RGB16, two frames: 60,000 exact samples from pydicom 3.0.1's explicitly
  selected **pure Python RLE** backend. Existing signed/multiframe RLE and
  lossless JPEG-LS native/reference pairs remain full-frame baseline tests.
- pydicom only reads the JPEG-LS container during regeneration. It never
  selects a JPEG-LS plugin implicitly. Two wrappers around CharLS do not count as
  independent implementations. Additional comparison evidence is maintained in
  the [separate validation workspace](../../dicom-go-validation/docs/CAPABILITY_AUDIT.md).
- Synthetic cases cover native 8/12/16/32 bits, both signs, odd dimensions,
  extrema, runs and row boundaries over two frames. RLE covers 8/12/16 and rejects
  32. Native RGB/YBR_FULL exercise planar/interleaved layouts and padding; RLE
  rejects unsupported YBR and planar inputs. Existing frame-assembly tests cover
  encapsulated-uncompressed fragmentation. Common multi-codec assembly remains
  a separate capability expansion.
- Historical point-only HTJ2K/public JPEG lossless cases remain explicitly marked
  with `qualificationLimit`; decode success is not full reconstruction evidence.
  Those families also retain full synthetic cases. Missing base adapters have
  enumerated `baselineSkip` reasons and asserted typed unavailability. Unexpected
  failures in supported cases fail the test.

## Local execution and regeneration

From `dicom-go`:

```sh
make codec-conformance-check       # offline hashes, metadata, samples, rejections
make codec-jpegls-charls-check      # explicit CharLS runtime gate
make codecfull-check               # all pinned native runtimes required
```

Use a writable local output path on Windows. Runtime acquisition and environment
variables are in [CODECFULL_PROFILE.md](CODECFULL_PROFILE.md). Requested native
profiles without a mandatory runtime fail; absence never becomes a passing skip.

From `pixeldata/codecfixture/testdata/codecfull`, run `go run generate.go` to update
synthetic entries. Review metadata, hashes and policies before committing. This
command never replaces independent goldens.

For independent candidates, install pydicom 3.0.2 and numpy 2.1.3 in an isolated Python
environment and run:

```sh
python scripts/regenerate-pixel-oracles.py --charls /path/to/CharLS-2.4.2-library --output /tmp/pixel-candidates
```

The command never downloads or silently approves data. It checks input hashes,
backend versions, output sizes and little-endian host storage, then writes
candidates and the runtime binary hash. Compare candidates with approved `.raw`
files and run the documented validation gates before updating reference hashes.

The original oracle provenance retains its actual pydicom 3.0.1 version.
Regeneration now requires 3.0.2, which fixes
[CVE-2026-32711](https://github.com/advisories/GHSA-v856-2rf8-9f28).
All three approved raw outputs were reproduced byte-for-byte with the upgraded
interpreter; [the separate revalidation record](../pixeldata/codecfixture/testdata/codecfull/pydicom/oracle-revalidation-pydicom-3.0.2.json)
records that runtime without rewriting the original generation history.
`pydicom/oracle-generation.json` records the approved run. Default tests need
neither Python nor native codec installations.
