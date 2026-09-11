# Pixel Transfer Syntax Transcoding

`pixeldata` provides an explicit, bounded still-image transcoding pipeline. It
does not register encoders globally and it never treats decoder availability as
evidence that an encoder exists.

## Registries and ownership

- `MemoryEncoderRegistry` is separate from the legacy decoder `Registry`.
  Registration freezes capabilities, rejects duplicate Transfer Syntax UIDs,
  and has no package-level default.
- `TranscodeOptions.DecoderRegistry` is required when the source is a compressed
  still-image syntax. `TranscodePath` also requires the target decoder so every
  newly encoded lossless or lossy output is decoded before publication.
  `EncoderRegistry` is required when the target is encoded.
- Input `object.File` and `object.Object` values are borrowed and are never
  mutated. Returned values are detached from source buffers and deferred value
  providers. An unavailable deferred value fails explicitly.
- `TranscodeCoreDataSet` is the `core.DataSet` adapter. Because duplicate Data
  Elements have ambiguous last-wins semantics in `object.Object`, it rejects
  duplicates explicitly instead of silently collapsing them.
- `TranscodePath` owns every opened handle and temporary file. It writes a
  private `0600` file in the destination directory, syncs and reads it back
  through the same open handle under finite limits, verifies the output syntax
  and pixel representation, revalidates source and destination identities, and
  only then renames that same temporary entry atomically.
- Linux and macOS use filesystem compare-and-swap primitives when replacing an
  existing destination. Windows also supports replacement: `TranscodePath`
  revalidates the destination snapshot immediately before publication, then
  atomically renames the validated temporary handle with replace-existing
  semantics and verifies the published destination identity. Publication has
  an old-or-new contract: a failure before the atomic rename leaves the previous
  destination authoritative; after the rename succeeds, the new destination
  remains authoritative even if a later verification, close, or durability
  step reports an error, and the operation does not roll it back. Other
  platforms without an equivalent primitive expose the in-memory APIs but
  return `ErrTranscodeUnsupported` from `TranscodePath`.

## Filesystem traversal and restricted Unix environments

`TranscodePath` accepts explicit paths outside the current working directory;
it is not an API that confines operations to a caller-selected subtree.
It resolves existing parent-directory aliases with `EvalSymlinks` once, then
opens the resulting canonical path component by component from a descriptor
for `/`, using `O_NOFOLLOW` throughout. Final source/destination symlinks are
rejected. A symlink introduced into a canonical ancestor is also rejected.
The caller must keep the canonical directory namespace stable during the
operation and use trusted directories. Precommit reopens and compares source
and parent identities; publication and cleanup remain anchored to the opened
parent descriptor. This does not promise a pathname-level transaction against
an adversary renaming ancestors after the last revalidation.

Issue #892 reproduced a concrete Linux restriction: a chroot root directory
with mode `0711`, a child running as uid/gid `65534`, and a readable/writable
`/allowed` directory. Ordinary file/cwd opens and writes worked, while opening
`/` with `O_RDONLY|O_DIRECTORY` returned `EACCES`. The original safe traversal
therefore refused the transaction before publication, preserving the existing
destination and leaving no temporary file.

