# C-STORE batch sessions

`net/dimse.StoreSession` plans and sends heterogeneous C-STORE batches without
materializing every file or retaining one file descriptor per input.

## Sources and ownership

- `NewPathStoreSource` inspects Part 10 metadata with `index`, closes the
  inspection handle, and reopens one file only when its turn is sent. The data
  set byte range is copied verbatim in the original Transfer Syntax, including
  compressed, video, Big Endian, and deflated representations. A native
  Explicit/Implicit Little Endian fallback is re-encoded only when negotiated.
  The final path component may not be a symbolic link, and each attempt opens
  the physical path with no-follow semantics before revalidating metadata.
- `NewDescribedPathStoreSource` accepts metadata already persisted by an archive
  but still reopens and revalidates the file before the C-STORE command.
- `NewRootedPathStoreSource` opens the absolute scanner root and every relative
  component with no-follow semantics. The CLI pins the physical root resolved
  during the scan, preserving its symlink policy across the scan-to-send
  interval instead of trusting a later path lookup.
- `NewFileStoreSource` and `NewDataSetStoreSource` borrow their inputs. The
  caller must keep them immutable and keep deferred providers open until the
  operation returns. The session never closes borrowed inputs.
- `NewLazyStoreSource` owns each file returned by its opener and closes it once
  on every path, including cancellation and remote failure.

Origins can contain sensitive filesystem data. They are returned only as an
explicit `StoreDescriptor.Origin`; errors and progress events use `SourceIndex`
and never include the origin, Patient Name, Patient ID, SOP Instance UID, or a
remote Error Comment.

## Planning and Transfer Syntax safety

`PlanStoreBatch` runs before the first network dial. It preserves input order,
deduplicates identical `(SOP Class, ordered writable Transfer Syntax list)`
contexts, assigns odd IDs `1..255`, and starts a new association before a 129th
unique context. `net/ul` independently rejects any direct attempt to propose
more than 128 contexts or duplicate IDs.

Path and in-memory sources advertise the original Transfer Syntax plus only the
native Explicit/Implicit Little Endian fallbacks that `object.WriteDataSet` can
really produce. The caller can order those native choices; the Twin node
preference is preserved. Compressed inputs never gain a native fallback
implicitly. A value-free Pixel Data header captured from both indexed and
in-memory sources is checked against the declared Transfer Syntax before
dialing, and reopened writable capabilities must match the plan.

There is no implicit pixel transcode. The optional
[`net/storetranscode` source](CSTORE_TRANSCODE_SOURCE.md) explicitly prepares a
requested representation through the existing transcoder before advertising it.
It preserves original-byte pass-through, validates runtime/profile/identity,
revalidates the reopened source and owns bounded temporary files until close.
Lossy output requires explicit authorization and a stable derived identity.

## Lifecycle, retry, and results

Each association carries one active DIMSE operation at a time by default.
`StoreSessionOptions.MaxInvokedOperations` greater than one opts into
pipelining through `AsyncSession` and `StartCStoreEncoded`, limited to the
effective negotiated invoked window and `MaxInFlightBytes`. Peers that omit
sub-item `0x53` or accept window 1 keep the serial `StoreClient` loop; the
session never switches from pipeline to serial mid-association. Path payloads
are still written without materializing `*object.Object`. Successful and
warning responses keep the association reusable; after the final confirmed
response the session performs A-RELEASE. Cancellation of an in-flight item
uses A-ABORT: in-flight work is `StoreOutcomeUnknown`, and items that were
never invoked are `StoreOutcomeCanceled`. A transport/protocol error after a
command may have reached the peer yields `StoreOutcomeUnknown`, uses A-ABORT,
and does not repeat that item by default. With `ContinueOnError`, later items
can continue on a fresh association.

Dial retry is bounded by `MaxAssociationAttempts`. Ambiguous item retry is
disabled unless `RetryUncertain` is set; enabling it gives at-least-once
semantics and may create a duplicate stored instance. All item results remain in
input order and distinguish success, warning, remote/local failure, unknown,
canceled, and not-sent states.

Planning limits are finite by default. Item and batch byte limits are also
enforced against bytes actually written, including sources whose size was only
a hint. `MaxInFlightBytes` further caps payload bytes admitted at once. The serial
baseline admits one payload; the opt-in pipeline admits up to the effective
invoked window without exceeding this budget. Progress callbacks run
synchronously after the payload handle is closed; callback panic/error is
redacted and stops new work.

## CLI

`cmd/storescu` accepts multiple files and directories, walks directories in
deterministic order without following symlinks, applies finite file, directory,
depth, and path-length limits, and delegates all UL/DIMSE work to
`StoreSession`:

```sh
go run ./cmd/storescu -- -called ORTHANC 127.0.0.1:4242 image.dcm export-directory/
go run ./cmd/storescu -- -max-invoked 4 -called ORTHANC 127.0.0.1:4242 image.dcm
```

`-max-invoked` defaults to 1 (serial). Values greater than one propose an
Asynchronous Operations Window and pipeline only when the peer accepts a
window greater than one.

Output uses only one-based source ordinals, outcome, and status.

## Independent interop

Set `DICOM_STORE_INTEROP_ADDRESS` to an independently implemented Storage SCP
(for example pynetdicom, DCMTK, or Orthanc) to enable the opt-in test. Optional
`DICOM_STORE_INTEROP_CALLED_AE` and `DICOM_STORE_INTEROP_CALLING_AE` override AE
titles. `DICOM_STORE_INTEROP_FILES` accepts an OS path-list of non-PHI Part 10
fixtures, allowing one run to exercise mixed native and compressed syntaxes.

Planner allocation and real loopback association-reuse comparisons at 1, 10,
100, and 1,000 instances are available with:

```sh
go test ./net/dimse -run '^$' -bench 'Benchmark(PlanStoreBatch|StoreSessionAssociationReuse)' -benchmem
```
