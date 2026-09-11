# JPEG-LS optional adapter

This nested module provides the optional native JPEG-LS pixel data adapter for
`dicom-go`. The base module already provides dependency-free JPEG-LS Lossless
(`1.2.840.10008.1.2.4.80`) decode and explicit encode for its qualified pure-Go
subset, plus explicit unsigned Near-Lossless decode/encode. This module stays
separate so applications can select and independently qualify a CharLS backend
without imposing a native dependency on other consumers.

The default build keeps the adapter boundary pure Go. Applications that compose
it with `pixeldata/builtin` register an explicit `Decoder` implementation for
Near-Lossless through `RegisterNearLossless`. Standalone consumers may use
`Register` or `RegisterDefault` when the injected backend must own both JPEG-LS
transfer syntax UIDs. The native decoder is `NewCharLSDecoder()`, enabled only
with the `jpegls_charls` build tag.

Supported boundaries:

- registers JPEG-LS Near-Lossless alone, or both Lossless and Near-Lossless,
  through distinct explicit APIs;
- validates common pixel metadata before decode;
- reuses the bounded shared JPEG-LS frame assembler for single/multiple Items;
- decodes native frame bytes for supported grayscale 8-bit and 16-bit data
  through CharLS when the `jpegls_charls` profile is enabled; RGB ILV=0 component
  planes are normalized to sample order and ILV=2 is supported; ILV=1 is excluded;
- returns typed errors for unavailable decoders, unsupported metadata,
  unsupported fragment layouts, malformed frames returned by decoder backends,
  and decoded frame size mismatches;
- implements `pixeldata.ContextCodec` and checks cancellation before admitting
  each frame. Native CharLS is not guaranteed to abort a single in-flight
  frame; `errors.Is(err, context.Canceled)` / `DeadlineExceeded` are preserved
  and are not classified as malformed frames.

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
import (
	jpeglscodec "github.com/ThalesMMS/dicom-go/examples/codec-adapters/jpegls"
	"github.com/ThalesMMS/dicom-go/pixeldata"
)

registry := pixeldata.NewMemoryRegistry()
if err := jpeglscodec.Register(registry, jpeglscodec.NewCharLSDecoder()); err != nil {
	panic(err)
}
```

Without `-tags jpegls_charls`, `NewCharLSDecoder()` returns a decoder that
reports `ErrDecoderUnavailable`.

Twin-Viewer receives JPEG-LS Lossless through the builtin pure-Go registry in
every profile. Its explicit `jpegls_charls` profile adds the CharLS-backed
Near-Lossless decoder. The plain `jpegls` profile registers only the unavailable
Near-Lossless boundary, so `.81` reports `JPEG-LS decoder unavailable` while
`.80` remains available without native CharLS.

## Tests

Run the default adapter tests directly:

```sh
go test ./...
```

Run the CharLS-backed tests when the native library is installed:

```sh
CGO_ENABLED=0 go test -tags jpegls_charls ./...
```

From the `dicom-go` root:

```sh
make codec-jpegls-charls-check
```

The base-module JPEG-LS qualification uses bit-exact pydicom/GDCM fixtures for
decode and a separate CharLS interoperability gate for streams emitted by the
pure-Go encoder.

The explicit pure-Go Near-Lossless encoder is independently qualified by
`TestCharLSDecodesPureGoNearLosslessEncoderFullSamples`: 216 frames and
1,839,024 samples. See [policy and reproduction](../../../docs/JPEGLS_NEAR_LOSSLESS_ENCODER.md).
