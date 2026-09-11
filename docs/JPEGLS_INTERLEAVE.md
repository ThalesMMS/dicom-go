# Pure-Go JPEG-LS interleave qualification

The builtin Lossless decoder (`1.2.840.10008.1.2.4.80`) now decodes RGB
`NEAR=0, ILV=0/1/2`. Its new ILV=1/2 profile is three unsigned components,
2-16 stored bits in 8/16 allocated bits, HighBit=BitsStored-1,
PlanarConfiguration=0, matching SOF precision and geometry, full component
coverage in frame-header order, and MAXVAL=2^precision-1. Arbitrary unique
component identifiers are accepted. The output is little-endian RGB triplets.
YBR_FULL, signed RGB, subsampling, partial/mixed interleaved scans, mapping
tables, HP color transforms, NEAR>0 and restart markers are not qualified.
Near-Lossless is now available through the separate [`.81` decoder](JPEGLS_NEAR_LOSSLESS.md).
The existing ILV=0 monochrome/palette profiles and explicitly registered ILV=0
encoder remain available.

This follows [ITU-T T.87 (1998), Annexes A, B and C](https://www.itu.int/rec/T-REC-T.87-199806-I/en).
The new scan control was written in-tree from those algorithms: shared adaptive
contexts, independent component neighbors, separate run indices for line
interleave, and one vector run with forced RItype=0 for sample interleave.
[DICOM PS3.5 2026c, 8.2.3](https://dicom.nema.org/medical/dicom/2026c/output/chtml/part05/sect_8.2.3.html)
requires PlanarConfiguration=0 for these compressed color profiles; the stream
specifies the encoded interleave. No color transform is inferred from metadata.

The review also found two preexisting scalar run defects: Rb was not recomputed
at the interruption position, and the decoder adapted Nn after restoring the
prediction sign. Both are fixed; the encoder's Rb calculation is fixed too.
The enlarged independent corpus fails before these fixes. Default ILV=0
encoder entropy now also matches CharLS byte-for-byte, across all 15 precisions.
The Golomb parameter calculation also uses a widened comparison: a valid large
LSE counter state previously overflowed a 32-bit shift and eventually panicked.
The regression is executed on Windows/386 as well as Windows/amd64.

## Independent evidence and provenance

`pixeldata/codecfixture/testdata/codecfull/jpegls/` contains 57 synthetic
codestreams and 19 complete canonical raw images. Main images are 129x33, with
different run lengths per channel, joint runs, changing interruption neighbors,
precision extrema, checker patterns and deterministic high variation. All 15
precisions (2-16) run through ILV=0/1/2; one-column and one-row images test edges.
Two additional 8/16-bit profiles exercise non-default LSE thresholds.
All 651,483 reconstructed samples are checked exactly across the 57 streams.

The explicit candidate generator is `scripts/generate-jpegls-interleave.py v1`.
It calls the CharLS 2.4.2 C API directly, encodes and independently decodes every
stream, and verifies every source byte before writing the candidate record.
The main #902 corpus manifest pins input/output hashes, layouts, license and
provenance. `jpegls/generation.json` records the actual runtime binary SHA256.
Default Go tests read committed data and need neither CharLS nor Python.
Additional comparison evidence and source-inspection notes live in the
[separate validation workspace](../../dicom-go-validation/docs/REFERENCE_NOTES.md).
The fixture generator uses [CharLS 2.4.2, 36dd330](https://github.com/team-charls/charls/tree/36dd3307e070d8fbc765c3ba890b7e681046fa39), BSD-3-Clause.

Generated images are project-authored synthetic data under the repository MIT
license, with no patient data. The external libraries remain tools/reference
dependencies; their source is not incorporated into the decoder.

LSE ID=1 zero fields select standard defaults. Threshold ordering and RESET's
standard upper bound are checked. Independent custom-threshold qualification
uses RESET=64 and full precision MAXVAL. During oracle preparation, CharLS 2.4.2
failed to decode its own RGB8 ILV=2 stream with thresholds 4/9/28 and RESET=31.
Its optimized 16-bit implementation also did not implement the normative
MAXVAL=40000 range in the tested profile. Those candidates were not approved;
no claim for arbitrary custom RESET values follows from this corpus. ILV=1/2
custom MAXVAL is explicitly rejected rather than decoded with a guessed range.

## Validation and reproduction

```sh
python scripts/generate-jpegls-interleave.py --charls /path/to/CharLS-2.4.2-library --output /tmp/candidates
go test ./pixeldata/jpegls ./pixeldata/codecfixture -count=1
go test -race ./pixeldata/jpegls ./pixeldata/codecfixture -count=1
go test ./pixeldata/jpegls -run '^$' -fuzz '^FuzzDecodeInterleave$' -fuzztime=15s -parallel=2
make check
```

Regeneration creates candidates; review hashes before replacing approved files.
Negative tests reject component duplication/order/coverage errors, metadata
conflicts, unsupported transforms/parameters and every byte truncation of the
main 8-bit interleaved streams, including shortened entropy with a forged EOI.
Public decoding retains bounded frame assembly and the 512 MiB working-set
checks, honors cancellation, and publishes no partial frame list on failure.
Concurrent use of the stateless codec is covered by race tests.
