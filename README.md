# dicom-go

A pure Go implementation of the DICOM medical imaging standard.

`dicom-go` uses idiomatic Go packages, concrete value
types and explicit registries. The `v0.1.0` release provides a practical subset
for reading and writing Part 10 files, converting datasets to/from DICOM JSON,
extracting pixel data, running reusable DIMSE network workflows, defining the
DICOMweb client/protocol boundary, and using headless clinical-viewer primitives
such as rendering, ROI, DICOM file-set reading/authoring and
de-identification helpers.

This project does not claim full DICOM conformance. See
[docs/CONFORMANCE.md](./docs/CONFORMANCE.md) for the exact `v0.1.0` capability
scope and limitations.

Security note: the network stack and CLI tools are intended for trusted networks
by default. The UL library can opt into TLS with caller-supplied `tls.Config`
values and can convey DICOM User Identity items through caller-supplied
callbacks, but the CLI defaults remain plain TCP and there is no built-in
authentication, authorization or audit-log backend. Do not expose these tools
directly to untrusted peers.

## Installation

Requirements:

- Go 1.22 or newer.

Install in a Go module:

```sh
go get github.com/ThalesMMS/dicom-go
```

## Quick Start

Open a Part 10 file and read Patient Name:

```go
package main

import (
	"fmt"

	"github.com/ThalesMMS/dicom-go"
	"github.com/ThalesMMS/dicom-go/core"
)

func main() {
	file, err := dicom.OpenFile("image.dcm")
	if err != nil {
		panic(err)
	}

	name, ok := file.GetString(core.NewTag(0x0010, 0x0010))
	if ok {
		fmt.Println(name)
	}
}
```

Runnable example:

```sh
go run ./examples/readfile -- image.dcm
```

## Usage

### File Reading

Use the root `dicom` package for common read paths, or use `object` directly
when you need lower-level options:

Option 1, default Part 10 reader:

```go
file, err := dicom.OpenFile("image.dcm")
if err != nil {
	panic(err)
}
```

Option 2, Part 10 reader with defensive limits:

```go
limitedFile, err := object.OpenFileWithOptions("image.dcm", object.ReadFileOptions{
	MaxElementBytes: 16 << 20,
	MaxTotalBytes:   128 << 20,
})
if err != nil {
	panic(err)
}
_ = limitedFile
```

Example: [`examples/readfile`](./examples/readfile).

### Private Dictionary Overlays

Implicit VR parsing uses the configured `dictionary.DataDictionary` to resolve
element VRs. For private or site-specific tags, compose an overlay before the
standard dictionary so unknown standard tags still resolve normally:

```go
type privateDictionary struct {
	byTag map[core.Tag]dictionary.Entry
}

func (d privateDictionary) ByTag(tag core.Tag) (dictionary.Entry, bool) {
	entry, ok := d.byTag[tag]
	return entry, ok
}

func (d privateDictionary) ByKeyword(string) (dictionary.Entry, bool) {
	return dictionary.Entry{}, false
}

private := privateDictionary{byTag: map[core.Tag]dictionary.Entry{
	core.NewTag(0x0011, 0x1001): {Tag: core.NewTag(0x0011, 0x1001), VR: core.VRLO},
}}
dict := dictionary.Chain{private, std.Dictionary}

file, err := object.OpenFileWithOptions("implicit.dcm", object.ReadFileOptions{
	Dictionary: dict,
})
```

Unknown private tags still fall back to `UN`, preserving defined-length values
as raw bytes instead of guessing a VR.

### File Writing

Writing is provided by the `object` package. Build or modify an `object.File`,
then write it with `object.WriteFile`:

```go
out, err := os.Create("out.dcm")
if err != nil {
	panic(err)
}
defer out.Close()

if err := object.WriteFile(out, file); err != nil {
	panic(err)
}
```

Example: [`examples/writefile`](./examples/writefile).

### JSON Conversion

Use `dicomjson` for DICOM JSON model conversion:

