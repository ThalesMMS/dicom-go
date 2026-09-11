# DICOM file-set authoring

The `dicomdir` package has three deliberately separate surfaces:

- the compatibility reader (`References`, `ReferencedPaths`) extracts active
  references from existing DICOMDIR datasets;
- `FileSet` indexes files that already exist below a media root, validates
  their selection keys, supports bounded recursive scans, queries, removal,
  and statistics;
- `WriteDICOMDIR` and `CommitDICOMDIR` author or transactionally publish a
  Basic Directory IOD for a validated `FileSet`.

The author never copies, renames, or rewrites referenced instances. Existing
paths must already be valid PS3.10 File IDs: one to eight components, each one
to eight characters from `A-Z`, `0-9`, and `_`. Absolute, drive-qualified,
UNC, device, traversal, symlink, case-colliding, and hard-link aliases are
rejected. `DICOMDIR` is reserved for the single root directory file.

## Building and publishing

```go
set, err := dicomdir.NewFileSet(root, dicomdir.Options{
    FileSetID: "MEDIA_01",
})
if err != nil {
    return err
}
report, err := set.Scan(ctx, dicomdir.ScanOptions{
    Policy: dicomdir.EntryReject,
})
if err != nil {
    return err
}
_, err = dicomdir.CommitDICOMDIR(ctx, set, dicomdir.WriteOptions{})
```

`EntryReject` stops at the first non-indexable entry. `EntrySkip` records a
bounded, value-free diagnostic and continues. Direct `Add` always returns its
error. Metadata indexing stops before top-level Pixel Data and uses finite
parser limits; no Pixel Data payload is materialized.

The initial authoring profile supports the standard PATIENT -> STUDY -> SERIES
-> IMAGE hierarchy. The SOP Class classifier recognizes additional Annex F
record families, but `Add` rejects those families until their complete Type
1/1C/2 selection-key schemas are implemented. Existing DICOMDIR compatibility
reading remains broader and unchanged.

## Encoding and transaction guarantees

The writer uses `object.WriteFile` with Explicit VR Little Endian. It first
encodes fixed-width zero offset placeholders, reads the actual Item offsets,
encodes again with First/Last/Next/Lower offsets, then reads the result back.
The second pass must have identical Item positions and a fully reachable,
acyclic hierarchy. Active references are compared with the File ID, ancestor
keys, SOP Class/Instance, related general SOP Class UIDs, and Transfer Syntax
of the referenced source.

`CommitDICOMDIR` creates a private temporary file in the media root, flushes
and closes it, performs strict read-back, revalidates source and destination
identity, and atomically replaces the root `DICOMDIR`. A failed preflight or
write never replaces an existing valid directory. Source identity and
selection metadata are checked again immediately before publication.

Optional descriptor File IDs must resolve to a regular, non-symlink member of
the file-set. The File-set UID is stable across parse/update and is encoded as
the Media Storage SOP Instance UID, not as a SOP Common element in the Basic
Directory dataset.

## Diagnostics, limits, and interoperability

Errors and diagnostics intentionally omit filesystem paths, DICOM values, and
UIDs. `Diagnostic.SourceIndex` is the correlation boundary for callers that
are allowed to show a local path. Default scan and parser budgets are finite;
callers may lower them through `dicomdir.Limits` and `index.Options`.

The regular test suite validates multi-patient hierarchy, exact offsets,
deterministic encoding, malformed graphs, stale sources, charset round-trips,
portable paths, transaction safety, and source immutability. An independent
pydicom gate is available when Python and pydicom are installed:

```sh
DICOM_GO_PYDICOM_DICOMDIR=1 go test ./dicomdir -run PydicomInterop -count=1 -v
```

The implementation follows DICOM PS3.3 Annex F (Directory Information) and
PS3.10 sections 8.2, 8.5, and 8.6 (file-set structure, File IDs, and DICOMDIR).
