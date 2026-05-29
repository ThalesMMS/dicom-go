# JPEG XL optional adapter

This nested module provides the supported opt-in JPEG XL pixel data adapter for
`dicom-go`. It stays outside the base module so applications that do not decode
JPEG XL do not inherit native runtime requirements.

The backend is enabled with the `jpegxl_djxl` build tag and calls the `djxl`
command from the `libjxl` project at runtime. Without that tag, the adapter
still builds but `NewDjxlDecoder()` returns `ErrDjxlUnavailable` from decode.

Supported boundaries:

- registers only the DICOM JPEG XL still-image transfer syntax UIDs:
  `1.2.840.10008.1.2.4.110`, `1.2.840.10008.1.2.4.111`, and
  `1.2.840.10008.1.2.4.112`;
- supports one fragment per frame, plus multi-fragment assembly for a single
  frame;
- validates common unsigned monochrome/RGB 8-bit and 16-bit DICOM pixel
  metadata before decode;
- converts `djxl` PGM/PPM output to native DICOM little-endian frame bytes;
- returns typed errors for unavailable `djxl`, unsupported metadata,
  unsupported fragment layouts, size mismatches, and malformed codestreams.

## Runtime setup

Install a `libjxl` package that provides the `djxl` executable.

- macOS: `brew install jpeg-xl`
- Debian/Ubuntu: install the distro package that provides the `djxl` tool,
  commonly `libjxl-tools`.
- Fedora/RHEL-family distributions: install the package that provides `djxl`,
  commonly named `jpegxl-tools` or `libjxl-tools` depending on the release.
- Windows: install `libjxl` tools from an official binary, MSYS2, vcpkg, or a
  source build, then point `DICOM_GO_DJXL` at `djxl.exe` if it is not on `PATH`.

`DICOM_GO_DJXL` takes precedence over `PATH` lookup:

```sh
DICOM_GO_DJXL=/opt/libjxl/bin/djxl go test -tags jpegxl_djxl ./...
```

## Registration

Registration is explicit:

```go
import jpegxlcodec "github.com/ThalesMMS/dicom-go/examples/codec-adapters/jpegxl"

if err := jpegxlcodec.RegisterDefault(); err != nil {
	panic(err)
}
```

Build the importing application with the backend tag when actual decoding is
required:

```sh
go build -tags jpegxl_djxl ./...
```

Without `-tags jpegxl_djxl`, registration is still possible, but decoding
returns `ErrDjxlUnavailable`. If the tag is present and `djxl` is absent,
non-executable, or cannot be launched, decoding also returns
`ErrDjxlUnavailable`.

## License and dependency notes

The adapter code follows the `dicom-go` repository license. `libjxl` is the JPEG
XL reference implementation and is BSD-3-Clause with an upstream patent grant.
This module invokes `djxl` as a runtime executable; it does not link or vendor
`libjxl`, and the base `dicom-go` module does not import this adapter.

## Unsupported behavior

This adapter does not encode, transcode, stream JPIP/media payloads, or register
itself by default. It rejects signed pixel metadata, planar RGB, unsupported
photometric interpretations, unsupported bit allocation, and fragment layouts
that cannot be mapped to complete still-image frames.

## Tests

Run the default boundary tests directly:

```sh
go test ./...
```

Run the `djxl` backend tests:

```sh
CGO_ENABLED=0 go test -tags jpegxl_djxl ./...
```

Run the full local JPEG XL fixture corpus when `../JPEGXL-Fixture` is available:

```sh
DICOMGO_JPEGXL_FULL=1 CGO_ENABLED=0 go test -tags jpegxl_djxl ./...
```

From the `dicom-go-dev` root:

```sh
make codec-jpegxl-djxl-check
```