```go
data, err := dicomjson.MarshalPretty(file.Dataset)
if err != nil {
	panic(err)
}
obj, err := dicomjson.Unmarshal(data, std.Dictionary)
if err != nil {
	panic(err)
}
_ = obj
```

`dicomjson.Options.BulkDataURIFunc` can replace large binary values with
`BulkDataURI`. Unmarshal preserves `BulkDataURI` references but does not fetch
external bulk data.

Example: [`examples/json`](./examples/json).

### SOP Class Validation Hooks

SOP Class-specific validation is opt-in. Applications can attach narrow hooks
for required attributes without changing parse/read behavior:

```go
rule := object.RequiredAttributeRule{
	SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", // CT Image Storage
	Attributes: []object.RequiredAttribute{
		{Tag: core.NewTag(0x0010, 0x0020), Keyword: "PatientID"},
	},
}
err := file.Dataset.ValidateSOPClass(object.ValidationOptions{
	Hooks: []object.SOPClassValidationHook{rule},
})
```

The library provides hooks and small helpers only; it does not ship a complete
SOP Class conformance rule set.

### Uniform Validation and Lifecycle Hooks

The `validation` package adds explicitly opt-in VR/VM, dictionary, dataset and
File Meta validation plus parser/writer lifecycle hooks. Existing read/write
APIs remain unchanged and do not enable these checks automatically. Reports are
bounded and omit element values; hook callbacks are trusted in-process code.
See [`docs/VALIDATION.md`](./docs/VALIDATION.md) for modes, hook actions,
streaming behavior and performance gates.

### Pixel Data

Native uncompressed frame extraction:

```go
frames, err := pixeldata.ExtractNativeFrames(file.Dataset)
```

Encapsulated Uncompressed Explicit VR Little Endian
(`1.2.840.10008.1.2.1.98`) is assembled into native frame bytes without a pixel
codec. Compressed encapsulated transfer syntaxes require an optional codec to be
registered before decoding. JPEG Baseline/Extended, JPEG Lossless, RLE Lossless,
JPEG-LS, and JPEG 2000/HTJ2K codecs are available in optional packages. The
`jpeglscodec` and `jpeg2000codec` aliases below refer to the nested optional
modules under `github.com/ThalesMMS/dicom-go/examples/codec-adapters/`:

The dependency policy is documented in
[`docs/CODEC_DEPENDENCY_POLICY.md`](docs/CODEC_DEPENDENCY_POLICY.md). The
default profile stays pure Go and permissive-only; LGPL/GPL/AGPL codec code is
not accepted. Native or CGO codec adapters must be explicit opt-ins behind build
tags or nested optional modules, and must not become base-module dependencies.
Synthetic codec fixture and conformance workflows are documented in
[`docs/CODEC_FIXTURE_WORKFLOW.md`](docs/CODEC_FIXTURE_WORKFLOW.md).

```go
if err := jpegcodec.RegisterDefault(); err != nil {
	panic(err)
}
if err := jpeglossless.RegisterDefault(); err != nil {
	panic(err)
}
if err := rle.RegisterDefault(); err != nil {
	panic(err)
}
// The JPEG-LS adapter is dependency-injected. Applications supply a
// dependency-specific decoder; nil registers the boundary but returns
// jpegls.ErrDecoderUnavailable during decode.
var jpeglsDecoder jpeglscodec.Decoder // supplied by an optional decoder package
if err := jpeglscodec.RegisterDefault(jpeglsDecoder); err != nil {
	panic(err)
}
// Choose one JPEG 2000 / HTJ2K backend. The pure-Go adapter is a nested
// optional module so the base module does not pull the JPEG 2000 dependency
// unless applications opt in.
if err := jpeg2000codec.RegisterDefault(); err != nil {
	panic(err)
}
// OpenJPEG-backed fallback builds can instead use RegisterOpenJPEGDefault.
// That path requires the jpeg2000_openjpeg build tag and OpenJPEG's
// opj_decompress executable. It uses OpenJPEG for JPEG 2000 Part 1 and keeps
// JPEG 2000 Part 2 / HTJ2K on the pure-Go fallback path.
pixels, err := pixeldata.Extract(file.Dataset)
if err != nil {
	panic(err)
}
frames, err := pixeldata.DecodeFrames(file.TransferSyntax.UID, pixels, file.Dataset)
if err != nil {
	panic(err)
}
_ = frames

nativeFile, err := pixeldata.DecompressFile(file, pixeldata.DecompressOptions{})
if err != nil {
	panic(err)
}
_ = nativeFile // Explicit VR Little Endian by default.
```

