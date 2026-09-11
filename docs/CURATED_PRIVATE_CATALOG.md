# Optional reviewed private catalog

`dictionary/curated` supplies 21 CT/MR definitions through the existing
[private creator resolver](PRIVATE_CREATORS.md). It has no registration side
effects, network access or dependency from the default parser. Its records are
immutable; queries and detached snapshots support concurrent reads.

## Provenance and bounded coverage

The source is [DCMTK 3.6.9 private.dic](https://github.com/DCMTK/dcmtk/blob/ac002900cab167509881e5b837cdef5dcb07cd37/dcmdata/data/private.dic),
commit `ac002900cab167509881e5b837cdef5dcb07cd37`. Every record in
[`catalog.json`](../dictionary/curated/catalog.json) carries creator, odd group,
byte offset, VR, VM, upstream name, project, version, commit, source URL, complete
source SHA-256, line and exact row, license URL, restrictions and verification
status. Generated `Origin` also carries the exact row's SHA-256 (without newline).
`Name` and `Keyword` preserve the reviewed upstream token.

All entries have VM `1`. Offsets are relative to the creator's reserved block:

| Creator | Group | Offset | VR | Name / keyword |
| --- | --- | --- | --- | --- |
| GEMS_ACQU_01 | 0019 | 02 | SL | NumberOfCellsInDetector |
| GEMS_ACQU_01 | 0019 | 03 | DS | CellNumberAtTheta |
| GEMS_ACQU_01 | 0019 | 04 | DS | CellSpacing |
| GEMS_ACQU_01 | 0019 | 23 | DS | TableSpeed |
| GEMS_ACQU_01 | 0019 | 27 | DS | GantryPeriod |
| SIEMENS CSA HEADER | 0029 | 08 | CS | CSAImageHeaderType |
| SIEMENS CSA HEADER | 0029 | 09 | LO | CSAImageHeaderVersion |
| SIEMENS CSA HEADER | 0029 | 10 | OB | CSAImageHeaderInfo |
| SIEMENS CSA HEADER | 0029 | 18 | CS | CSASeriesHeaderType |
| SIEMENS CSA HEADER | 0029 | 19 | LO | CSASeriesHeaderVersion |
| SIEMENS CSA HEADER | 0029 | 20 | OB | CSASeriesHeaderInfo |
| Philips MR Imaging DD 001; PHILIPS MR IMAGING DD 001 | 2005 | 20 | SL | NumberOfChemicalShifts |
| Philips MR Imaging DD 001; PHILIPS MR IMAGING DD 001 | 2005 | 2D | SS | NumberOfStackSlices |
| Philips MR Imaging DD 001; PHILIPS MR IMAGING DD 001 | 2005 | B0 | FL | DiffusionDirectionRL |
| Philips MR Imaging DD 001; PHILIPS MR IMAGING DD 001 | 2005 | B1 | FL | DiffusionDirectionAP |
| Philips MR Imaging DD 001; PHILIPS MR IMAGING DD 001 | 2005 | B2 | FL | DiffusionDirectionFH |

The final five rows represent ten entries: both spellings occur explicitly in
upstream. No general case folding or inferred creator aliases are applied.
`reviewed-upstream` means the definition was checked against this pinned project
source. It is not validation by the manufacturer for every scanner or software
release. There is no numerical coverage target or imported synthetic vendor-tag
generator. Unknown/UN definitions, repeating groups and fixed-block-only entries
are excluded. CSA values remain opaque OB; this package does not decode CSA blobs.

The selected dictionary definitions use the release's BSD-3-Clause terms.
The [complete applicable notice](../dictionary/curated/LICENSE.DCMTK) preserves
copyright, redistribution conditions and disclaimer; it is also available via
`curated.LicenseNotice()`. Preserve it in source and binary distributions.

## Explicit use and auditing

```go
catalog, err := curated.New()
if err != nil { return err }
dict := dictionary.Chain{catalog, std.Dictionary}
file, err := object.ReadFileWithOptions(source, object.ReadFileOptions{
    Dictionary: dict,
})
if err != nil { return err }
defer file.Close()
record, found := catalog.Lookup("GEMS_ACQU_01", 0x0019, 0x02)
// record.Definition and record.Origin are detached values.
```

Imports are `dictionary/curated`, `dictionary`, `dictionary/std` and `object`
under `github.com/ThalesMMS/dicom-go`. Supply the dictionary when reading implicit
VR data; inspection cannot retroactively decode raw UN values. Actual block
assignment comes from each dataset/item's own creator reservation, following
[PS3.5 7.8](https://dicom.nema.org/medical/dicom/2026c/output/chtml/part05/sect_7.8.html).
Explicit VR remains authoritative. Unknown definitions preserve the resolver's
UN/raw fallback. No default dictionary, charset or privacy rule is changed.

Put a caller-owned overlay first: `dictionary.Chain{local, catalog, std.Dictionary}`.
`Lookup` reports only this catalog's origin; when an earlier overlay wins, audit
the effective definition at that provider. `ByTag` and `ByKeyword` return no match
because these definitions have no absolute tag until a creator reserves a block.
`Entries()` returns a detached slice. A known definition does not authorize
retention during de-identification; the existing basic profile removes private
creators and values, including known CSA OB data.

## Offline regeneration and verification

From the library module:

```sh
go run ./cmd/privatecataloggen
go run ./cmd/privatecataloggen -check
go generate ./dictionary/curated
go run ./cmd/privatecataloggen -check -verify-upstream /path/to/pinned/private.dic
```

The last command additionally checks the exact upstream file hash and every
selected line, and rejects competing block-relative definitions. Obtain that
file separately from the pinned commit; the generator never downloads it.
Ordinary generation checks the committed reviewed source and recorded rows;
it does not claim to authenticate an arbitrary replacement source against the
remote project. Adding entries requires source and license review, not merely
setting a verification label.

Generation sorts by creator/group/offset and has no timestamps. `-check` compares
bytes without writing. Validation rejects duplicate/conflicting keys, duplicate
JSON fields (including case variants), unknown fields, trailing documents,
noncanonical keys, invalid/UN VR, unsupported VM syntax and incomplete provenance.
The initial source format supports fixed positive VM, ordered ranges, `N-n` and
`N-Nn`; numeric bounds are at most one million. Input is bounded to 2 MiB, nesting
to 16 and records to 4096. The source format currently accepts reviewed DCMTK
rows only. Invalid input leaves existing output intact; replacement follows
successful validation and formatting through a temporary file in the same folder.

Tests regenerate byte for byte, reverse input order, verify source drift and
rejected conflicts, exercise all 21 definitions in blocks `10`, `1F`, `80`, `FF`,
and cover item scopes, overlay precedence, unknown/raw fallback, privacy removal
and concurrent immutable snapshots. Fixtures contain synthetic values only.