On Linux, an `EACCES` from that initial root open now retries the **same `/`
anchor** with `O_PATH|O_DIRECTORY|O_CLOEXEC|O_NOFOLLOW`. An `O_PATH` descriptor
can serve as `openat`'s directory anchor without directory read permission;
subsequent traversal still enforces search permission. See the Linux
[open(2) specification](https://man7.org/linux/man-pages/man2/open.2.html).
The retry is limited to `EACCES`; it does not retry `EPERM`, resource failures,
or operations relative to `.`. Final directory handles remain readable for
publication and `fsync`; their errors are not bypassed. Other Unix platforms
retain the original root-opening contract.

If a sandbox still rejects traversal (including `O_PATH` or ancestor/final
directory reads), provision the directory permissions/operations required by
this contract, or use `TranscodeFile` with caller-owned I/O and publication.
Do not treat lexical path-prefix checks as an equivalent replacement.

The local qualification on 2026-09-07 ran the static Go 1.26.4 transcode suite
on Debian WSL Linux/amd64 (kernel 5.15.167.4), including the real unprivileged
chroot, successful output readback, final/ancestor symlinks, directory/ancestor
replacement, failed-conversion preservation and cleanup. The Windows/amd64
module baseline is checked separately; no macOS sandbox execution is claimed.
The restricted test is opt-in and must run only in a disposable Linux test
environment with chroot privileges:

```sh
CGO_ENABLED=0 go test -c ./pixeldata -o /tmp/pixeldata-sandbox.test
sudo env DICOM_GO_TEST_CHROOT=1 /tmp/pixeldata-sandbox.test \
  -test.run='Transcode' -test.v
```

## Supported conversions

- Native Implicit/Explicit VR Little Endian, Explicit VR Big Endian, and
  Deflated Explicit VR Little Endian can be converted among one another without
  changing pixel sample values.
- Encapsulated Uncompressed can be decoded to native and produced with one
  fragment per frame.
- RLE Lossless can be decoded through an explicit decoder registry and encoded
  through the pure-Go `pixeldata/rle.Encoder`. The encoder accepts 8- or 16-bit
  allocated samples (including 12 stored bits), signed or unsigned monochrome,
  and unsigned palette/interleaved RGB, including multiple frames.
- An equivalent source and target Transfer Syntax uses a detached
  byte-preserving Pixel Data fast path unless `ForceReencode` is selected.
- Other encoder families are extension points. Missing adapters return typed
  availability errors. Video, JPIP, Deflated Image Frame Compression, Float
  Pixel Data and Double Float Pixel Data are never silently treated as ordinary
  integer still-image frames.

RLE emits the PS3.5 64-byte header, byte planes from most significant to least
significant, PackBits reset for each row, and one fragment per frame. Basic
Offset Table entries point to Item tags and include prior item headers plus even
padding. The pipeline removes stale Extended Offset Table fields whenever the
representation changes and emits EOT/EOT Lengths if frame offsets do not fit in
32 bits.

## Lossy policy

Lossy encoders require `AllowLossy`. A successful lossy conversion appends the
compression ratio and method history, sets Lossy Image Compression to `01`,
marks Image Type as derived, and assigns a new SOP Instance UID. Encoder output
metadata changes are applied only when every frame reports the same validated
Photometric Interpretation and Planar Configuration. A real transform must
also be declared in `EncoderCapabilities.OutputPhotometricInterpretations` or
`OutputPlanarConfigurations`; otherwise the encoder output is rejected.

The default module ships no lossy encoder. Optional adapters must remain
explicit, publish directional manifest evidence, and satisfy the same limits,
cancellation, metadata, and transactional contracts.

## Limits and cancellation

Zero-valued `TranscodeOptions` use finite defaults for frames, pixels, input and
output bytes, fragments, structural nodes (elements, string components and
sequence items), and nesting depth. `DecompressDataSetContext`
and `DecompressFileContext` add finite frame/pixel/native/input/expansion limits
while preserving the legacy wrappers. Internal codecs may implement the
additive `ContextCodec` interface; legacy codecs are checked before and after
their call.

Errors and reports contain fixed stages and field names, not paths, patient
values, pixel bytes, or backend-controlled panic text.

## Verification

```sh
go test ./pixeldata ./pixeldata/rle -count=1
go test -race ./pixeldata ./pixeldata/rle -count=1
go test ./pixeldata/rle -bench Encoder -benchmem
```

The encoder benchmarks report elapsed time, bytes and allocations per
operation, and a sampled peak heap delta. The first recorded qualification run
is in [`PIXEL_TRANSCODING_BENCHMARKS.md`](PIXEL_TRANSCODING_BENCHMARKS.md).

The optional pydicom reader gate is documented in
[`INTEROP_MATRIX.md`](INTEROP_MATRIX.md). The capability manifest distinguishes
`decode` and `encode`; legacy v2 JSON without a direction remains decode-only.
