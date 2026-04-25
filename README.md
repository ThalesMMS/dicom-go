# dicom-go

A pure Go implementation of the DICOM medical imaging standard.

`dicom-go` uses a layered architecture with idiomatic Go packages, concrete
value types and explicit registries. The `v0.1.0` release provides a practical subset
for reading and writing Part 10 files, converting datasets to/from DICOM JSON,
extracting pixel data, and running minimal C-ECHO/C-STORE workflows.

This project does not claim full DICOM conformance. See
[docs/CONFORMANCE.md](./docs/CONFORMANCE.md) for the exact `v0.1.0` capability
scope and limitations.

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

Network examples: [`examples/echo`](./examples/echo) and
[`examples/store`](./examples/store).

## Features

- DICOM Part 10 read/write.
- Native transfer syntaxes: Implicit VR Little Endian, Explicit VR Little
  Endian, Explicit VR Big Endian.
- Encapsulated Pixel Data preservation for supported fragment-only workflows.
- DICOM JSON marshal/unmarshal.
- Native pixel data extraction.
- Optional RLE Lossless pixel codec.
- DICOM UL association negotiation over plain TCP.
- Minimal DIMSE C-ECHO and C-STORE.
- CLI tools for dump, echo, store and UID generation.

## Limitations

`v0.1.0` is intentionally limited. Compressed JPEG/JPEG-LS/JPEG 2000/MPEG/HEVC
pixel codecs, deflated transfer syntaxes, TLS, Query/Retrieve and formal SOP
Class conformance validation are not implemented.

Read [docs/CONFORMANCE.md](./docs/CONFORMANCE.md) before using this module with
untrusted files, large studies or production network peers.

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
| `net/dimse` | Minimal C-ECHO and C-STORE command/data helpers. |
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
