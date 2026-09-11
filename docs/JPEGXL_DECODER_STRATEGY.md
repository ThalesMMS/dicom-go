# JPEG XL Decoder Strategy

Date: 2026-06-22

Issue: [#137](https://github.com/ThalesMMS/go-dev/issues/137)

## Decision

Do not add a default JPEG XL decoder adapter to `dicom-go`.

The default profile remains metadata/payload-readable only for JPEG XL transfer
syntaxes. JPEG XL frame decoding is available only through the nested optional
`examples/codec-adapters/jpegxl` module and its explicit `jpegxl_djxl` build
tag.

The repository now carries a minimal registered-codec proof through
`pixeldata.Codec`: `codecfixture.DependencyUnavailableJPEGXL()` registers a
JPEG XL codec boundary for a synthetic JPEG XL payload and intentionally returns
`codecfixture.ErrDependencyUnavailable`. That proves the adapter shape and
fallback behavior without adding `libjxl`, WASM, CGO or native dependencies to
the base module. The #155 optional adapter extends that boundary by invoking
the `djxl` executable at runtime and returning
`jpegxladapter.ErrDjxlUnavailable` when the runtime is absent.

## Candidate Comparison

| Candidate | License | Maintenance | Platform support | Build complexity | Performance expectation | Dependency size | DICOM JPEG XL fit | Decision |
|---|---|---|---|---|---|---|---|---|
| No default decoder; keep explicit boundary | Repository-only | Fully under local control | All current platforms | None | No default decode path | None | Correct for preserving encapsulated Pixel Data and surfacing typed unavailable/missing codec diagnostics | Selected for the default profile in #137 |
| `github.com/gen2brain/jpegxl` | MIT | Small Go-facing wrapper around `libjxl`; latest `v0.5.0` published 2026-06-21 | pkg.go.dev renders linux/amd64, windows/amd64, darwin/amd64 and js/wasm docs | Pulls `purego`, `wazero`, embedded WASM path and dynamic/shared library probing | WASM fallback likely slower than native; dynamic path depends on installed `libjxl` | Larger than current optional adapters; includes WASM/runtime concerns | Promising API (`Decode`, `DecodeConfig`), but latest module declares `go 1.25.0`, above `dicom-go`'s `go 1.22` contract | Revisit when module target and fixture evidence fit policy |
| Native `libjxl` / `djxl` | BSD-3-Clause plus upstream patent grant | Reference implementation for JPEG XL | Upstream documents Linux packages, Homebrew, Windows binaries, Debian/Ubuntu packages; MSYS2 and vcpkg expose current packages | Native C++ runtime, CLI or shared-library packaging, platform-specific dependency audit | Best expected correctness/performance path | Heavy: Brotli, Highway, lcms, JPEG/PNG/OpenEXR-related packaging depending on distribution | Strongest codec conformance candidate; the optional adapter keeps it out of default builds and validates DICOM frame conversion through focused tests | Selected for the explicit `jpegxl_djxl` optional profile |
| Custom cgo binding to `libjxl` | BSD-3-Clause upstream, local binding TBD | Local maintenance burden | Same native platforms as `libjxl` if built and packaged | Highest: cgo, headers, linker flags, CI matrix, binary distribution | Potentially best runtime path | Same native dependency set plus cgo toolchain | Could expose exact metadata, but not justified before CLI/shared-library evidence | Reject for now |

## Source Evidence

- `libjxl` identifies itself as the JPEG XL reference encoder/decoder and
  states that JPEG XL was standardized as ISO/IEC 18181 in 2022:
  <https://github.com/libjxl/libjxl>.
- `libjxl` is BSD-3-Clause with an additional patent grant:
  <https://github.com/libjxl/libjxl/blob/main/LICENSE> and
  <https://github.com/libjxl/libjxl/blob/main/PATENTS>.
- The upstream README documents `djxl` decode usage plus Linux, Homebrew,
  Windows binary and Debian/Ubuntu packaging paths:
  <https://github.com/libjxl/libjxl>.
- `github.com/gen2brain/jpegxl` documents a CGo-free wrapper using `libjxl`
  compiled to WASM with `wazero`, trying a dynamic/shared library through
  `purego` first and falling back to WASM:
  <https://github.com/gen2brain/jpegxl>.
- `github.com/gen2brain/jpegxl@v0.5.0` currently declares `go 1.25.0`; the
  `dicom-go` module declares `go 1.22`.
- Homebrew reports `jpeg-xl` 0.11.2 as BSD-3-Clause with dependencies on
  `brotli`, `giflib`, `highway`, `imath`, `jpeg-turbo`, `libpng`,
  `little-cms2` and `openexr`.
- MSYS2 reports `mingw-w64-x86_64-libjxl` 0.11.2-4 as BSD-3-Clause and lists
  runtime dependencies including Brotli, Highway, lcms2, libjpeg-turbo, libpng
  and OpenEXR: <https://packages.msys2.org/packages/mingw-w64-x86_64-libjxl>.
- vcpkg reports `libjxl` 0.11.2 as BSD-3-Clause, with core dependencies on
  Brotli, Highway and lcms plus optional tool dependencies:
  <https://vcpkg.io/en/package/libjxl.html>.

## Build Profile

The default profile is still a boundary, not a decoder:

- `default`: no JPEG XL decoder dependency; recognized JPEG XL transfer
  syntaxes preserve metadata and encapsulated Pixel Data and report a clear
  missing optional adapter diagnostic when rendering is requested.
- `jpegxl_djxl`: nested optional module with an explicit build tag; registers
  only the three JPEG XL still-image transfer syntaxes; requires `djxl` at
  runtime; must not enter the base module; returns typed
  dependency-unavailable errors when `djxl` is absent.
- `codecfull`: includes the qualified `djxl` backend with deterministic
  lossless/lossy/JPEG-recompression fixtures, percentile/memory evidence, and
  fail-closed runtime/package gates documented in `CODECFULL_PROFILE.md`.

## Follow-Up Scope

The native `djxl` option from `#155` is implemented as a nested optional module.
Issue `#924` adds an opt-in supervised helper process that speaks a private
versioned stdio protocol (`JXLH` v1) and uses the public libjxl C API. The
helper is not a `djxl` server mode, is not an HTTP service, and must not enter
the base module. Existing `Register()` callers keep the CLI decoder; Twin owns
helper lifecycle, memory admission, and packaging when `jpegxl-helper` is
present. Fallback to `djxl` is explicit and pixel-equivalent.

Future work should remain explicit and should not change the default module
dependency contract.

1. A `gen2brain/jpegxl` adapter only if `dicom-go` raises its module target or
   the dependency offers a compatible release, and only after the adapter maps
   `image.Image` output to DICOM native frames with tests for all three DICOM
   JPEG XL transfer syntaxes.
2. A native shared-library `libjxl` adapter only if packaging owners accept the
   transitive runtime dependency set and CI can exercise that tagged profile.

JPEG XL must be described as renderable only when an application explicitly
imports and registers this optional module, builds with `jpegxl_djxl` or the
qualified `codecfull` profile, and passes preflight with a working `djxl`
runtime.
