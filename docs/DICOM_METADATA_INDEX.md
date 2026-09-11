# Bounded DICOM Metadata Indexing

The `index` package extracts a detached metadata record without materializing
Pixel Data. It is intended for opt-in catalog and discovery workflows. It is
not a replacement for a complete parse when an application must validate the
whole object or interpret structured SOP Classes.

## Security model

The built-in core profile excludes patient and descriptive fields. Callers
must explicitly select PHI-bearing profiles or custom elements and must treat
the resulting record as sensitive. DICOM UIDs, dates, descriptions, private
elements, selected values, and filesystem paths can all identify a patient or
institution.

`ReadPath` opens and closes its file before returning. Borrowed `ReaderAt` and
`ReadSeeker` inputs remain caller-owned. Results are detached and never retain
a deferred value reader. Reader limits bound logical metadata processing;
deflated data sets are bounded by the configured logical byte budget, not only
by compressed source size. `Result.BytesScanned` is the logical position
reached: it includes seek-skipped bytes and, for deflated input, counts inflated
DICOM bytes. It is deliberately not presented as a physical I/O measurement.

Errors and diagnostics produced by the package do not include paths, element
values, callback error text, or panic values. Scanner results carry a possibly
sensitive path in `ScanResult.Path`; the nested record's source origin is
cleared so the path has one explicit handling point. Do not log `ScanResult`
or an index record wholesale. Persist fixed error codes and aggregate counters
unless an access-controlled workflow explicitly needs the path or selected
metadata.

Stopping before Pixel Data is a performance boundary, not proof that the
remainder of the object is valid. In particular, corruption after Pixel Data
is not observed. Raw data sets require an explicit transfer syntax; syntax
guessing is not enabled by the scanner. Setting `Options.RawDataSet` forces raw
interpretation from the source's first byte; it is not a Part 10 fallback or
auto-detection switch. The UID is canonicalized through the transfer syntax
registry before parsing.

## Directory scanning

`Scan` is synchronous:

```go
options := index.DefaultScanOptions()
options.Filter = func(path string, info fs.FileInfo) bool {
	return strings.EqualFold(filepath.Ext(path), ".dcm")
}
err := index.Scan(ctx, root, options, func(result index.ScanResult) error {
	// Consume or copy the result here. Returning slowly applies backpressure.
	return nil
})
```

The producer, work queue, result queue, and worker count are bounded. A slow
`yield` callback stops result consumption and therefore propagates
backpressure to readers and traversal. Sending at every pipeline boundary
also observes context cancellation. `Scan` closes all directory handles and
waits for workers before returning.

Callbacks must return. Go cannot safely preempt an arbitrary function call, so
a callback that waits must select on the same context. Filter and yield panics
are recovered at the scanner boundary, converted to typed redacted errors, and
cancel the remaining pipeline. A returned yield error is likewise redacted;
its text is not retained.

The default symlink policy is `SymlinkIgnore`:

- `SymlinkIgnore` skips all symbolic links.
- `SymlinkReject` yields a structured path plus a value-free symlink error.
- `SymlinkFollowFiles` follows only links resolving to regular files. For a
  directory root, the resolved file must remain beneath that root. Directory
  links, broken links, cycles, and escaping targets are rejected and are never
  traversed. A root that is itself a file link is treated as the explicitly
  selected source; a root directory link is never followed.

Regular-file identities and opened directory identities are rechecked to
reduce replacement races. Portable path traversal cannot provide the same
guarantees as a platform-specific `openat` sandbox against a tree being
maliciously renamed during the scan. Do not use a concurrently attacker-writable
tree as a security boundary.

## Resource limits

Use `DefaultScanOptions` and tighten limits for the deployment. Zero numeric
scanner fields receive finite defaults; negative values are invalid.

- `Workers` and `QueueDepth` bound concurrency and pending work. They also have
  hard public ceilings.
- `MaxFiles` counts every non-directory entry before filter or symlink policy,
  preventing a rejecting filter or symlink flood from making traversal
  unbounded.
- `MaxDirectories` includes the root directory.
- `MaxDepth` counts directories below the root; the root has depth zero.
- `MaxPathBytes` applies to discovered and resolved paths.
- `MaxErrors` bounds yielded per-path failures; the next failure cancels the
  scan with a resource-limit error.
- `ReadOptions.Limits` independently bounds logical DICOM bytes, selected
  bytes and values, primitive elements, total parser tokens, encapsulated
  fragments, sequence depth, and diagnostics for each file.

Directory entries are read in fixed-size batches rather than loading an entire
large directory at once. Special files such as devices, sockets, and FIFOs are
counted but never opened.

## Integration guidance

Adopt the indexer behind an explicit feature or adapter first, and compare its
records with the existing full parser on representative fixtures. Preserve the
full parse for SEG, SR, GSPS, KOS, RT Structure Set, RT Dose, Parametric Map,
VPS, and other workflows that derive relationships from nested content.
Metadata indexing should not change import hashing, duplicate detection,
atomic storage, or viewer decoding defaults.

Useful local gates are:

```sh
go test ./index -count=1
go test -race ./index -count=1
go vet ./index
```

Fuzz corpora should cover malformed Part 10 and raw inputs, explicit and
undefined-length sequences, native and encapsulated Pixel Data headers,
deflated streams, character-set inheritance, long pre-Pixel metadata, private
selectors, cancellation, and every exact/+1 resource boundary. Test fixtures
must be synthetic and contain no real patient data.
