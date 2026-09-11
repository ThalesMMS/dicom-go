# Explicit C-STORE representation preparation

`net/storetranscode.NewSource` composes an existing `dimse.StoreSource` with the
existing `pixeldata.TranscodeDataSet` pipeline. It does not add codecs to DIMSE,
change `StoreSession` negotiation defaults, or implement a second pixel planner.
A zero target delegates unchanged to the base source. Selecting a target is an
explicit request to prepare that representation, not permission to guess a
transfer syntax or to apply loss on codec failure.

```go
source, err := storetranscode.NewSource(dimse.NewPathStoreSource(path), storetranscode.Options{
    Target: transfer.RLELossless,
    Transcode: pixeldata.TranscodeOptions{
        DecoderRegistry: decoders, // caller-owned, needed for compressed input
        EncoderRegistry: encoders, // caller-owned, e.g. rle.RegisterEncoder
    },
    MaxSpoolBytes: 512 << 20,
})
if err != nil { return err }
result, err := session.Store(ctx, source)
source.RecordResult(result)
report := source.Report()
```

No package-global encoder registry is used. `Options.Dictionary` optionally
resolves implicit VR and caller-owned private catalogs during source preparation.

## Preparation, offers and ownership

Construction is lazy. The first `Inspect` prepares the representation completely
before the batch planner can advertise it. It opens the existing source, writes
its original dataset bytes to a bounded private temporary file, checks source
descriptors, hashes the bytes, and reads the dataset through the ordinary parser.
SOP Class/Instance identity and pixel representation are checked through the
existing object-backed StoreSource descriptor logic. A requested transformation
then runs through the existing transcoder and serializer, including metadata,
codec/profile validation and actual encoder execution. Thus a registration alone
does not produce a false offer when its runtime or input profile fails.

Inspect removes all temporary files before returning and caches only detached
descriptors, a SHA-256 digest and any derived identity. It keeps no open input
handle or batch of pixel buffers. Subsequent inspection returns that plan.
`Open` reopens the original, checks its descriptors and complete byte digest, and
prepares the representation again before the C-STORE command. A changed source,
runtime, producible syntax set, output identity or encoded size fails the open.
Create a new adapter to plan a deliberately changed source. A conservative size
check can reject a nondeterministic encoder whose output length changes between
preflight and open; such output is not silently substituted after negotiation.

One target is selected per source; heterogeneous batches can select different
targets. `FallbackOriginal` additionally permits the original syntax and exact
original dataset bytes, after the requested target in offer order. A target that
cannot be produced is excluded with a reason; original fallback requires this
explicit option. Resource exhaustion and cancellation abort preparation.
Equivalent-syntax pass-through requires no pixel codec and preserves the encoded
bytes. Native conversion, decode, encode and decode/encode are distinct report
operations. No codec is invoked while the dataset is being sent on the network;
the writer only copies a previously verified temporary representation.

Every opened handle owns an independent private temporary directory and closes
it once, including send failure or cancellation. The adapter closes handles it
opens from the base source, according to the base source's existing ownership
contract. Borrowed input objects are not closed or modified. Standalone callers
of `Open` must call the returned `Close`; StoreSession already does so. Source
methods permit concurrent independent opens when the base source and injected
registries support concurrent use. Reports are detached, synchronized snapshots.

## Limits and cancellation

`MaxSpoolBytes` bounds aggregate input plus output temporary dataset bytes for
one preparation/handle (default 2 GiB). Files use `0600` within a newly created
private directory. Input and output limits also apply separately. The adapter
uses `pixeldata.ResolveTranscodeLimits` to reuse the transcoder's finite defaults
and validation; negative limits are rejected. Its duration covers preparation,
including source copy, hash, parse and transcode. Cancellation is checked during
file reads/writes and copies; codecs retain their existing cooperative context
contract. No detached worker queue is introduced.

Actual transformation materializes the input pixels because that is the current
transcoder contract. Memory therefore includes the bounded input and transcoder
working buffers, in addition to the temporary-file and network budgets. This API
does not claim streaming pixel conversion or a memory cap equal to the compressed
output size. Set `Transcode.Limits` for the application's memory envelope.
Opaque pass-through can defer Pixel Data. `StoreSession.MaxInFlightBytes` reserves
the largest offered representation size for each admitted operation; preparation
may additionally hold one source's working buffers. An explicitly lossy target
is never substituted in response to lossless codec failure.

