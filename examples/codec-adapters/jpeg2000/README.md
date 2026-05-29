# JPEG 2000 / HTJ2K optional adapter

This nested module provides the supported opt-in JPEG 2000 / HTJ2K pixel data
adapter for `dicom-go`. It stays outside the base module so applications that do
not decode JPEG 2000 do not inherit the JPEG 2000 dependency.

The adapter uses `github.com/mrjoshuak/go-jpeg2000` as a pure Go dependency. No
CGO, native library, or adapter-specific build tag is required by this module.
Applications may still place registration behind their own build tag when they
want default builds to avoid the optional dependency path.

Supported boundaries:

- registers JPEG 2000, JPEG 2000 Part 2, and HTJ2K still-image transfer syntax
  UIDs explicitly;
- excludes JPIP and streaming transfer syntaxes;
- validates common DICOM pixel metadata and codestream metadata before decode;
- supports one fragment per frame, plus multi-fragment assembly for a single
  frame;
- returns interleaved native frame bytes for supported 8-bit and 16-bit unsigned
  monochrome/RGB data;
- returns typed errors for unsupported metadata, unsupported fragment layouts,
  size mismatches, and malformed codestreams.

The profile decision and current benchmark evidence are documented in
[PRODUCTION_PROFILE.md](./PRODUCTION_PROFILE.md). In short, this module is the
supported dependency-free opt-in JPEG 2000 / HTJ2K adapter for the controlled
fixture-backed profile, but it is not enough by itself for the clinical
`codecfull` profile. That qualified profile combines this adapter boundary with
OpenJPEG for JPEG 2000 Part 1/2 and OpenJPH for HTJ2K, and validates external
clinical fixtures against independent references.

The OpenJPEG fallback profile is documented in
[OPENJPEG_PROFILE.md](./OPENJPEG_PROFILE.md). Build with `-tags
jpeg2000_openjpeg` or call `RegisterOpenJPEG` / `RegisterOpenJPEGDefault` to use
that profile. It registers OpenJPEG for JPEG 2000 Part 1 transfer syntaxes,
keeps JPEG 2000 Part 2 and HTJ2K on the pure-Go fallback path, requires
OpenJPEG's `opj_decompress` executable, and keeps the default `jpeg2000`
pure-Go path unchanged.

Registration is explicit:

```go
import jpeg2000codec "github.com/ThalesMMS/dicom-go/examples/codec-adapters/jpeg2000"

if err := jpeg2000codec.RegisterDefault(); err != nil {
	panic(err)
}
```

Run the adapter tests directly:

```sh
go test ./...
go test -bench BenchmarkDecodeJPEG2000Profile -benchmem ./...
go test -tags jpeg2000_openjpeg ./...
```
