# dicom-go

A pure Go implementation of the DICOM medical imaging standard.

`dicom-go` uses idiomatic Go packages, concrete value
types and explicit registries. The `v0.1.0` release provides a practical subset
for reading and writing Part 10 files, converting datasets to/from DICOM JSON,
extracting pixel data, and running minimal C-ECHO/C-STORE workflows.

This project does not claim full DICOM conformance. See
[docs/CONFORMANCE.md](./docs/CONFORMANCE.md) for the exact `v0.1.0` capability
scope and limitations.

Security note: the network stack and CLI tools are intended for trusted networks
only. There is no TLS, authentication, or user identity negotiation. Do not
expose these tools directly to untrusted peers.

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

### Pixel Data

Native uncompressed frame extraction:

```go
frames, err := pixeldata.ExtractNativeFrames(file.Dataset)
```

RLE Lossless decoding is available as an optional codec and must be registered:

```go
if err := rle.RegisterDefault(); err != nil {
	panic(err)
}
pixels, err := pixeldata.Extract(file.Dataset)
if err != nil {
	panic(err)
}
frames, err := pixeldata.DecodeFrames(file.TransferSyntax.UID, pixels, file.Dataset)
if err != nil {
	panic(err)
}
_ = frames
```

Example: [`examples/pixeldata`](./examples/pixeldata).

### Streaming large element values (native Pixel Data)

By default, `dicom-go` materializes defined-length element values into memory.
For large values (notably native/uncompressed Pixel Data, tag `(7FE0,0010)`), you
can opt into a mode that *skips materialization* and then stream the raw encoded
value bytes on demand.

Enable skipping by setting `InlineValueBytesThreshold` (bytes). Any defined-length
*primitive* element whose length is **strictly greater** than the threshold is
not stored in memory (the parsed element will have no inline value).

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

Notes/limitations:

- Streaming uses an underlying seekable input (an `io.ReadSeeker`).
  When you read from a file path via `object.OpenFileWithOptions`, this is
  supported.
- Only **defined-length primitive** values are eligible for this skip/stream path.
  Undefined-length values, sequences, and encapsulated/compressed Pixel Data are
  parsed normally (and are not currently streamable via `CopyValueTo`).
- `InlineValueBytesThreshold = 0` preserves historical behavior (always
  materialize, subject to `MaxElementBytes`).

### CLI Tools

Inspect files:

```sh
go run ./cmd/dcmdump -- image.dcm
go run ./cmd/dcmdump -- -json image.dcm
```

Verification:

```sh
go run ./cmd/echoscp -- -port 11112 -single
go run ./cmd/echoscu -- -host 127.0.0.1 -port 11112
```

Storage:

```sh
go run ./cmd/storescp -- -address 127.0.0.1:11112 -output ./received
go run ./cmd/storescu -- 127.0.0.1:11112 image.dcm
```

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
- See [`docs/INTEROP_ORTHANC.md`](./docs/INTEROP_ORTHANC.md) and
  [`scripts/interop_retrieve_orthanc.sh`](./scripts/interop_retrieve_orthanc.sh) for a
  reproducible Orthanc setup.

Network examples: [`examples/echo`](./examples/echo) and
[`examples/store`](./examples/store). Query/Retrieve examples are currently
provided via `cmd/findscu` and `cmd/dicom-go-retrieve`.

## Features

- DICOM Part 10 read/write.
- Native transfer syntaxes: Implicit VR Little Endian, Explicit VR Little
  Endian, Explicit VR Big Endian.
- Encapsulated Pixel Data preservation for supported fragment-only workflows.
- DICOM JSON marshal/unmarshal.
- Native pixel data extraction.
- Optional RLE Lossless pixel codec.
- DICOM UL association negotiation over plain TCP.
- Minimal DIMSE C-ECHO, C-STORE and Study Root C-FIND (SCU).
- CLI tools for dump, echo, store, find and UID generation.

## Limitations / production-readiness warnings

`dicom-go` `v0.1.0` is intentionally limited and is **not** a complete DICOM toolkit.
In particular, do not assume broad interoperability or conformance beyond what is
explicitly documented.

Not implemented / out of scope in `v0.1.0`:

- Most compressed pixel codecs (JPEG/JPEG-LS/JPEG 2000/MPEG/HEVC, etc.).
- Deflated transfer syntaxes.
- TLS (DICOM over TLS) and security/auditing profiles.
- Many DIMSE services.

Networking cautions:

- Query/Retrieve support is **limited** (implemented: Study Root C-FIND SCU and
  Study Root C-MOVE SCU only).
- Storage Commitment is currently exposed as **building blocks** (N-ACTION / N-EVENT-REPORT
  primitives + in-memory transaction correlation), not as a production-ready long-lived
  service.
- The library does **not** support asynchronous/concurrent DIMSE operations on a single
  association.

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
| `pixeldata/rle` | Optional pure Go RLE Lossless decoder. |
| `net/ul` | DICOM Upper Layer PDU codec and association negotiation. |
| `net/dimse` | Minimal C-ECHO, C-STORE, C-FIND, C-MOVE and Storage Commitment command/data helpers. |
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

Regenerate the standard dictionary:

```sh
go generate ./dictionary/std
```
