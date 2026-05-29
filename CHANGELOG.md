# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project uses Go module semantic version tags.

## [Unreleased]

### Added

- `video`: bounded, context-aware inspection and streaming extraction for DICOM
  MPEG, H.264, and HEVC transfer syntaxes, including fragment/container
  validation and deterministic frame timing metadata for application-owned
  native media backends.
- `gsps`: image/frame-specific Displayed Area selection with
  SCALE TO FIT/TRUE SIZE/MAGNIFY calibration and complete rectangular,
  circular, polygonal, and bitmap Display Shutter read/write/apply support,
  including strict malformed/unknown-field errors and exact P-Value, CIELab,
  and overlay-data preservation.
- `pixeldata/display`: a staged grayscale display pipeline (`Pipeline` /
  `RenderGray`) implementing the DICOM modality and VOI transforms — stored
  pixel extraction, Rescale Slope/Intercept, Modality LUT Sequence, Window
  Center/Width with LINEAR/LINEAR_EXACT/SIGMOID functions, and VOI LUT Sequence
  — with `*FromObject` extractors and invalid-LUT-descriptor reporting.
- `pixeldata/display` presentation stage: a `Presentation` transform modeled
  after the modality/VOI transforms covering MONOCHROME1 / Presentation LUT
  Shape inversion, DICOM overlays (groups 0x6000-0x601E) burn-in, and
  rectangular/circular/polygonal/bitmap display shutters with intersection
  semantics, plus Presentation LUT Sequence application. GSPS interpretation
  returns an explicit `ErrUnsupportedGSPS` limitation.
- `pixeldata/display` color path: `RenderColor` converts PALETTE COLOR (via
  red/green/blue palette LUTs), YBR_FULL and YBR_FULL_422, and RGB (interleaved
  or planar) to RGBA, with `PaletteFromObject` and a metadata-only
  `ColorMetadataFromObject` (Color Space and raw ICC Profile, no color
  management). Segmented palette LUTs are expanded from discrete, linear, and
  indirect segments. Unsupported photometric/layout cases return typed errors
  instead of misleading images, preserving the lightweight default dependency
  policy.
- `pixeldata/display`: `VOILUT.WindowByte` (window a single pre-sampled modality
  value) and `DecodeModality` (extract stored pixels to modality values for
  callers that re-window the same frame, such as render caches).
- `sr`: typed DICOM Structured Reporting primitives for Key Object Selection
  (KOS) and Basic Text SR — document metadata, the content-item tree
  (CONTAINER/TEXT/CODE/IMAGE/NUM), coded entries, image references, and numeric
  measurements — with dataset encode (`Document.Dataset`) and decode
  (`ReadDocument`) for synthetic round-trips.
- DIMSE Query/Retrieve: Study Root C-MOVE SCU implementation with status handling
  and a small `dicom-go-retrieve` CLI for interoperability testing.
- DIMSE Storage Commitment Push Model primitives: N-ACTION and N-EVENT-REPORT,
  including status classification helpers and transaction UID correlation helper.
- Documentation: Query/Retrieve and Storage Commitment design notes, conformance
  scope updates, and Orthanc interop setup guide.

### Changed

- `net/dicomweb`: `TokenManager.Logout` is terminal. Subsequent `Token` calls
  return `ErrBearerTokenUnavailable` and cannot reacquire credentials; callers
  must construct a new manager to authenticate again.
- `pixeldata/frame` grayscale rendering now delegates to `pixeldata/display`,
  removing the duplicated windowing/modality math while preserving output.
- `render`: VR transfer-function colors are now linearized from sRGB before DVR
  compositing and converted back to sRGB for display output; STL iso-surface
  export documents the intentional watertight voxel-boundary mesh policy instead
  of smoothing with marching cubes.
- Conformance and README limitations updated to reflect newly added DIMSE
  capabilities and remaining non-goals.

### Fixed

- `deid`: explicit burned-in pixel redaction now supports RGB native pixel data
  with `PlanarConfiguration=1`, masking the requested rectangle in each color
  plane instead of rejecting planar images.

## [0.1.0] - 2026-04-24

Initial public release of `dicom-go`.

### Added

- M0: repository architecture, package boundaries, Makefile quality targets and
  contribution baseline.
- M1: dataset parser support for native transfer syntaxes, sequences, items,
  fragments and defensive read limits.
- M2: writer support for raw elements, datasets and Part 10 files.
- M3: generated standard tag dictionary and standard UID registry.
- M4: native uncompressed pixel data metadata and frame extraction.
- M5: DICOM Upper Layer PDU codec and association negotiation.
- M6: DICOM JSON marshal/unmarshal support.
- M7: minimal DIMSE C-ECHO and C-STORE support with SCU/SCP helpers.
- M8: user documentation, runnable examples, conformance scope and release
  notes.
- Part 10 file read/write through the `object` package.
- Native transfer syntax support for Implicit VR Little Endian, Explicit VR
  Little Endian and Explicit VR Big Endian.
- Encapsulated Pixel Data fragment parsing/preservation.
- Optional DICOM RLE Lossless codec in `pixeldata/rle`.
- CLI tools: `dcmdump`, `echoscu`, `echoscp`, `storescu`, `storescp`,
  `dicomuidgen`, and dictionary generation tools.
- Runnable examples under `examples/`.

### Known Limitations

- No claim of full DICOM conformance.
- No TLS or user identity negotiation.
- Does not include C-FIND, C-MOVE, C-GET, N-* services or Storage Commitment.
- No automatic transcoding.
- Not supported: JPEG, JPEG-LS, JPEG 2000, JPEG XL, MPEG or HEVC pixel decoding.
- Parser limits are opt-in and should be configured for untrusted input.
