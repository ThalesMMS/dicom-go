# Shared encapsulated JPEG frame assembly

`pixeldata/encapsulated` resolves already parsed DICOM Items into frames. JPEG
Baseline, Extended (8/12 bit), Lossless Process 14/SV1, builtin JPEG-LS `.80`/`.81`
and the optional JPEG-LS adapter consume this same implementation. Image and
scan parameter validation remains with each codec. Encoders are unchanged.

Normative baseline: DICOM PS3.5 **2026c**, [A.4 and A.4.1](https://dicom.nema.org/medical/dicom/2026c/output/chtml/part05/sect_A.4.html),
[A.4.3](https://dicom.nema.org/medical/dicom/2026c/output/chtml/part05/sect_A.4.3.html),
and PS3.3 [C.7.6.3](https://dicom.nema.org/medical/dicom/2026c/output/chtml/part03/sect_C.7.6.3.html).
The existing qualified JPEG-LS offset validation was extracted and strengthened;
no external parser or codec implementation was copied.

## Frame bounds

- BOT entries are little-endian uint32 offsets to Item **Tags**, relative to
  the first Item after the BOT. The count must equal NumberOfFrames; the first
  offset is zero, subsequent entries increase strictly, and all entries align
  to actual Items. Eight-byte Item headers and final padding count in offsets.
- EOT and Lengths must both be present, nonempty OV attributes with one entry
  per frame. BOT must be empty and each frame must occupy exactly one Item.
  Lengths exclude the optional single zero pad, whose parity/value are checked.
  EOT does not enable multiple-Item frames.
- With empty tables, one Item per declared frame is unambiguous at the Item
  level. Otherwise a bounded marker scanner finds complete codestreams. It
  skips APP/COM/table/scan-header payloads by length, handles JPEG byte stuffing
  and JPEG-LS bit stuffing, and carries marker state across Item boundaries.
  EOI must terminate an Item, optionally followed by one zero pad. A fragment
  cannot contain data from two frames. No division by expected frame count or
  search for DICOM Sequence Delimitation bytes inside payloads is performed.
- Offset tables establish Item ranges; they do not prove that the enclosed
  codec stream is valid. `ValidateFrame` lets consumers check structural
  SOI/EOI and trailing data separately. The Go standard JPEG wrapper uses it
  because `image/jpeg` otherwise ignores extra images after EOI; it also checks
  decoded header dimensions before allocating the image.

`JPEG` and `JPEGLS` are the only accepted formats. RLE, Encapsulated Uncompressed,
JPEG 2000, JPEG XL and video retain their existing syntax-specific paths. In
particular, RLE's restrictions and the Encapsulated Uncompressed padding/layout
regressions remain independent of JPEG rules.

## Memory, ownership and deferred input

`FromFragments(ctx, sequence, object, frameCount, format, limits)` plans an
existing `core.FragmentSequence`. `ReadTables` and `New` accept a caller-provided
`Source`; `ReaderAt` adapts already indexed Item Value ranges. The existing
parser can supply those ranges through deferred Item tokens (`Offset+8` and
`Header.Length`); no second dataset parser is introduced.

Default limits are 100,000 frames, 100,000 total Items, 100,000 Items per frame,
512 MiB total encoded payload and 512 MiB per compressed frame. Callers may set
smaller or explicit larger limits. Invalid counts, overflow and byte budgets
are checked before allocating frame buffers. Zero limit fields select defaults.
The Plan retains O(Items+Frames) index data and a 32 KiB scratch buffer while
inferring boundaries. It never concatenates the whole Pixel Data.

`Plan.Frame` returns a `View`. A single in-memory Item is a read-only borrowed
slice with capacity restricted to its length. Multiple Items or deferred reads
produce one caller-owned buffer, never recycled on the next read. EOT may trim
the returned view to omit padding. In-memory final codestreams may omit their
unencoded odd-length pad; an odd non-final fragment is rejected. `ReaderAt`
ranges always describe even on-wire lengths.

Source bytes, Item ranges and metadata must remain unchanged and alive while
the Plan or borrowed views are used. The Plan never closes the source. Safe
concurrent reads require an immutable, concurrency-safe source. Context checks
bound scanning/copy work; arbitrary blocking `io.ReaderAt` operations cannot be
interrupted by this API. Read errors and cancellation return no partial frame.
The caller owns any previously returned frames and their retention budget.

Codec output/working-set limits are separate from encoded-input limits. The
JPEG wrappers and CharLS adapter account for retained output plus compressed
input and a joined frame; builtin JPEG-LS retains its existing independent
decoded working-set bound. Reading one compressed frame at a time avoids
retaining joined buffers for all frames alongside decoded output.

## Evidence and reproduction

`TestSharedJPEGAssemblyIndependentFullSamples` splits each of five existing
codec cases at **every even byte position**, with empty/populated BOT and two
frames. Every output sample also equals the same local decoder's unfragmented
output exactly. The independent references are:

- Two small 8-bit grayscale Baseline/Process 2 streams: complete exact
  libjpeg-turbo 3.1.4.1 reconstruction, produced through Pillow 12.3.0; input,
  raw output and native binary hashes are recorded in
  `pixeldata/codecfixture/testdata/codecfull/jpeg-assembly/generation.json`.
  These two-pixel fixtures qualify assembly, not a broader DCT image corpus.
- The existing 8x8 Process 4 12-bit fixture: every sample uses its preexisting
  libjpeg-turbo reference and fixed IDCT policy (max 8, PSNR 54, tile mean 4).
  That rounding policy is not changed or applied to the lossless fixtures.
- The existing 4x3 RGB Lossless fixture: all 36 samples match libjpeg-turbo
  exactly under both Process 14 and SV1 UIDs.

`TestSharedJPEGLSMultiframeOrderAgainstIndependentReference` refragments ten
distinct MR frames and checks every byte against the existing full uncompressed
reference, with and without BOT. The 57 lossless and 116 Near-Lossless CharLS
corpus streams also continue through the shared builtin assembly path.

Offset regressions were moved from JPEG-LS into `encapsulated/offsets_test.go`.
Additional tests cover marker/length/EOI seams, opaque delimiter-looking bytes,
invalid EOT/BOT/VR/padding, actual parser-derived deferred ranges, file truncation
and closure, ownership, concurrent reads, pre-read budgets and cancellation.

```sh
go test ./pixeldata/... -count=1
go test -race ./pixeldata/encapsulated ./pixeldata/codecfixture ./pixeldata/jpegls -count=1
go test ./pixeldata/encapsulated -run '^$' -fuzz '^FuzzPlanMarkerAndOffsetBoundaries$' -fuzztime=15s -parallel=2
go run scripts/generate-jpeg-assembly-inputs.go -output /tmp/jpeg-oracles
python scripts/generate-jpeg-assembly-oracles.py --directory /tmp/jpeg-oracles
make check
```

Generators write candidates only. Default tests require no native runtime;
optional CharLS checks require its explicitly configured library.
