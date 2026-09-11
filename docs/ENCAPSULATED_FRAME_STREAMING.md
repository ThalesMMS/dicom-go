# Incremental encoded frame delivery

Issue #909 builds on [shared Item assembly](ENCAPSULATED_FRAME_ASSEMBLY.md)
and [cancelable frame channels](FRAME_CHANNEL_STREAMING.md). The base profile is
pure Go. The ordinary `parser.Reader` still parses all element headers, Items,
sequences and Part 10 datasets; there is no parallel dataset parser.

## API and qualified scope

Install `encapsulated.NewStream(ctx, sink, limits)` as
`object.ReadFileOptions.EncapsulatedSink` (or `parser.ReaderOptions.EncapsulatedSink`).
`parser.EncodedFrameSink.HandleEncodedFrame` receives an **entire encoded frame**
with index, top-level image metadata and transfer syntax. An Item callback in the
lower-level `parser.EncapsulatedSink` contract is not a frame event. Applications
normally use the supplied assembler rather than implementing that interface.

JPEG Baseline `.50`, Extended `.51`, Lossless `.57/.70`, and JPEG-LS `.80/.81`
are qualified. Pixel decoding and codec profile checks remain separate. The
streaming API does not imply JPEG 2000 progressive decoding, video, JPIP, RLE,
Encapsulated Uncompressed or other transfer syntaxes. Their existing reading and
codec APIs remain available without this option.

Only a single top-level integer encapsulated Pixel Data value is qualified.
Nested encapsulated Pixel Data, native/float Pixel Data, a skip policy combined
with streaming, duplicate Pixel Data and image metadata or EOT attributes after
the streamed value are rejected. Metadata in sequence items never updates the
main image metadata. Selective readers and validation lifecycle hooks reject
this new option; ordinary readers are the qualified integration path.

```go
sink := parser.EncodedFrameSinkFunc(func(frame parser.EncodedFrame) error {
    // frame.Data is owned by this callback's receiver and contains one complete
    // JPEG/JPEG-LS codestream. Decode/process it or explicitly retain it here.
    return consume(frame)
})
stream, err := encapsulated.NewStream(ctx, sink, encapsulated.Limits{
    MaxFrameBytes: 64 << 20,
    MaxFragmentsPerFrame: 4096,
})
if err != nil { return err }
file, err := object.ReadFileWithOptions(input, object.ReadFileOptions{
    EncapsulatedSink: stream,
    MaxElementBytes: 1 << 20,
    MaxPixelDataBytes: 512 << 20,
    MaxTotalBytes: 600 << 20,
    MaxElements: 200000,
    MaxSequenceDepth: 32,
})
if err != nil { return err } // Earlier callbacks were provisional.
defer file.Close()
// file.Dataset.HasDiscardedValues() is true in this mode.
```

A runnable, metadata-free reporting example is available with
`go run ./examples/streamencapsulated input.dcm`. It processes each encoded frame
synchronously and only reports successful file completion after the final read.

## Boundaries and delivery time

The assembler reuses #908's JPEG marker scanner, Item range assembly, limits and
offset ordering checks. Length-delimited APP/COM/table/scan-header payloads are
opaque, including embedded SOI, EOI and DICOM delimiter-looking bytes. Entropy
stuffing and markers split between Items are handled by the same state machine.

The first complete codestream is delivered immediately after its final Item,
before reading the next Item header or waiting for the sequence delimiter. BOT
offsets must start at zero, increase, align with actual Item boundaries and agree
with the observed codestream boundaries. They include eight-byte Item headers.
Empty BOT uses unambiguous SOI/EOI framing, never fragment-count division.

EOT/Lengths must both be present, nonempty OV values with one entry per frame;
BOT must be empty and each frame must occupy one Item. EOT lengths equal the
codestream length through EOI, excluding any terminal zero pad. Invalid tables,
lengths, extra images in one Item, odd Items and truncated markers fail closed.
EOT tables are checked before payload allocation. The frame/fragment/byte limits
also apply before each Item buffer allocation.