## Lossy identity and outcomes

Lossy encoding requires `Transcode.AllowLossy`. The existing transcoder creates
a new SOP Instance UID and updates lossy history/derived-image metadata. The
adapter retains the first verified new identity across inspection, open and any
explicit retries so the command and transmitted dataset agree. It does not reuse
the original instance identity. `AllowLossy` and `FallbackOriginal` are mutually
exclusive because these alternatives can have different SOP Instance identities.

The original file/object remains unchanged. A new adapter for the same original
can create a new derived instance; keep the same adapter when intentional retry
must retain its planned identity. Delivery/retry remains governed by StoreSession.
No valid terminal response after an invoked C-STORE means an unknown outcome;
a valid refused/failure response means confirmed failure. The pipeline now applies
the same C-STORE status check as the serial client, aborts an unusable association,
and respects `ContinueOnError` before considering remaining sources. It does not
classify remote failure statuses as success or treat transport loss as confirmed
failure. These semantics follow the confirmed service in
[PS3.7 2026c Section 9.1.1](https://dicom.nema.org/medical/dicom/2026c/output/chtml/part07/chapter_9.html).

## Diagnostics and CLI

`Report` records original syntax, candidate operation, producibility, exclusion
reason, unavailable runtime/registry and lossy transformation status. The selected
syntax is recorded when the writer is called; it is not proof of delivery.
`RecordResult` optionally attaches the existing StoreSession outcome and
negotiated syntax, including rejection, canceled, unknown and not-sent results.
Reasons are stable categories such as `encoder-unavailable`, `decoder-unavailable`,
`runtime-unavailable`, `profile-unsupported`, `lossy-disallowed`, `resource-limit`
and `source-changed`. Generic backend failures remain `preparation-failed`;
typed causes remain accessible with `errors.Is`. Default error/report text omits
instance UIDs, origins, paths, patient data and backend-controlled text.

The CLI builds caller-owned builtin decoder registries and registers the existing
pure-Go RLE/JPEG-LS encoders only when `-transcode-to` is selected. JPEG Baseline
encoding additionally requires `-allow-lossy`; quality defaults explicitly to 90
and can be set with `-jpeg-quality`. Other requested syntaxes are offered only
if the selected implementation can actually produce them.

```sh
go run ./cmd/storescu -transcode-to 1.2.840.10008.1.2.5 -called HOROS 192.168.15.3:4007 synthetic.dcm
go run ./cmd/storescu -transcode-to 1.2.840.10008.1.2.4.80 -transcode-fallback-original -max-invoked 4 127.0.0.1:104 synthetic.dcm
go run ./cmd/storescu -transcode-to 1.2.840.10008.1.2.4.50 -allow-lossy -jpeg-quality 95 127.0.0.1:104 synthetic.dcm
```

`-transcode-spool-dir` chooses the temporary parent;
`-transcode-max-spool-bytes` sets its per-handle aggregate limit. Default CLI output
is unchanged without transcode selection. When selected, reports correlate only
by source ordinal and standard syntax UID. These are trusted-network tools;
transport/authentication/deployment protections remain caller responsibilities.

## Local qualification

Always-on tests cover exact lossless metadata/pixel roundtrip, original byte
preservation, missing codecs, unsupported profiles, runtime/source changes,
explicit original fallback, bounded spool writes (including a source ignoring a
write error), cancellation, close errors, independent concurrent handles, and
stable authorized lossy identity. Real local async tests hold two requests
outstanding and test success, refusal, cancel and transport abort without retrying
uncertain operations. The existing serial/planner/pipeline tests also run.

Additional mixed-batch validation and full-sample comparison evidence lives in
the [separate validation workspace](../../dicom-go-validation/docs/REFERENCE_NOTES.md).
The local async test separately proves simultaneous outstanding requests.

The optional pinned pynetdicom/pydicom/Pillow gate receives JPEG Baseline,
checks complete metadata and command/derived identity, and independently decodes
65,536 samples with libjpeg. The quality-95 synthetic ramp has explicit maximum
error, PSNR and local mean-error bounds; this does not qualify arbitrary lossy
clinical use. The profile is isolated from normal dependencies and never installed
by `make check`:

```sh
python -m pip install -r scripts/requirements-store-transcode-interop.txt
DICOMGO_PYNETDICOM_INTEGRATION=1 DICOMGO_PYTHON=/path/to/python go test -race ./net/storetranscode -count=1
```