If no codec is registered for a compressed encapsulated transfer syntax,
`DecodeFrames` returns an error wrapping `pixeldata.ErrCodecNotFound` with the
transfer syntax UID, name, registration hint and currently registered codec UIDs
when available. JPEG-LS, JPEG 2000/HTJ2K, and JPEG XL syntaxes remain
metadata/payload-readable even when their optional decoder is absent. JPEG XL
decoding is supplied only by the nested opt-in adapter; the base module does
not ship a JPEG XL decoder or native runtime. The `codecfull` release profile
qualifies that adapter with its pinned `libjxl`/`djxl` runtime and external
fixture evidence. MPEG,
H.264, and HEVC syntaxes can be inspected and streamed with the `video` package
for an application-owned native media pipeline; they deliberately remain
outside the still-image decoder registry. The `jpip` package resolves JPIP
references only through an explicit host/scheme policy, scopes credentials to
their exact origin, revalidates redirects, and provides cancellable bounded
retrieval plus an LRU of full, ranged, and progressive representations.
`jpip.Client.DecodeFrame` feeds complete `image/jp2`, `image/jph`, or
`image/jphc` responses through the caller's registered JPEG 2000/HTJ2K codec;
JPP/JPT data-bin responses return a typed assembly limitation.

`pixeldata.DecompressFile` and `pixeldata.DecompressDataSet` are convenience
APIs for export and receive workflows that need native uncompressed Pixel Data.
They decode through the same registered codecs and return Explicit VR Little
Endian by default, or Implicit VR Little Endian when configured. They do not
perform compression, lossy recompression or arbitrary transfer-syntax
transcoding.

For display-oriented helpers, `render` builds on `pixeldata/display` and
`pixeldata/frame` to render 8/16-bit monochrome frames and 8-bit interleaved RGB
frames, sample pixels, auto-window stacks, build volumes, and produce MPR/MIP/
slab/oblique/VR/CPR images. `roi` provides raster/vector ROI, measurements,
segmentation operations and 2D/3D statistics. Planar RGB, 16-bit RGB, palette
color, YBR conversion and full VOI/presentation-state pipelines remain outside
the supported rendering subset.

The VR renderer treats transfer-function color stops as display sRGB, bakes them
to linear RGB for front-to-back compositing, and converts back to sRGB for the
output image. STL iso-surface export intentionally writes the watertight
thresholded voxel boundary rather than a smoothed marching-cubes mesh, preserving
the exact selected voxel support for deterministic measurement/export workflows.

Examples: [`examples/pixeldata`](./examples/pixeldata) and
[`examples/render-roi`](./examples/render-roi).

### DICOMDIR and de-identification helpers

Small reusable utilities are exposed as public packages:

- `dicomdir` extracts existing DICOMDIR references and builds, validates,
  queries, and transactionally writes bounded DICOM file-sets. See
  [`docs/DICOM_FILE_SETS.md`](./docs/DICOM_FILE_SETS.md).
- `deid` applies a small in-place anonymization subset for common patient tags,
  recursive sequence items, policy-driven private tags, hierarchy UID remapping
  and burned-in pixel risk reporting.
- `gsps` reads and writes grayscale softcopy presentation states for referenced
  images, displayed area, window/level and simple graphic annotations.
- `rtstruct` reads and writes RT Structure Sets, including helper conversion
  from pixel-space vector ROIs to patient-space closed planar contours with
  frame-of-reference and source image references.

