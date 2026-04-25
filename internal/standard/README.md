# Standard Dictionary Source

`internal/standard/dicom.dic` and `internal/standard/uids.tsv` are the versioned
input sources for `cmd/dicomdictgen` and `cmd/dicomuidgen`.

Source:

- URL: `https://raw.githubusercontent.com/DCMTK/dcmtk/master/dcmdata/data/dicom.dic`
- Upstream project: DCMTK
- Vendored on: April 22, 2026
- File header at vendoring time: generated from `DICOM PS 3.6-2026b` and `PS 3.7-2026b` on `2026-03-28`

UID source:

- Table: `DICOM PS 3.6` table `A-1`
- Vendored file: `internal/standard/uids.tsv`

Why this file is committed:

- dictionary generation stays reproducible without network access
- entry ordering in `dictionary/std/std_gen.go` is deterministic
- UID entry ordering in `dictionary/uid/std_gen.go` is deterministic
- updates are explicit reviewable changes instead of implicit fetches at build time

Update procedure:

1. Download the latest `dicom.dic` from the URL above into `internal/standard/dicom.dic`.
2. Review upstream header changes, especially the PS3.6/PS3.7 version lines.
3. Run `go generate ./dictionary/std`.
4. Update `internal/standard/uids.tsv` from the current PS3.6 table A-1 source.
5. Run `go generate ./dictionary/uid`.
6. Re-run `go generate ./dictionary/std` and `go generate ./dictionary/uid`, then confirm the outputs are byte-identical.
7. Run `go test ./...`.

Design notes for the Go generator:

- Repeating tag ranges are expanded to concrete tags because `dicom-go` currently exposes exact-tag lookup only.
- Context-dependent VRs are relaxed to exact Go VRs: `xs -> US`, `ox -> OW`, `px -> OW`, `lt -> OW`, `up -> UL`.
- Human-readable `Entry.Name` values are derived from the DICOM keyword because `dicom.dic` does not carry the long display name field used by `dicom-go`.
- The UID source is committed as TSV instead of fetching the PS3.6 DocBook XML during generation.
- Keyword lookup in the Go UID registry is case-insensitive to match the existing `dictionary` package contract.