Normative basis: DICOM **2026c**, [PS3.5 A.4](https://dicom.nema.org/medical/dicom/2026c/output/chtml/part05/sect_A.4.html),
[PS3.3 C.7.6.3](https://dicom.nema.org/medical/dicom/2026c/output/chtml/part03/sect_C.7.6.3.html)
and [PS3.5 A.4.3](https://dicom.nema.org/medical/dicom/2026c/output/chtml/part05/sect_A.4.3.html).

## Retention, ownership and errors

Without `DeferPixelData`, the returned Pixel Data element contains
`core.DiscardedValue`, distinct from empty and deferred values. The marker follows
an element copied into another dataset. `HasDiscardedValues` includes sequence
items; object writing rejects the dataset before emitting bytes, and the low-level
writer rejects a discarded element with `core.ErrDiscardedValue`. A caller must
explicitly replace or remove the value before serializing an altered dataset.

With **explicit** `DeferPixelData: true`, a seekable input is required. Encoded
frames are still delivered incrementally, but the complete original Pixel Data
value location is recorded and a nil deferred value remains in the object. Its
existing value provider can replay the original Items and tables without retaining
their payload. `OpenFileWithOptions` keeps its file handle alive until `File.Close`;
`ReadFileWithOptions` borrows the caller's source, which must remain open and
unchanged until replay finishes. Closed/truncated sources produce errors. Merely
passing a seekable source does **not** opt into retention.

Each delivered frame owns its buffer, including a one-Item frame. No frame buffer
is reused. The stream itself retains at most the current frame's Items and, while
joining them, one additional frame buffer. Payload memory is bounded by twice
`MaxFrameBytes`, plus bounded read/parser buffers, Item descriptors and tables.
Default limits: 100,000 frames, 100,000 aggregate fragments, 100,000 fragments per
frame, 512 MiB encoded object bytes (including BOT), 512 MiB per frame. Set tighter
limits for the application. Ordinary non-pixel metadata is retained by the object;
use parser limits for metadata/sequence/total-file budgets as shown above.
BOT/EOT metadata is O(frame count); payload retention is not.

Callbacks execute synchronously and naturally apply backpressure. There are no
worker goroutines or internal frame queues. For a queue use
`parser.NewEncodedFrameChannelSinkContext(ctx, ch)`; it shares the native channel
adapter's cancellation/finalization implementation. The caller chooses capacity
and thus its additional memory budget: capacity times maximum frame bytes, plus
the active frame. Consumer-retained buffers and codec decode memory are separate
caller budgets. Consumers cancel the context when abandoning delivery; never
close the channel themselves.

The high-level reader owns sink finalization exactly once, even on early header,
open, recovery, callback and late parser errors. Direct `Reader.Next` users must
call `Reader.Close` when stopping, after an active read returns. The stream never
closes its source. `Stream.Close` cancels its own child context, prevents further
delivery and forwards finalization once; a downstream channel
sink interrupts active sends safely. A custom callback must implement its own
cancelable behavior. Cancellation is checked between 32 KiB input chunks and
during marker scans. It cannot interrupt an arbitrary blocking `io.Reader` call;
use a transport deadline or close owned input. The example does the latter on
interrupt, with a single source-close guard.

Already delivered frames remain valid owned bytes after an error, but the file
read returns no successful object on truncation, table conflict, consumer error
or cancellation. Even a closed frame channel is **not** a successful-file signal:
always inspect the producer's final error before committing file-level results.
Recovery never retries a parse after streaming callbacks have run.

## Evidence and local validation

- `TestEncodedStreamFirstFrameBeforeRemainderAndBackpressure`: controlled pipe,
  no bytes of later frames available before first delivery, all three table modes.
- `TestEncodedStreamPayloadRetentionDoesNotGrowWithFrameCount`: constant-size
  source generator, 1,024 frames/about 64 MiB, bounded sampled live heap.
- `TestEncodedStreamFullIndependentJPEGReconstructions`: full libjpeg-turbo JPEG
  samples, existing 12-bit tolerance policy and exact JPEG Lossless RGB reference.
- `TestEncodedStreamJPEGLSIndependentFullCorpus`: all 57 lossless and 116
  near-lossless CharLS fixtures, two frames, empty/populated BOT and legal EOT,
  full reconstructed samples matched exactly. This proves reconstruction;
  it does not establish clinical quality.
- `TestStreamMarkerSeamsAndPreallocationLimits`: every even boundary of opaque
  segments/entropy/EOI, budget rejection before reads and mid-read cancellation.
- Deferred real-file replay, source closure, malformed tables, nested scope,
  canceled/abandoned delivery, consumer errors and late truncation have tests.
- Existing native channel tests remain; native nested Pixel Data stays separate,
  and recovery early failures now finalize both sink contracts consistently.

```sh
go test ./parser ./object ./pixeldata/encapsulated ./pixeldata/codecfixture -count=1
go test -race ./parser ./object ./pixeldata/encapsulated ./pixeldata/codecfixture -count=1
go test ./pixeldata/encapsulated -run '^$' -fuzz '^FuzzEncodedStreamItems$' -fuzztime=15s -parallel=2
```