These packages are not a full media-import workflow and do not claim full DICOM
Basic Profile de-identification. Applications remain responsible for their
import/export policy, PHI review and compliance requirements.

Example: [`examples/dicom-utilities`](./examples/dicom-utilities).

### Streaming large values and deferred Pixel Data

By default, `dicom-go` materializes defined-length element values into memory.
For large values, you can opt into modes that *skip materialization* and then
stream the raw encoded value bytes on demand.

Use `InlineValueBytesThreshold` for defined-length primitive values. Any
defined-length *primitive* element whose length is **strictly greater** than the
threshold is not stored in memory (the parsed element will have no inline
value), and can later be streamed with `CopyValueTo` when the reader has a
seekable source.

```go
file, err := object.OpenFileWithOptions("image.dcm", object.ReadFileOptions{
	// Keep small values inline, but skip large buffers like Pixel Data.
	InlineValueBytesThreshold: 1 << 20, // 1 MiB
})
if err != nil {
	panic(err)
}

// Stream raw Pixel Data bytes (for defined-length, native/uncompressed Pixel Data)
out, err := os.Create("pixeldata.bin")
if err != nil {
	panic(err)
}
defer out.Close()

n, err := file.Dataset.CopyValueTo(core.NewTag(0x7FE0, 0x0010), out)
if err != nil {
	panic(err)
}
_ = n
```

Use `DeferPixelData` when Pixel Data itself should be consumed without
materializing, including encapsulated/fragmented Pixel Data. With a seekable
source, `CopyValueTo` can stream the raw Pixel Data value bytes, and
`object.WriteFile` can round-trip deferred Pixel Data without loading the value
into memory.

Notes/limitations:

- Streaming skipped/deferred values uses an underlying seekable input (an
  `io.ReadSeeker`). Reading from a file path via `object.OpenFileWithOptions` or
  `object.OpenDataSetWithOptions` provides this and keeps the source open until
  `Close()`.
- `InlineValueBytesThreshold` applies to defined-length primitive values.
  General SQ replay is intentionally unsupported, so sequence item values are
  materialized. Duplicate deferred tags are treated as ambiguous.
- `DeferPixelData` applies to Pixel Data and requires a seekable source. For
  encapsulated Pixel Data, `CopyValueTo` streams the encoded Pixel Data value
  bytes (Basic Offset Table, fragments and delimiters), not the element header.
- `InlineValueBytesThreshold = 0` preserves historical behavior (always
  materialize, subject to `MaxElementBytes`).

### CLI Tools

Inspect files:

```sh
go run ./cmd/dcmdump image.dcm
go run ./cmd/dcmdump -show-offsets image.dcm
go run ./cmd/dcmdump -json image.dcm
go run ./cmd/dcmdump -recover-transfer-syntax legacy-or-raw.dcm
```

Transfer Syntax recovery is disabled by default. The recovery flag and the
dedicated library APIs use bounded inference, report confidence and candidate
diagnostics, and never enable recovery in network ingestion paths. See
[`docs/TRANSFER_SYNTAX_RECOVERY.md`](docs/TRANSFER_SYNTAX_RECOVERY.md) for the
policy and resource-bound contracts.

Verification:

```sh
go run ./cmd/echoscp -- -port 11112 -single
go run ./cmd/echoscu -- -host 127.0.0.1 -port 11112
```

For longer-running echo tests, `echoscp` also exposes bounded service controls:
`-max-associations`, `-max-active-operations`, `-queue-depth` and
`-enqueue-timeout`.

Storage:

```sh
go run ./cmd/storescp -- -address 127.0.0.1:11112 -output ./received
go run ./cmd/storescu -- 127.0.0.1:11112 image.dcm another.dcm directory/
```

`storescp` serves C-STORE and C-ECHO verification only. Other DIMSE commands
on an accepted association are logged as unsupported and abort that association.

CLI errors use `tool: category: detail` formatting for runtime failures. Common
categories are `file`, `parse`, `transfer`, `codec` and `network`; usage errors
still print command help and exit with code 2.

