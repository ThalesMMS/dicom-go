# dicom-go architecture

`dicom-go` uses small Go interfaces, concrete structs and explicit registries
to keep package boundaries visible and dependencies narrow.

The current capability and limitation surface is maintained in
[CAPABILITIES.md](./CAPABILITIES.md). Stable outcome ordering across the library,
application services, and both UI shells is maintained in
[ROADMAP.md](./ROADMAP.md).

## Dependency direction

```text
core
  |
  +--> dictionary -----> dictionary/std
  +--> dictionary/uid
  +--> encoding

encoding + transfer + dictionary/std
  |
  +--> parser
          |
          +--> object
                 |
                 +--> dicomjson
                 +--> dicomxml
                 +--> pixeldata -----> pixeldata/rle

transfer ---------------------------> net/ul
net/ul + object + parser -----------> net/dimse
object + dicomjson + dicomxml
  + transfer ----------------------> net/dicomweb
object + dicomjson + dicomxml + pixeldata
  + net/ul + net/dimse
  + net/dicomweb -------------------> cmd/*

pixeldata/display + pixeldata/frame --> render --> roi
object + transfer ------------------> root package dicom
object -----------------------------> deid + dicomdir
```

Rules:

1. `core` owns DICOM tags, VRs, lengths, values and elements. It has no I/O.
2. `dictionary` packages depend only on `core`.
3. `encoding` contains primitive endian/text helpers and does not parse files.
4. `transfer` identifies transfer syntaxes and codec requirements.
5. `parser` reads and writes datasets from streams.
6. `object` provides high-level object and Part 10 file APIs.
7. `dicomjson`, `dicomxml`, `pixeldata`, `net/ul`, `net/dimse`, reusable DICOMweb helpers
   and `cmd/*` consume lower layers. DIMSE and DICOMweb packages own protocol
   mechanics, request/response/status mapping and reusable client helpers; they
   must not own application catalogs, node stores, credentials, jobs or UI
   orchestration.
8. `render` owns headless display rendering, stack geometry, MPR/MIP/VR/CPR and
   volume sampling; it may consume `object`, `pixeldata`, `pixeldata/display`
   and `pixeldata/frame`, but never application UI packages.
9. `roi` owns headless ROI masks, vector ROI geometry, measurements,
   segmentation operations and statistics. It may consume `render` geometry and
   volumes; `render` must not import `roi`.
10. `deid` and `dicomdir` own small reusable DICOM object utilities. They may
   consume `object` and `core`; they must not import application code or viewer
   workflows.
11. The root `dicom` package remains a small compatibility facade over common
   read, write, and validation paths, not an implementation package. New
   high-level file and dataset workflows belong in `object`.

## Public API shape

Preferred Part 10 path:

```go
file, err := dicom.OpenFile("image.dcm")
if err != nil {
	// handle error
}

name, _ := file.GetString(core.NewTag(0x0010, 0x0010))
fmt.Println(name)
```

Lower-level dataset path:

```go
obj, err := object.ReadDataSet(r, transfer.ExplicitVRLittleEndian)
```

Writer path:

```go
if err := object.WriteFile(w, file); err != nil {
	// handle error
}
```

The root `dicom` package intentionally keeps the public facade small. New code
may use `object` directly when File Meta, dataset, validation, and transfer
syntax choices must remain explicit at the call site.

Network path:

```go
assoc, err := ul.Dial(address, ul.DialOptions{Contexts: contexts})
pc, err := dimse.AcceptedContextForSOPClass(assoc, sopClassUID)
err = dimse.SendDataSet(assoc, pc.ID, file.Dataset, negotiatedSyntax)
```

Headless rendering/ROI path:

```go
img, err := render.RenderFrame(frame, render.WindowLevel{Center: 40, Width: 400})
encoder, err := render.NewStandardEncodedRenderer(render.DefaultEncodedImageLimits())
encoded, err := encoder.RenderEncoded(ctx, render.EncodedRequest{
    Frame: frame, Window: render.WindowLevel{Center: 40, Width: 400},
    MediaType: render.MediaTypePNG, Width: 512, Height: 512,
})
vol, err := render.BuildVolume(stack)
mask := roi.VectorROI{Shape: roi.ROIRectangle, Points: []image.Point{image.Pt(1, 1), image.Pt(8, 8)}}.Rasterize(16, 16)
stats := roi.Stats2D(mask, valueAt)
```

DICOM media/de-identification path:

```go
paths, err := dicomdir.ReferencedPaths("DICOMDIR")
set, err := dicomdir.NewFileSet(mediaRoot, dicomdir.Options{FileSetID: "MEDIA_01"})
scan, err := set.Scan(ctx, dicomdir.ScanOptions{Policy: dicomdir.EntryReject})
written, err := dicomdir.CommitDICOMDIR(ctx, set, dicomdir.WriteOptions{})
uids := deid.NewUIDRemapper()
err = deid.AnonymizeObject(file.Dataset, deid.Options{}, uids)
_ = paths
_ = scan
_ = written
```

## Design decisions

### Concrete values behind small interfaces

`core.Value` is a small interface with concrete payloads: `RawValue`, `StringValue`,
`SequenceValue`, `FragmentSequence` and `BulkDataValue`.

### Explicit registries

Transfer syntaxes, pixel codecs and dictionaries are visible values. Optional
decoders are registered explicitly. `pixeldata/builtin.NewRegistry` is the
single composition point for the built-in JPEG Baseline/Extended, JPEG Lossless,
JPEG-LS Lossless, and RLE decoder baseline; applications layer product-specific
optional codecs on that caller-owned registry. Encoders use a separate explicit
registry with no package global or default entries. JPEG Baseline, JPEG-LS
Lossless, and RLE encoders are registered explicitly by callers.

### File meta and dataset separation

`object.File` keeps Part 10 preamble, file meta, dataset and transfer syntax
separate. Tag lookup on `File` delegates group `0002` tags to `Meta` and other
tags to `Dataset`.

### Networking remains layered

`net/ul` knows about association negotiation and PDUs. `net/dimse` knows about
DIMSE command sets and P-DATA command/data separation. `net/audit` defines
local event payloads for service wrappers without owning a logging backend.
Object parsing does not depend on sockets.

### Pixel codecs stay optional

The parser can preserve native Pixel Data and encapsulated fragments without
linking image codecs. Decoding, encoding and bounded Transfer Syntax conversion
are `pixeldata` concerns. The core module ships native frame extraction,
Encapsulated Uncompressed assembly, optional explicit decoders and a pure-Go RLE
Lossless encoder; applications decide which registries to inject.

Capability and limitation claims are intentionally kept out of this architecture
document. See [CAPABILITIES.md](./CAPABILITIES.md) and its focused matrices for
the current verified scope.
