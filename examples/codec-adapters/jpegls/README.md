# JPEG-LS optional adapter

This nested module provides the supported opt-in JPEG-LS pixel data adapter for
`dicom-go`. It stays outside the base module so applications that do not decode
JPEG-LS do not inherit native decoder dependencies.

The default build keeps the adapter boundary pure Go. Applications register an
explicit `Decoder` implementation through `Register` or `RegisterDefault`. The
production decoder is `NewCharLSDecoder()`, enabled only with the
`jpegls_charls` build tag.

Supported boundaries:

- registers JPEG-LS Lossless and Near-Lossless transfer syntax UIDs explicitly;
- validates common pixel metadata before decode;
- supports one encapsulated fragment per output frame;
- decodes native frame bytes for supported grayscale 8-bit and 16-bit data
  through CharLS when the `jpegls_charls` profile is enabled;
- returns typed errors for unavailable decoders, unsupported metadata,
  unsupported fragment layouts, malformed frames returned by decoder backends,
  and decoded frame size mismatches.

## CharLS backend

The `jpegls_charls` profile loads the CharLS shared library dynamically at
runtime. CharLS is a BSD-3-Clause C++17 JPEG-LS implementation with a stable C
API for interoperability. The Go dynamic loader uses `github.com/ebitengine/purego`
under Apache-2.0 in this nested optional module. The tested local backend is
CharLS 2.4.2. The `codecfull` release profile rejects any other version.

Expected setup:

- macOS: install CharLS with Homebrew or another package manager that places
  `libcharls.2.dylib` on the dynamic loader path.
- Linux: install the distro CharLS runtime/development package, or build CharLS
  with CMake so `libcharls.so.2` is on the dynamic loader path.
- Windows amd64/arm64: the production Twin-Viewer ZIP packages include the
  architecture-specific CharLS 2.4.2 DLL built from the pinned, SHA-256
  verified upstream source archive. Standalone applications can place
  `charls-2-x64.dll` or `charls-2-arm64.dll` beside the executable.

Set `DICOM_GO_CHARLS_LIBRARY` to an absolute library path to override automatic
library discovery. The loader verifies the required symbols and rejects
incompatible runtimes with `ErrDecoderUnavailable` before decoding.

Registration with the native backend is explicit:

```go
import jpeglscodec "github.com/ThalesMMS/dicom-go/examples/codec-adapters/jpegls"

if err := jpeglscodec.RegisterDefault(jpeglscodec.NewCharLSDecoder()); err != nil {
	panic(err)
}
```

Without `-tags jpegls_charls`, `NewCharLSDecoder()` returns a decoder that
reports `ErrDecoderUnavailable`.

Twin-Viewer enables this backend only in its explicit `jpegls_charls` codec
profile. Its plain `jpegls` profile remains the no-decoder boundary that reports
`JPEG-LS decoder unavailable`, and default Twin-Viewer/default `dicom-go` builds
remain usable without native CharLS.

## Tests

Run the default adapter tests directly:

```sh
go test ./...
```

Run the CharLS-backed tests when the native library is installed:

```sh
CGO_ENABLED=0 go test -tags jpegls_charls ./...
```

From the `dicom-go-dev` root:

```sh
make codec-jpegls-charls-check
```