Query (Study Root C-FIND SCU):

```sh
go run ./cmd/findscu -- -host 127.0.0.1 -port 11112 -called PACS_AE -calling CLIENT_AE -level study \
  -k PatientID=12345 -k StudyDate=20240101
```

Minimal Go API example (Study Root C-FIND):

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	assoc, err := ul.Dial("127.0.0.1:11112", ul.DialOptions{
		Context:        ctx,
		CallingAETitle: "CLIENT_AE",
		CalledAETitle:  "PACS_AE",
		Contexts: []ul.PresentationContext{
			dimse.StudyRootFindPresentationContext(),
		},
	})
	if err != nil {
		panic(err)
	}
	defer assoc.Close()

	pc, err := dimse.AcceptedContextForSOPClass(assoc, dimse.StudyRootFindSOPClassUID)
	if err != nil {
		panic(err)
	}

	identifierElements, err := dimse.BuildStudyRootStudyFindKeys(map[string]string{
		"PatientID":        "12345",
		"StudyDate":        "20240101",
		"StudyInstanceUID": "", // return key
		"PatientName":      "", // return key (if supported by SCP)
	})
	if err != nil {
		panic(err)
	}
	identifier := object.FromElements(identifierElements, std.Dictionary)

	identifierSyntax := transfer.ImplicitVRLittleEndian
	if pc.TransferSyntaxUID != "" {
		identifierSyntax = transfer.Syntax{UID: pc.TransferSyntaxUID}
	}

	results, errs := dimse.Find(ctx, assoc, pc.ID, dimse.CFindRequest{
		AffectedSOPClassUID: dimse.StudyRootFindSOPClassUID,
		MessageID:           1,
	}, identifier, identifierSyntax)

	for r := range results {
		if r.Identifier != nil {
			fmt.Println(r.Identifier.Elements())
		}
	}
	if err := <-errs; err != nil {
		panic(err)
	}

	_ = assoc.Release(ctx)
}
```

Query/Retrieve (C-MOVE, Study Root):

```sh
# Issue a Study Root C-MOVE to a remote QR SCP (e.g. Orthanc)
go run ./cmd/dicom-go-retrieve -- \
  -remote 127.0.0.1:4242 \
  -calling-aet DICOMGO \
  -called-aet ORTHANC \
  -move-destination DICOMSTORE \
  -study-uid 1.2.840....
