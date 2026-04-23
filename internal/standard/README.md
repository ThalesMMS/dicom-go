# Standard Dictionary Source

`internal/standard/dicom.dic` and `internal/standard/uids.tsv` are the versioned
input sources for `cmd/dicomdictgen` and `cmd/dicomuidgen`.

Source:

- URL: `https://raw.githubusercontent.com/DCMTK/dcmtk/master/dcmdata/data/dicom.dic`
- Upstream project: DCMTK
- Vendored on: April 22, 2026
- File header at vendoring time: generated from `DICOM PS 3.6-2026b` and `PS 3.7-2026b` on `2026-03-28`

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