```

Notes:

- C-MOVE causes the remote system to open a **separate** C-STORE association to the
  Move Destination AE. The `-move-destination` value is an AE Title, not a local
  host/port; the remote peer must have that AE Title registered in its AE
  configuration so it can resolve the destination host/port and open the C-STORE
  association to your Storage SCP.
- See [`docs/INTEROP_ORTHANC.md`](./docs/INTEROP_ORTHANC.md),
  [`docs/INTEROP_MATRIX.md`](./docs/INTEROP_MATRIX.md) and
  [`scripts/interop_orthanc_matrix.sh`](./scripts/interop_orthanc_matrix.sh) for
  reproducible opt-in Orthanc checks.

Network examples: [`examples/echo`](./examples/echo) and
[`examples/store`](./examples/store). Query/Retrieve examples are currently
provided via `cmd/findscu` and `cmd/dicom-go-retrieve`.

## Features

- DICOM Part 10 read/write.
- Native transfer syntaxes: Implicit VR Little Endian, Explicit VR Little
  Endian, Explicit VR Big Endian.
- Deflated Explicit VR Little Endian dataset read/write.
- Encapsulated Uncompressed Explicit VR Little Endian frame assembly without a
  pixel codec, plus encapsulated Pixel Data preservation for supported
  fragment-only workflows.
- DICOM JSON marshal/unmarshal.
- Native pixel data extraction.
- Still-image decompression-to-native API for supported registered codecs.
- Optional JPEG Baseline and RLE Lossless pixel codecs.
- DICOM UL association negotiation over plain TCP, with optional TLS configured
  by callers.
- Framework-neutral safe network telemetry, opaque association IDs, bounded raw
  P-DATA capture, progress-based phase timeouts, and a pre-negotiation bounded
  association server. See
  [network observability and operational controls](./docs/NETWORK_OBSERVABILITY.md).
- Minimal DIMSE C-ECHO, C-STORE, Study Root C-FIND (SCU/SCP),
  Study Root C-MOVE (SCU/SCP workflow helpers) and Study/Patient Root
  Query/Retrieve model helper primitives.
- Generic typed DIMSE-N command models, SCU methods and SCP handlers for
  N-EVENT-REPORT, N-GET, N-SET, N-ACTION, N-CREATE and N-DELETE. The existing
  Storage Commitment API remains available as a service-specific adapter.
- DICOMweb is a library responsibility: reusable QIDO-RS, WADO-RS and STOW-RS
  request/response helpers belong here rather than in application backends. The
  `net/dicomweb` package boundary/scaffold exists, while full Twin migration is
  tracked by #126-#130; applications remain responsible for endpoint profiles,
  credentials, jobs, archive import/export and UI state.
- CLI tools for dump, echo, store, find and UID generation.

## Limitations / production-readiness warnings

`dicom-go` `v0.1.0` is intentionally limited and is **not** a complete DICOM toolkit.
In particular, do not assume broad interoperability or conformance beyond what is
explicitly documented.

Not implemented / out of scope in `v0.1.0`:

- Most still-image compressed pixel codecs beyond the optional JPEG
  Baseline/Extended, JPEG Lossless, RLE Lossless, JPEG-LS boundary, and JPEG
  2000/HTJ2K subsets. JPEG XL is not implemented in the base module; it is
  available through a nested opt-in adapter and is qualified separately by the
  `codecfull` release profile.
- Built-in video frame decoding, JPIP JPP/JPT data-bin assembly, deflated
  image-frame compression, and SMPTE ST 2110 media syntaxes.
  The `video` package does provide bounded validation and streaming extraction
  for DICOM MPEG, MPEG-4 AVC/H.264, and HEVC/H.265 payloads so applications can
  hand them to a native media backend without copying the complete payload into
  memory. The `jpip` package provides policy-bound remote retrieval and decoding
  for complete JPEG 2000/HTJ2K responses when the corresponding optional codec
  is registered.
- Security/auditing profiles.
- Service-class workflows built on DIMSE-N, including MPPS, UPS and Print
  Management. The generic protocol layer is present, but applications still
  own the service-specific datasets, state machines and negotiation policy.
- A complete reusable DICOMweb client/helper surface. Applications may still have
  temporary adapters, but protocol semantics should move here instead of becoming
  permanent application-owned networking code.

These broad gaps are tracked as scoped backlog items in
[docs/ROADMAP.md](./docs/ROADMAP.md#tracked-v010-limitation-backlog).
The still-image codec adapter policy is tracked separately in
[docs/ROADMAP.md](./docs/ROADMAP.md#optional-still-image-codec-adapter-plan).

Networking cautions:

- Query/Retrieve support is **limited** (implemented: Study Root C-FIND SCU/SCP,
  Study Root C-MOVE SCU/SCP workflow helpers, a Study Root C-GET SCU workflow
  helper and Patient Root SCU presentation-context/identifier helpers). The
  C-MOVE SCP and C-GET storage APIs use caller-provided callbacks; there is no
  built-in archive.
- Storage Commitment is exposed as N-ACTION / N-EVENT-REPORT primitives, an
  in-memory transaction tracker, event/action dataset helpers and a minimal
  single-request SCP API. It is not a production service with persistence,
  retries or audit integration.
- Generic N-service operations use `dimse.NormalizedClient` and
  `dimse.NormalizedSCPOptions`. They preserve optional command status fields,
  enforce one outstanding operation per association and require an explicit
  presentation-context policy when a Meta SOP Class abstract syntax differs
  from the command SOP Class UID.
- DICOMweb helpers should cover reusable QIDO-RS, WADO-RS and STOW-RS protocol
  behavior. Application code owns node stores, URL/profile configuration,
  credential retrieval, retry policy, job progress, archive integration,
  receiver policy, operation history, UI summaries and auto-query.
- Legacy DIMSE helpers assume **one outstanding operation per association**.
  `ul` can negotiate Asynchronous Operations Window and the explicit
  `dimse.AsyncSession` API multiplexes C-ECHO, C-STORE, C-FIND, C-MOVE, C-GET
  and N-DIMSE under one sole receive owner. It remains opt-in; ordinary helpers
  stay synchronous, and commands are not retried automatically. See
  [`docs/ASYNCHRONOUS_OPERATIONS.md`](docs/ASYNCHRONOUS_OPERATIONS.md).
- `ul.AssociationServer` bounds silent negotiations plus active handlers before
  goroutines are created. `dimse.SCPControls` supplies command/dataset progress,
  operation and cancel-grace policy without turning a slow active transfer into
  an absolute deadline.
- High-level `net/telemetry` events omit endpoints, payloads, identifiers and
  credentials by default. Raw P-DATA capture is a separate explicit PHI-bearing
  sink with mandatory byte budgets; A-ASSOCIATE/User Identity bytes are never
  captured.
- TLS transport is opt-in at the UL library boundary through
  `ul.DialOptions.TLSConfig` and `ul.AcceptOptions.TLSConfig`. Plain TCP remains
  the default and the CLIs do not expose TLS flags yet.
- User Identity negotiation is opt-in through `ul.DialOptions.UserIdentity` and
  `ul.AcceptOptions.UserIdentityHandler`. Accepting an identity item only gives
  application code material for its own policy decision; it is not built-in
  authentication or authorization.
- Authentication, authorization, audit-log persistence, PHI detection and
  de-identification are deployment responsibilities. The `net/audit` package
  provides opt-in local event hooks for service wrappers; it does not implement
  ATNA formatting, secure log transport or retention policy.

Read [docs/CONFORMANCE.md](./docs/CONFORMANCE.md) before using this module with
untrusted files, large studies, or production network peers.

## Package Layout

| Package | Status in v0.1.0 |
|---|---|
| `dicom` | Read-focused convenience facade for common `object` APIs. |
| `core` | Tags, VRs, lengths, elements, primitive values, sequences and fragments. |
| `dictionary` | Dictionary interfaces and entries. |
| `dictionary/std` | Generated standard tag dictionary. |
| `dictionary/uid` | Generated standard UID registry. |
| `encoding` | Endian-aware primitive and text helpers. |
| `transfer` | Transfer syntax registry and built-in syntax definitions. |
| `parser` | Dataset token reader/writer. |
| `object` | High-level object model and Part 10 file read/write APIs. |
| `dicomjson` | DICOM JSON marshal/unmarshal helpers. |
| `pixeldata` | Pixel metadata, native frame extraction and codec registry. |
| `pixeldata/jpeg` | Optional pure Go JPEG Baseline decoder. |
| `pixeldata/rle` | Optional pure Go RLE Lossless decoder. |
| `net/ul` | DICOM Upper Layer PDU codec and association negotiation. |
| `net/audit` | Optional structured audit event model for network service wrappers. |
| `net/dimse` | C-ECHO, C-STORE, Query/Retrieve helpers, all six generic normalized DIMSE services, and Storage Commitment adapters. |
| `net/dicomweb` | Package boundary/scaffold for reusable QIDO-RS, WADO-RS and STOW-RS helpers; full Twin migration remains tracked by #126-#130. |
| `cmd/*` | Runnable command line tools. |
| `examples/*` | Runnable examples by feature area. |

## Development

Use the repository Makefile:

```sh
make fmt
make vet
make test
make build
make check
```

`make check` runs formatting, `go vet`, `go test ./...` and `go build ./...`.
Longer fuzz campaigns for parser, JSON and UL PDU decoding are documented in
[`docs/FUZZING.md`](./docs/FUZZING.md).

Regenerate the standard dictionary:

```sh
go generate ./dictionary/std
```
