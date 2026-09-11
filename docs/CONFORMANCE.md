# dicom-go v0.1.0 capability scope

This document describes the implemented capabilities of `dicom-go` `v0.1.0`.
It is not a formal DICOM Conformance Statement and must not be read as a claim
of complete DICOM conformance.

## Non-patient Query/Retrieve

The opt-in [Hanging Protocol and Color Palette profiles](NON_PATIENT_QR.md)
reuse streaming C-FIND, AsyncSession and existing C-MOVE/C-GET suboperations.
They validate model-specific keys, UID-only retrieval, storage roles and selected
identity without patient hierarchy. The documented key/charset subsets and
synthetic independent pynetdicom evidence do not qualify complete IODs or all
optional service-class behavior.

## Private dictionaries

Optional caller-owned [private creator catalogs](PRIVATE_CREATORS.md) resolve
implicit VRs and inspection metadata by creator, group and block-relative offset.
Each dataset/item reserves its own blocks; explicit VRs remain authoritative and
unverified values remain opaque. Reservation helpers avoid collisions and report
invalid or ambiguous creators. No vendor catalog or de-identification retention
policy is implied by this capability.

The separate opt-in [curated catalog](CURATED_PRIVATE_CATALOG.md) supplies 21
reviewed DCMTK 3.6.9 definitions for GE CT, Siemens CSA headers and two explicit
Philips MR creator spellings. Each carries its pinned source, hashes and license.
This qualifies dictionary resolution, not manufacturer validation, CSA blob
interpretation or permission to retain private data. Unknown values keep their
existing fallback; the default parser does not import this package.

## Explicit C-STORE transcode sources

The receiving CLIs separately enforce [instance persistence](INSTANCE_PERSISTENCE.md):
canonical matching SOP identity, descriptor-relative no-follow access, complete
file publication without replacement, cancellation and redacted storage logs.
Windows and Linux tests qualify the documented trusted-directory model; this
does not add peer authentication or protection against privileged local mutation.

An optional [StoreSession source adapter](CSTORE_TRANSCODE_SOURCE.md) prepares
requested representations through the existing transcoder before offering them.
The default source behavior remains unchanged. Bounded preparation, source
revalidation, explicit lossy identity and value-free reports are separate from
negotiation and delivery. Independent peers qualify the documented RLE/JPEG-LS
lossless and synthetic JPEG Baseline profiles; unavailable runtimes do not create
codec offers. Pipeline refusal and unknown-outcome handling match the confirmed
C-STORE service semantics.

## Embedded DICOMweb server scope

`net/dicomweb` includes both the existing neutral client and a new embeddable,
deny-by-default server handler. The server profile covers all six native QIDO
search resources with bounded DICOM JSON or negotiated Native DICOM Model XML
queries, WADO study/series/instance
objects and metadata plus frames and same-origin bulk data, and STOW at
collection or study scope using either multipart Part 10 or DICOM JSON/Native
XML metadata with request-local uncompressed bulk data. An injected
`dicomweb.Renderer` provides
study, series, instance, selected-frame rendered representations and thumbnails
with bounded JPEG/PNG output. WADO object payloads and STOW requests are
streamed/staged without whole-study payload buffering; storage conflict and
idempotency decisions remain atomic backend responsibilities.

The current server profile uses DICOM JSON by default and supports negotiated
multipart Native DICOM Model XML for QIDO and WADO metadata. STOW accepts Part
10 instances or metadata-based payloads whose `BulkDataURI` values match unique
`Content-Location` parts in the same request. The metadata path currently
supports uncompressed `application/octet-stream` in Explicit VR Little Endian;
compressed bulk transformation is not implemented. HTTP Range, WADO-URI,
DELETE, and implicit transcoding are also outside this profile. Unsupported
negotiation fails
explicitly and does not claim complete Studies Service conformance. See
[`DICOMWEB_SERVER.md`](DICOMWEB_SERVER.md) for endpoints, status behavior,
limits, security defaults, ownership, and interop gates.

## Supported Transfer Syntaxes

Top-level JPEG/JPEG-LS Pixel Data also has an opt-in
[incremental encoded-frame API](ENCAPSULATED_FRAME_STREAMING.md). It delivers
complete frames through the existing parser, with bounded Item assembly,
cancelable backpressure, explicit payload discard and separate seekable deferred
roundtrip. Native frame streaming keeps nested Pixel Data separate from the main
image. Encoded streaming does not qualify other compressed syntaxes or progressive
pixel decoding.

Dataset parsing/writing is supported for:

| Transfer Syntax | UID | Scope |
|---|---|---|
| Implicit VR Little Endian | `1.2.840.10008.1.2` | Native dataset read/write. |
| Explicit VR Little Endian | `1.2.840.10008.1.2.1` | Native dataset read/write. |
| Deflated Explicit VR Little Endian | `1.2.840.10008.1.2.1.99` | Part 10 and raw dataset read/write with raw Deflate applied to the dataset stream. Deferred value replay is not available while inflating. |
| Explicit VR Big Endian | `1.2.840.10008.1.2.2` | Native dataset read/write. |
| Encapsulated Uncompressed Explicit VR Little Endian | `1.2.840.10008.1.2.1.98` | Encapsulated fragment parsing/preservation and frame assembly into native bytes without a pixel codec. |
| JPEG-LS Lossless | `1.2.840.10008.1.2.4.80` | Builtin pure-Go still-image decode and explicitly selected pure-Go encode for the qualified NEAR=0 subset: ILV=0 decode/encode and unsigned RGB ILV=1/2 decode. |
| JPEG-LS Near-Lossless | `1.2.840.10008.1.2.4.81` | Builtin pure-Go decode for [qualified unsigned profiles](JPEGLS_NEAR_LOSSLESS.md); `.80` remains strictly lossless. |
| MPEG-2, MPEG-4 AVC/H.264 and HEVC/H.265 video media payloads | `1.2.840.10008.1.2.4.100` through `1.2.840.10008.1.2.4.108` | Metadata and encapsulated Pixel Data preservation only; no local decode, render or transcode. |
| JPEG XL still-image payloads | `1.2.840.10008.1.2.4.110` through `1.2.840.10008.1.2.4.112` | Metadata and encapsulated Pixel Data preservation only; no default decoder adapter, render or transcode. |

Part 10 File Meta Information is read and written as Explicit VR Little Endian,
as required by the file format.

Malformed legacy files and raw datasets may be inspected through a dedicated,
explicitly opt-in Transfer Syntax recovery API. The default reader remains
strict. Recovery evaluates only the three native uncompressed syntaxes by
default, requires confidence and runner-up margins, preserves the original File
Meta, and returns value-free diagnostics. It never infers compressed or
deflated payloads. See
[`TRANSFER_SYNTAX_RECOVERY.md`](TRANSFER_SYNTAX_RECOVERY.md).

Structural/value-representation validation and parser/writer lifecycle hooks
are available only through dedicated opt-in APIs. Existing readers and writers
do not enable them automatically. The engine covers VR/VM, dictionary and
selected cross-field rules, but does not claim complete IOD/module conformance.
See [`VALIDATION.md`](VALIDATION.md).

## Unsupported Transfer Syntaxes

The following transfer syntax families are not decoded or transcoded in
`v0.1.0`:

- Other deflated syntaxes, including JPIP-referenced deflate and deflated
  image-frame compression.
- JPEG Extended and JPEG Lossless variants outside the documented
  [Pixel Data Codecs](#pixel-data-codecs) limits.
- JPEG-LS profiles outside the documented builtin Lossless and Near-Lossless
  subsets, including restart-marker streams, unless an explicitly selected
  optional backend supports the requested mode. Metadata and encapsulated
  Pixel Data can be preserved when the dataset is otherwise readable.
- JPEG 2000 and HTJ2K frame decoding unless the optional
  `examples/codec-adapters/jpeg2000` module is registered. Metadata and
  encapsulated Pixel Data can be preserved when the dataset is otherwise
  readable.
- JPEG XL frame decoding and transcoding. Metadata and encapsulated Pixel Data
  can be preserved when the dataset is otherwise readable, but the current
  decoder strategy keeps JPEG XL as an explicit non-default boundary; see
  [`JPEGXL_DECODER_STRATEGY.md`](JPEGXL_DECODER_STRATEGY.md).
- MPEG, MPEG-4 AVC/H.264 and HEVC/H.265.
- JPIP-referenced syntaxes.
- SMPTE ST 2110 video/audio transfer syntaxes.

The registry may recognize these UIDs, but recognition does not imply decode
support.

See [CAPABILITIES.md](CAPABILITIES.md) for the current repository inventory and
the [outcome dependency model](ROADMAP.md#outcome-dependency-model) for stable
sequencing rules. Executable work remains in the
[live GitHub queue](https://github.com/ThalesMMS/go-dev/issues?q=is%3Aissue%20state%3Aopen).

## Headless Render and ROI Scope

`dicom-go` now exposes application-independent clinical viewer primitives:

- `render`: 2D frame rendering, bounded PNG and 8-bit JPEG Baseline encoding,
  crop-before-scale aspect-preserving viewport rendering, pixel sampling, auto-window,
  stack geometry, volume construction, MPR/MIP/slab, oblique reslice, VR and
  CPR. Fused MPR accepts a secondary volume and explicit target-to-source
  mapper, reduces both scalar layers over the same physical slab extent, and
  applies VOI, palette, and alpha only after sampling. Mapping failure is
  atomic; domain coverage is reported and a wholly out-of-domain overlay
  returns `ErrFusionNoOverlap`.
- `roi`: raster masks, vector ROI rasterization, measurements, segmentation
  operations, 2D statistics and volumetric statistics.
- `object.FrameGeometryAt`: frame-specific Image Position/Orientation and Pixel
  Measures resolved from Shared and Per-Frame Functional Groups, with legacy
  top-level Image Plane attributes retained as fallbacks.

These packages are library computation surfaces only. They do not implement UI,
mouse interactions, overlays, menus, local viewer preferences, catalog
integration or persistence. SEG, SR, GSPS and RTSTRUCT serialization cover the
focused application data models exposed by the library; volumetric presentation
states and full presentation-pipeline application remain outside this phase.
RTSTRUCT rasterization accepts `CLOSED_PLANAR` contours with union semantics and
homogeneous `CLOSEDPLANAR_XOR` contours with symmetric-difference semantics.
`POINT`, open contours, and private geometric types return
`ErrUnsupportedContourType` instead of disappearing silently.

`rtdose` parses native 16/32-bit RT Dose grids, applies Dose Grid Scaling,
retains dose units/type/summation and RT references, validates relative or
absolute Grid Frame Offset Vector geometry, samples registered patient-space
points, and exposes embedded or computed cumulative DVHs. Computed DVHs
rasterize RTSTRUCT contours on the dose grid, preserve XOR holes, account for
anisotropic/non-uniform voxel volume, report partial grid overlap, and are
cancellable and caller-bounded by voxel/bin limits. Encapsulated dose pixels,
ambiguous geometry, mismatched Frame of Reference UIDs, and unsupported dose
semantics fail closed. Only `GY` plus `PHYSICAL` is labelled physical dose;
relative and effective/biological values remain explicit.

`spatialreg` parses Spatial Registration Storage
(`1.2.840.10008.5.1.4.1.1.66.1`) and Deformable Spatial Registration Storage
(`1.2.840.10008.5.1.4.1.1.66.3`). Matrix registrations compose Matrix Sequence
items in DICOM order, validate RIGID, RIGID_SCALE, or AFFINE constraints, reject
singular or ill-conditioned transforms, and name Source-to-Registered and
Registered-to-Source directions explicitly. Point, plane, and fixed-size
volume-extent mapping preserve affine scale and shear. Registered-coordinate
sampling delegates through the validated inverse to `render.VolumeReader`, so
the source `VolumeStore` remains the memory-budget authority and no transformed
voxel copy is materialized. Deformable registration uses only the normative
registered-to-source direction. It applies optional pre-matrix,
trilinearly interpolated Vector Grid Data displacement, and optional post-matrix
in that order. Grid dimensions,
byte length, orientation, origin, anisotropic resolution, endian, and
homogeneous matrices are validated before decoded-vector allocation. Points
outside the grid, triple-NaN neighborhoods, partial NaNs, non-finite values,
and malformed lengths fail explicitly. `MaxDeformableVectorGridBytes` rejects
oversized OF values in nested sequences before parser allocation, while
`spatialreg.Options` adds bounded defaults for bytes, voxels, and registration
items. Both Spatial Registration SOP Classes are in the default DIMSE Storage
profile. The package neither computes registration fields nor promises a
generic inverse deformation.

`parametricmap` parses Parametric Map Storage with native integer Pixel Data,
Float Pixel Data, or Double Float Pixel Data. It resolves shared and per-frame
geometry, Real World Value Mapping, quantity definitions, UCUM units, source
references, and supported spatial dimension indices. Calibrated frames are
decoded lazily into a bounded LRU cache and can be sampled or resampled in
patient space and measured through raster ROI statistics. Missing mappings,
non-finite values, unsupported non-spatial dimensions, inconsistent geometry,
and registration mismatches fail explicitly. Quantitative values are returned
with their units and are not passed through ordinary image VOI transforms.

`dynamic` extracts a frame-level temporal model without decoding Pixel Data. It
keeps temporal position, stack identity and in-stack spatial position as
independent coordinates and reads them from enhanced functional groups,
Dimension Index Sequence / Dimension Index Values and legacy temporal
attributes. Timing can use Frame Acquisition DateTime, Frame Reference Time,
Trigger Time / Nominal Cardiac Trigger Delay, Actual Frame Duration, Frame Time
or Frame Time Vector. The resulting timeline preserves irregular offsets,
gated phase metadata and multiple stacks. When explicit temporal indices are
missing, repeated spatial positions can be grouped by acquisition time; an
ordinary spatial CT stack with unique positions is not classified as dynamic
merely because its slices have different acquisition times. This package does
not schedule playback, decode frames, perform motion correction, register
stacks or infer clinical timing absent from the dataset.

## Structured Report reference and template scope

`sr` preserves structural by-reference Content Items, resolves forward and
backward paths into an immutable navigation index, validates known SOP Class
relationship restrictions, and writes reference slots without by-value macros.
Basic Text SR, Enhanced SR, and Key Object Selection profiles prohibit
by-reference relationships. The implemented Comprehensive, Comprehensive 3D,
and Extensible SR checks permit only their documented relationship subset.

Reference resolution and template validation are explicit operations with
strict or warning modes and caller-controlled resource limits. Generic
`ReadDocument` remains validation-compatible with existing callers, while
generic document serialization rejects an invalid by-reference graph rather
than emitting it. Built-in diagnostics describe only structural paths, stable
codes, and value-free messages.

Template and context-group definitions are not bundled. The caller supplies
explicitly versioned definitions to an immutable registry; successful
validation applies only to those definitions and is not a general DICOM
conformance claim. The focused `MeasurementReport` API is an internal exchange
profile, not a complete implementation or validator of TID 1500 or TID 1501.
See [`SR_TEMPLATES_AND_REFERENCES.md`](SR_TEMPLATES_AND_REFERENCES.md) for the
profile matrix, limits, PHI-safe diagnostic boundary, and independent-fixture
interop gates.

## DICOMDIR and De-identification Scope

`dicom-go` exposes small reusable object utilities:

- `dicomdir`: extracts existing directory-record references and provides a
  bounded IMAGE file-set authoring profile with strict portable File IDs,
  two-pass offsets, source revalidation, read-back, and atomic publication.
- `deid`: provides a data-driven PS3.15 2026b Basic Application
  Confidentiality Profile planner, explicit profile options, recursive action
  processing, stable UID remapping, injected temporal shifting, verified
  safe-private rules, redacted reports, and caller-attested pixel/visual
  cleaning. The earlier simple anonymization API remains as a compatibility
  helper.

These are not full application workflows. `dicomdir` does not copy or rename
source files, manage catalogs, or author non-IMAGE leaf schemas. Its broader
record classifier is capability inventory only; unsupported leaf types fail
closed. See [`DICOM_FILE_SETS.md`](DICOM_FILE_SETS.md). The strict `deid`
planner implements the
versioned Table E.1-1 action surface and, when selected, the concept- and
Value-Type-specific Table E.3.4-1 Clean Structured Content actions. A
single-file de-identification plan rejects DICOMDIR because its directory
records and referenced media require one application-level transaction.
Output still requires IOD-, media-, and deployment-specific validation before
any conformance claim. It does not
perform automated PHI detection, OCR, or automatic burned-in pixel PHI
detection. Pixel and recognizable-visual-feature options require explicit
caller-owned cleaners and are only attested after those callbacks succeed. See
[`DEIDENTIFICATION.md`](DEIDENTIFICATION.md).

### Video, JPIP and Streaming Transfer Syntax Policy

`v0.1.0` treats video, JPIP and streaming-oriented syntaxes as separate from
the still-image pixel codec path. Dataset metadata can be parsed when the
underlying dataset transfer encoding is otherwise readable, and encapsulated
Pixel Data fragments can be preserved as bytes. The library offers bounded
application-owned extraction/retrieval adapters, but it does not include a
video player, transcode these media families, inflate Deflated Image Frame
Compression, or handle SMPTE ST 2110 media.

| Family | Policy | Expected behavior |
|---|---|---|
| MPEG-2, MPEG-4 AVC/H.264 and HEVC/H.265 video | Recognized as encapsulated media payload transfer syntax UIDs; `video` provides bounded, context-aware validation and streaming extraction, but no built-in video decoder or player. | Applications can hand the unchanged stream to an owned native media backend. `pixeldata.DecodeFrames` still returns a typed non-renderable media error because video is not a still-image codec. |
| JPIP Referenced and JPIP HTJ2K Referenced, including deflated variants | `jpip.Client` performs cancellable HTTP(S) retrieval under an explicit host/scheme allowlist, exact-origin credential policy, redirect revalidation, bounded retries/response size, and bounded representation LRU. | Complete `image/jp2`, `image/jph`, and `image/jphc` responses decode through the registered JPEG 2000/HTJ2K codec. JPP/JPT data-bin streams, denied origins, partial/corrupt/oversized responses, offline endpoints, and codec failures return typed errors. |
| Deflated Image Frame Compression | Streaming/image-frame compression is not implemented; this is separate from Deflated Explicit VR Little Endian dataset support. | The transfer syntax remains unsupported for frame extraction and should not be described as covered by dataset deflate support. |
| SMPTE ST 2110 video/audio | Recognized as media transport syntax; no active-video or PCM audio decoding/playback. | Treat as a media non-goal for `v0.1.0`; parse metadata where possible and preserve values, but do not attempt frame rendering. |

JPP/JPT data-bin assembly and broader streaming-media decoding remain outside
this profile. Current rendering and command-line boundaries are documented in
their focused capability and command documents.

### Physiologic Waveform Profile

The `waveform` package parses 12-lead, general, ambulatory and 32-bit ECG,
hemodynamic, cardiac-electrophysiology, arterial-pulse, respiratory, routine
EEG, EMG, EOG, sleep-EEG, and body-position Waveform Storage. It preserves
multiplex groups, encoded channel order, source codes, timing, units,
sensitivity/correction/baseline calibration, padding, and supplied textual,
coded, numeric, and temporal annotations. The integer decoder recognizes `SB`,
`UB`, `SS`, `US`, `SL`, `UL`, `SV`, and `UV` in either DICOM byte order; a
Waveform Storage group is rendered only when its SOP Class permits the encoded
sample interpretation (`SB`, `UB`, `SS`, or `SL` in the supported physiologic
IODs). Other combinations remain an explicit raw fallback. Incomplete
calibration remains explicitly raw; the package performs no lead inference or
diagnostic interpretation.

Waveform Data can be deferred even when nested inside Waveform Sequence items.
`object.ReadFileOptions.DeferWaveformData` records each value location in
source order together with its enclosing item identity for seekable,
non-deflated sources. Writing a data set that still contains nested deferred
values is rejected with `object.ErrDeferredSequenceValueWrite`; callers must
retain the source and materialize those values before rewriting the object.
Applications may supply bounded `ReaderAt` sources to `waveform.Open`; the
lazy multiresolution min/max index preserves spikes, applies one recording-wide
memory budget, and caps each envelope query by viewport width.
Audio/companded, retired trial, industrial ultrasound-waveform, malformed, and
unsupported sample encodings return explicit raw-fallback metadata or errors.
The default DIMSE Storage SOP Class profile includes the supported physiologic
Waveform Storage UIDs.

## DIMSE Operations

For design notes and roadmap details on Query/Retrieve and Storage Commitment support, see:

- `docs/DIMSE_QR_AND_STORAGE_COMMITMENT_DESIGN.md`

Implemented:

- Asynchronous Operations Window User Information sub-item `0x53` and an
  explicit multiplexed DIMSE runtime:
  - absent negotiation preserves the synchronous `1/1` default;
  - finite and zero/unlimited asymmetric windows are encoded, decoded,
    negotiated and exposed in local endpoint orientation;
  - `dimse.AsyncSession` is the sole receive owner, correlates complete
    command/dataset messages by direction and Message ID, serializes whole
    outbound messages, and enforces invoked/performed backpressure;
  - explicit concurrent entry points cover C-ECHO, C-STORE (including
    encoded payloads via `StartCStoreEncoded`), C-FIND, C-MOVE,
    C-GET (including reverse C-STORE), and generic N-DIMSE;
  - legacy clients and SCP helpers remain synchronous and cannot be mixed with
    a session on the same association.
  - See [`ASYNCHRONOUS_OPERATIONS.md`](ASYNCHRONOUS_OPERATIONS.md).

- C-ECHO SCU/SCP.
- C-STORE SCU/SCP.
  - `net/dimse.StoreSession` performs bounded metadata-first batch planning,
    uses at most 128 unique odd Presentation Context IDs per association, and
    splits larger plans deterministically. Path payloads are passed through only
    in their original Transfer Syntax; no implicit pixel transcode is claimed.
  - The batch baseline permits one outstanding operation per association.
    Confirmed responses finish with A-RELEASE; cancellation or uncertain
    post-command transport failure uses A-ABORT and is not retried unless the
    caller explicitly opts into at-least-once semantics.
  - Pipelining is opt-in: `StoreSessionOptions.MaxInvokedOperations` greater
    than one uses exclusive `AsyncSession` plus `StartCStoreEncoded` when the
    negotiated invoked window is also greater than one. Twin-Viewer Send and
    `cmd/storescu` default to one outstanding operation. See
    [`CSTORE_SESSION.md`](CSTORE_SESSION.md) and
    [`ASYNCHRONOUS_OPERATIONS.md`](ASYNCHRONOUS_OPERATIONS.md).
  - `cmd/storescp` handles only C-STORE-RQ and C-ECHO-RQ; any other DIMSE
    command on an accepted association is logged and aborts that association.
  - `cmd/storescp` applies configurable PDU, command, dataset, element-count,
    nesting, Pixel Data, fragment, and association-time limits. Parser resource
    violations return C-STORE `0xA700` when a response can be emitted and close
    only the affected association; malformed datasets use `0xC000`.
  - Concurrent negotiations/associations, active C-STORE operations, and queued
    C-STORE operations are independently bounded. Association saturation uses
    transient provider-local A-ASSOCIATE-RJ; store-queue saturation drains the
    bounded dataset without materializing it and returns `0xA700`. SIGINT uses
    a configurable graceful shutdown deadline before aborting remaining peers.
- C-FIND SCU/SCP (Study Root and Patient Root Query/Retrieve Information Model
  wrappers).
  - Study Root query levels: `STUDY`, `SERIES`, `IMAGE`.
  - Patient Root query levels: `PATIENT`, `STUDY`, `SERIES`, `IMAGE`.
  - Status handling:
    - `0xFF00` / `0xFF01`: Pending (continue receiving matches)
    - `0x0000`: Success (stop)
    - `0xFE00`: Cancel (final non-success)
    - Any other status: treated as a failure/warning and returned as an error
      by SCU helpers.
  - SCP handler API: callers provide matching Identifier datasets; there is no
    built-in persistent database/index.
  - Matching: package `qrmatch` implements PS3.4 C.2.2 Query/Retrieve matching
    over caller-supplied values (universal, single-value with VR padding, UI
    list, VR-qualified wildcards, inclusive DA/TM/DT ranges with partial
    precision, and same-item sequence matching). Wildcards in UI/DA/TM/DT are
    not treated as patterns. Unsupported optional keys are reported instead of
    filtering the candidate set to empty. `MemoryFindIndex` is an in-memory
    Study Root C-FIND handler covering `STUDY`, `SERIES` and `IMAGE` without
    reading bulk pixel data. PN matching is case-insensitive; other VRs are
    case-sensitive. Limits bound values, sequence depth/items and wildcard
    work.
  - Concurrency: this is a minimal synchronous implementation; applications must
    not issue multiple concurrent DIMSE operations on a single association.
- Modality Worklist Information Model - FIND SCU/SCP:
  `1.2.840.10008.5.1.4.31`.
  - Typed builders preserve absent versus present-empty keys, Patient Name/ID,
    Patient Birth Date/Sex, single/wildcard PN matching, DA/TM ranges and the
    one-item Scheduled Procedure Step Sequence. A universal sequence request
    returns the complete selected item; Timezone Offset From UTC is validated
    and preserved without cross-offset conversion.
  - Specific Character Set values are validated through the library character-
    set registry. The SCU writes query `StringValue` keys under the caller's
    `(0008,0005)` declaration and interprets response text through `object`. The
    SCP decodes request keys and candidate values under their respective
    declarations before matching, validates raw provider output under the
    candidate declaration and emits `(0008,0005)` only when returned text uses
    a non-default repertoire. Missing declarations are not inferred.
  - The SCU streams pending Identifiers through a synchronous callback, sends
    C-CANCEL on local cancellation/limits and drains the final response.
  - The SCP streams provider results directly, projects only requested return
    keys and applies finite byte/element/depth/match limits before wire output.
  - The optional in-memory matcher is deterministic, case-sensitive outside PN
    and case-insensitive for supported PN literal/wildcard matching. It is not a
    RIS/HIS or persistent database.
  - Supported matching/return keys, `FF00`/`FF01` policy, limits and examples are
    listed in [`MODALITY_WORKLIST.md`](MODALITY_WORKLIST.md).
- C-MOVE SCU/SCP workflow helpers for Study Root and Patient Root Query/Retrieve
  Information Models:
  - Study Root MOVE: `1.2.840.10008.5.1.4.1.2.2.2`
  - Patient Root MOVE: `1.2.840.10008.5.1.4.1.2.1.2`
  - See `cmd/dicom-go-retrieve` and `docs/INTEROP_ORTHANC.md` for a runnable example.
  - `cmd/dicom-go-retrieve` keeps C-MOVE as its default and offers explicit
    `-method get`. C-GET proposes the default Storage SOP Classes with SCP role,
    validates each same-association C-STORE command/dataset pair, and writes
    owner-protected Part 10 files without overwriting duplicate SOP Instance
    UIDs. CLI final output distinguishes completed, warning, failed and canceled
    status classes with the peer's sub-operation counts.
  - `cmd/findscu` and `cmd/dicom-go-retrieve` select Study Root (default) or
    Patient Root explicitly. Model/level/key paths are validated before dialing,
    and command output identifies the selected information model.
  - `cmd/findscu -output jsonl` emits exactly one compact DICOM JSON object per
    pending Identifier and reserves stdout for those records. Its summary is
    optional and written to stderr. Binary values default to deterministic
    omitted-content `BulkDataURI` references or may be explicitly inlined; each
    JSONL response dataset is bounded to 16 MiB by default (configurable through
    256 MiB). The default text output is unchanged.
  - Both commands run C-FIND/C-MOVE/C-GET through `AsyncSession`. The first
    SIGINT sends one targeted C-CANCEL, drains a terminal response for a bounded
    five seconds, and attempts orderly A-RELEASE; canceled operations exit 130.
    The signal's default behavior is restored after the first SIGINT, so a
    second SIGINT forces process termination. Timeout and protocol failures exit 1.
  - Command helpers parse and emit optional Move Originator AE Title, Move
    Originator Message ID and C-MOVE response sub-operation counts when present.
  - SCP handler API: callers validate or resolve the Move Destination, resolve
    matching instances and open C-STORE sub-operations through callbacks. There
    is no built-in persistent database/index or production archive.
  - SCP responses report remaining/completed/failed/warning sub-operation
    counts and use final success, cancel, destination-unknown or processing
    failure/warning status values as appropriate.
- C-GET SCU/SCP workflow helpers for Study Root and Patient Root Query/Retrieve
  Information Models:
  - Study Root GET: `1.2.840.10008.5.1.4.1.2.2.3`
  - Patient Root GET: `1.2.840.10008.5.1.4.1.2.1.3`
  - `SendCGetWithProgress` sends C-GET-RQ, handles interleaved C-STORE-RQ
    sub-operations on the same association through a caller-provided storage
    callback and continues reading C-GET-RSP until a final response.
  - C-GET requires accepted Storage SOP Class presentation contexts with SCP role
    enabled for the requester; otherwise helpers fail with
    `ErrCGetStorageRoleNotAccepted`.
  - The helper reports C-GET response status and sub-operation counters. It is
    not a production archive.
- Patient Root Query/Retrieve Information Model helper primitives:
  - FIND: `1.2.840.10008.5.1.4.1.2.1.1`
  - MOVE: `1.2.840.10008.5.1.4.1.2.1.2`
  - GET: `1.2.840.10008.5.1.4.1.2.1.3`
  - Presentation context helpers are available for FIND/MOVE/GET.
  - Identifier builders cover Patient Root `PATIENT`, `STUDY`, `SERIES` and
    `IMAGE` levels, with common identifying and optional keys exposed through
    `QueryRetrieveRequiredKeys` and `QueryRetrieveOptionalKeys`.
  - Patient Root SCP wrappers reuse the same C-FIND/C-MOVE/C-GET callback
    shapes as Study Root. There are no built-in archive/index semantics.
- Generic normalized DIMSE services:
  - Typed request/response models and command-set parsers cover N-EVENT-REPORT,
    N-GET, N-SET, N-ACTION, N-CREATE and N-DELETE.
  - `NormalizedClient` provides context-aware SCU operations on an established
    association. It selects an accepted presentation context, correlates
    Message IDs and SOP Instance UIDs, bounds response datasets and returns
    typed warning/failure errors with optional status fields preserved.
  - `NormalizedSCPOptions` registers per-association handlers through the
    existing DIMSE dispatcher or `ServeAssociation`; missing handlers return
    Status `0x0211` without installing mutable package globals.
  - A Meta SOP Class whose accepted abstract syntax intentionally differs from
    the command SOP Class requires an explicit SCU override or SCP validation
    callback. This avoids silently accepting a command on the wrong context.
  - Command Data Set Type `0x0101` means absent; every other received value is
    treated as present, as required for interoperability.
  - The layer supplies protocol primitives rather than embedding service-class
    state. The separate `ups` and Storage Commitment workflows compose these
    primitives; MPPS and Print Management remain application-owned. See
    `examples/ndimse` for a synthetic local N-GET exchange.
- Unified Procedure Step reusable workflow:
  - Push `1.2.840.10008.5.1.4.34.6.1`, Watch `.6.2`, Pull `.6.3`, Event `.6.4`
    and Query `.6.5` presentation contexts are exposed by package `ups`.
  - Push/Pull/Watch normalized operations enforce the Annex CC state machine,
    Transaction UID ownership, required final attributes and terminal
    immutability over an injectable atomic state-plus-outbox repository.
    N-SET is restricted to mutable existing UPS attributes and effective
    no-op replay does not create a new revision or event.
    `BuildPerformedProcedure` and `BuildDiscontinuationProgress` construct the
    final-state macros used before Complete and performer Discontinue.
  - Watch implements specific/global subscribe, unsubscribe, suspend, deletion
    lock policy, durable delivery claims and bounded at-least-once callbacks.
    Filtered global subscribe materializes existing and future UPS instances
    selected by the same scalar tag registry and exact/wildcard/CS/temporal
    value matchers as UPS C-FIND. Sequence matching is rejected. Filters are
    durable across service restart and bounded to 16 keys, 16 values per key,
    4 KiB total matching-value bytes, and the configured
    `MaxSubscriptionFilterScanned` existing-step scan. Unsupported argument
    tags return `0114`, invalid arguments return `0115`, exhausted bounds return
    `0213`, and keys used with a specific or unfiltered-global UID return
    `C314`. Suspend removes future filtered inheritance while preserving
    materialized instructions; global unsubscribe removes the AE's global and
    materialized instructions without erasing committed outbox deliveries.
    Event Type 4 restart/shutdown reports use configured fallback AEs plus
    active subscribers and are queued atomically with bounded fan-out.
    Stores opt into filtered-global support with
    `FilteredGlobalSubscriptionCommitter`; stores without that atomic
    persistence/inheritance capability return `C307`.
  - Pull/Watch/Query C-FIND uses service-specific streaming routes, synchronous
    callback backpressure, C-CANCEL plus final drain, finite limits and a
    documented top-level matching/return subset. Sequence matching is not
    implemented; sequence return keys remain supported. A validated late
    C-CANCEL received after its operation completed is ignored so the
    association remains reusable.
  - `MemoryStore` is a bounded process-local reference, not a production
    worklist. Applications own durable storage, AE resolution, authorization,
    clinical scheduling, workers and audit retention. See
    [`UNIFIED_PROCEDURE_STEP.md`](UNIFIED_PROCEDURE_STEP.md).
- Storage Commitment Push Model (N-ACTION / N-EVENT-REPORT) SCU/SCP primitives
  and durable-workflow facade:
  `1.2.840.10008.1.20.1`
  - `ServeStorageCommitmentSCP` receives one N-ACTION request, parses Transaction
    UID and Referenced SOP Sequence, invokes a caller-provided handler and sends
    N-ACTION-RSP success or failure status.
  - `BuildStorageCommitmentActionInformation` /
    `ParseStorageCommitmentActionInformation` cover N-ACTION datasets.
  - `BuildStorageCommitmentEventInformation` /
    `ParseStorageCommitmentEventInformation` cover N-EVENT-REPORT result
    datasets, including Referenced SOP Sequence and Failed SOP Sequence with
    Failure Reason.
  - `StorageCommitmentTransactionTracker` remains in-memory only and correlates
    Transaction UID delivery for one process.
  - `StorageCommitmentWorkflow` adds an injectable versioned transaction store,
    expiring processing/delivery leases, exact result-partition validation,
    same-association and separate-callback delivery, bounded retry/backoff,
    duplicate suppression, listener integration, and PHI-free persisted failure
    classes. `MemoryStorageCommitmentStore` is the bounded process-local
    implementation; applications inject durable storage for restart recovery.
  - Event Type ID 1 represents complete success; Event Type ID 2 is emitted and
    accepted whenever any requested SOP Instance fails.
  - See `docs/DIMSE_QR_AND_STORAGE_COMMITMENT_DESIGN.md` and
    `docs/STORAGE_COMMITMENT_WORKFLOW.md` for ownership, persistence, delivery,
    and manual interop boundaries.

Not implemented (still out of scope / planned):

Query/Retrieve (QR):

- Retired Patient/Study Only Query/Retrieve models.
- Color Palette Query/Retrieve models.

Retired Patient/Study Only and Color Palette Query/Retrieve models are
classified as out of scope for `v0.1.0` unless a separate use case is opened.

Notes / limitations:

- C-MOVE sub-operations (C-STORE) occur on a **separate association** from the
  C-MOVE association. As an SCU, a C-STORE SCP must be running at the Move
  Destination AE. As an SCP, application callbacks are responsible for opening
  and reporting those storage sub-operations.
- The C-GET SCU helper handles C-STORE sub-operations interleaved with C-GET
  responses for that one operation. The matching Study Root and Patient Root
  SCP wrappers use caller-provided callbacks and do not provide a production
  archive.

### Status and error mapping (design)

This section defines how status codes returned by planned Query/Retrieve and
Storage Commitment services will be interpreted and surfaced.

#### Query/Retrieve (C-MOVE / C-GET)

Status is taken from **Status (0000,0900)** in each response.

The implementation will classify response status values into these buckets:

- **Pending**: operation is still in progress; more responses are expected.
  - Typical code: `0xFF00`.
- **Success**: final response indicating completion.
  - Typical code: `0x0000`.
- **Cancel**: final response indicating the operation was canceled.
  - Typical code: `0xFE00`.
- **Warning**: final response indicating completion with warnings.
- **Failure**: final response indicating the operation failed.

Retrieve responses may also include sub-operation counters (informational):

- **Number of Remaining Sub-operations** (0000,1020)
- **Number of Completed Sub-operations** (0000,1021)
- **Number of Failed Sub-operations** (0000,1022)
- **Number of Warning Sub-operations** (0000,1023)

Unknown/unrecognized status values will be treated as **Failure** for API-level
error reporting, while preserving the raw status code.

#### Storage Commitment (Push Model)

Storage Commitment uses N-ACTION to request commitment and N-EVENT-REPORT to
deliver results. Status is taken from **Status (0000,0900)**.

- **Success**: `0x0000`
- **Warning**: warning statuses (implementation-dependent)
- **Failure**: any non-zero status not classified as warning

For N-EVENT-REPORT, the returned dataset indicates per-instance success/failure.
Unknown/unrecognized status values will be treated as **Failure** for API-level
error reporting, while preserving the raw status code.

### Association negotiation and runtime behavior (design)

This section documents the intended/assumed association behavior for the planned
Query/Retrieve and Storage Commitment work.

#### AE titles

- Calling AE Title (local): configurable.
- Called AE Title (remote): configurable.
- For C-MOVE, the *Move Destination* AE Title is configurable and is sent in the
  C-MOVE request.
- C-MOVE command helpers preserve optional Move Originator AE Title and Move
  Originator Message ID when applications set them or peers send them.

#### Presentation contexts

- The SCU proposes one presentation context per SOP Class it intends to use.
- For each proposed context, the SCU proposes a list of transfer syntaxes.
- For the current scope, the default transfer syntax proposal list is:
  - Explicit VR Little Endian (`1.2.840.10008.1.2.1`)
  - Implicit VR Little Endian (`1.2.840.10008.1.2`)
- An `A-ASSOCIATE-AC` preserves every presentation-context result, including
  rejections on a partially accepted association. Accepted contexts remain
  available through `AcceptedContexts`; rejected results are exposed as
  defensive copies (`PresentationContextOutcomes`,
  `RejectedPresentationContexts`) with context ID, proposed Abstract Syntax
  UID, proposed Transfer Syntax UIDs, peer result/reason, and the accepted
  Transfer Syntax UID when the result is acceptance.
- Peer result/reason codes are inspectable without matching error text:
  user-rejection, no-reason, abstract-syntax-not-supported, and
  transfer-syntaxes-not-supported. Transfer-syntax refusals name the proposed
  UID set and do not include dataset or patient identifiers.
- When no context is accepted, `ErrNoAcceptedPresentationContexts` remains
  detectable with `errors.Is`. Details are available through
  `errors.As(*NoAcceptedPresentationContextsError)`. An SCP that cannot accept
  any proposed context still returns that typed error locally and rejects the
  association with `A-ASSOCIATE-RJ`.
- DIMSE helpers (`ExplainMissingPresentationContext`,
  `FormatPresentationContextDiagnostic`) explain a later SOP Class selection
  failure from the preserved results. Twin-Viewer Verify/Send/Retrieve surface
  those diagnostics in user-visible errors.

Notes:

- Identifiers (C-FIND/C-MOVE/C-GET request/response datasets) are encoded using
  the accepted transfer syntax for the QR presentation context.
- If multiple presentation contexts are accepted for a given SOP Class, the
  implementation uses the first accepted context.

#### Maximum PDU length

- The SCU proposes a configurable maximum PDU length.
- Defaults should remain conservative to work with common peers.

#### Timeouts

- TCP dial context and A-ASSOCIATE negotiation timeout are independently
  configurable. The negotiation timeout does not become the association's
  lifetime context.
- UL idle, read-progress, write-progress and release timeouts are configurable.
  Progress deadlines renew when bytes move and therefore permit a slow active
  transfer to exceed the configured interval.
- `dimse.SCPControls` can override command/dataset progress policy and configure
  an explicit absolute operation timeout plus cancel grace.
- DIMSE response timeout: configurable.
- For services with multiple pending responses (e.g., C-MOVE/C-GET), the timeout
  applies per response read, with an overall operation timeout configurable at a
  higher level.

#### Operation concurrency, cancellation, retry and backpressure

Current behavior:

- Legacy DIMSE SCU helpers guard one active request/response operation per
  association. A second helper operation on the same association fails with
  `ErrOperationInProgress`.
- `ul` implements Asynchronous Operations Window Negotiation, with absent
  negotiation defaulting to `1/1`. `dimse.AsyncSession` is the explicit opt-in
  runtime for negotiated concurrent operations; ordinary helper channels do
  not imply multiplexing.
- `OperationOptions` configures context cancellation, an overall operation
  timeout, and a per-response timeout for helpers with `WithOptions` variants.
- Operation failures preserve `errors.Is` matching for local cancellation and
  timeout through `ErrOperationCanceled` and `ErrOperationTimeout`. Errors after
  a partial write, timeout or canceled receive loop also match
  `ErrAssociationStateUncertain`.
- C-FIND leaves Release or Abort to the caller by default. C-MOVE uses
  `OperationErrorPolicyAbort` by default because in-flight retrieve operations
  can leave the association state uncertain. Callers using `WithOptions` variants
  can choose leave-open, release or abort behavior.
- The library does not automatically retry DIMSE command or dataset writes.
  Retry policy belongs to callers or service code that can decide whether an
  operation is idempotent and whether the peer or association state is still
  valid.

Implemented operation boundaries:

- The shared DIMSE operation executor provides an active-operation guard,
  context and timeout options, typed cancellation/timeout errors and explicit
  release-vs-abort policy after uncertain transport state.
- The bounded SCP service queue provides backpressure options such as maximum
  concurrent associations, maximum active operations, queue depth and enqueue
  timeout. `dimse.ServiceQueue` provides the reusable worker/queue boundary, and
  `cmd/echoscp` wires it to queue flags. When capacity is exhausted after an
  echo association has been accepted but before a command is accepted, `echoscp`
  aborts the affected association with a clear diagnostic. Future service paths
  should return a service-specific refusal or out-of-resources status when the
  protocol path supports it.
- `ul.AssociationServer` now applies the association limit before negotiation,
  including silent peers. Saturation may backpressure or return the PS3.8 local-
  limit-exceeded transient rejection. Its deadline-aware shutdown cancels and
  closes remaining associations after the grace deadline. Application handlers
  must still cooperate with context cancellation.
- `net/telemetry` provides safe, correlated lifecycle/PDU/DIMSE summaries and
  atomic counters. Endpoints are omitted unless an explicit plaintext or HMAC
  policy is selected. Raw P-DATA is a separate opt-in sink with mandatory byte
  budgets; A-ASSOCIATE/User Identity bytes are excluded.
- An operation window greater than one is qualified only through
  `dimse.AsyncSession`, which owns association reads, assembles command plus
  dataset, and routes both directions by Message ID. The legacy C-GET helper's
  scoped single-operation loop remains supported but is not a concurrent
  session.

#### Role selection (SCU/SCP)

- By default, an association is negotiated with the local application acting as
  SCU for the requested operation (QR or Storage Commitment).
- `ul.DialOptions.RoleSelections` can propose SCP/SCU role selection items by
  SOP Class UID, and `ul.AcceptOptions.RoleSelections` defines which requested
  roles the accepting side is willing to return.
- `ul.Association` exposes requested and accepted role selections so DIMSE
  service code can inspect the negotiated result.
- For operations involving sub-operations (notably C-GET, which delivers C-STORE
  sub-operations on the same association), role selection must allow the local
  application to act as SCP for Storage SOP Classes.

#### SOP Class Extended Negotiation

- `ul.DialOptions.ExtendedNegotiation` can propose raw SOP Class Extended
  Negotiation items for SOP Classes already present in presentation contexts.
- `ul.AcceptOptions.ExtendedNegotiationHandler` can accept, ignore or reject
  each requested item. Accepted data is returned in the A-ASSOCIATE-AC user
  information and exposed through `ul.Association.AcceptedExtendedNegotiation`.
- When no handler is configured, requested SOP Class Extended Negotiation items
  are ignored rather than implicitly accepted. A handler rejection rejects the
  association with `ErrExtendedNegotiationRejected`.
- Other unsupported user-information sub-items are preserved by the PDU codec as
  `UnknownUserItem`; association negotiation ignores them unless a future API
  assigns explicit policy.

Current scope assumptions:

- For initial C-GET support, accept Storage SOP Classes with SCP role enabled on
  the same association so the peer can issue C-STORE requests.
- For C-MOVE, sub-operations occur on a separate association initiated by the
  C-MOVE SCP to the Move Destination AE. This does not require Storage SCP role
  selection on the original C-MOVE association, but does require a C-STORE SCP at
  the destination and application code that opens and reports those
  sub-operations.

#### Handling multiple contexts for retrieve + storage

- Query/Retrieve uses Study Root or Patient Root QR presentation contexts.
- Storage sub-operations use Storage SOP Class presentation contexts.
- Transfer syntax selection for storage follows the accepted context for the
  Storage SOP Class; if no suitable context is accepted, storage sub-operations
  will fail.

## Non-goals and production-readiness boundaries (v0.1.0)

`dicom-go` `v0.1.0` is a library-focused release that implements only a small
subset of DICOM networking and does **not** claim full DICOM conformance.

The following are explicit **non-goals** and **boundaries** for this version:

- **Not a complete DICOM network stack**: only basic DIMSE primitives required by
  current implemented features and a generic normalized-service layer are
  provided; service-class workflows remain application responsibilities.
- **DICOMweb belongs at the library boundary**: reusable QIDO-RS, WADO-RS and
  STOW-RS request/response helpers are a `dicom-go` responsibility, not an
  application-backend responsibility. The `net/dicomweb` package provides the
  reusable client and embeddable bounded server core. Twin delegates those
  mechanics to the library; application endpoint profiles, credential lookup/storage, jobs,
  archive import/export, receiver policy, operation history, UI summaries and
  auto-query stay outside this library.
- **Query/Retrieve is limited**:
  - Implemented: Study Root C-FIND SCU/SCP, C-MOVE SCU/SCP workflow helpers and
    C-GET SCU/SCP workflow helpers, plus Patient Root C-FIND/C-MOVE/C-GET
    SCU/SCP wrappers and presentation-context/identifier helpers.
  - Study Root C-FIND matching is provided by `qrmatch` and covers `STUDY`,
    `SERIES` and `IMAGE`. Callers still own the archive/index; the library does
    not bundle a production PACS catalog.
  - Modality Worklist is implemented as a separate single-level C-FIND profile
    with a deliberately bounded Table K.6-1 key subset; it is not treated as a
    hierarchical Query/Retrieve model.
  - Not implemented: retired Patient/Study Only models and Color Palette
    Query/Retrieve models.
  - C-MOVE still requires a C-STORE SCP at the Move Destination AE; the library
    does not provide a production archive or destination registry.
- **Storage Commitment policy remains application-owned**:
  - Implemented: N-ACTION and N-EVENT-REPORT primitives, strict action/event
    datasets, persistent-store/CAS seams, bounded listener and retry workflow,
    same/separate/manual delivery modes, and an in-memory store for tests.
  - Not bundled: a database, clinical commitment decision, callback directory,
    automatic deletion policy, audit-retention backend, or always-on worker.
    The application supplies these through the documented interfaces and owns
    their lifecycle. Automated external-peer coverage remains opt-in.
- **Normalized DIMSE is a protocol layer, not six complete applications**:
  N-EVENT-REPORT, N-GET, N-SET, N-ACTION, N-CREATE and N-DELETE have generic
  SCU/SCP support. UPS and Storage Commitment add narrow, separately documented
  workflows; MPPS, Print Management and other service-specific state machines
  are not bundled.
- **UPS persistence and clinical policy remain application-owned**:
  package `ups` supplies the normative state machine, atomic repository/outbox
  seam, bounded in-memory reference, streaming query routes and callback
  delivery worker. It does not bundle a production database, scheduler,
  clinical priority policy, AE directory, authorization, audit backend or UI.
- **Limited advanced association semantics**: role selection and raw SOP Class
  Extended Negotiation items are available at the UL boundary, but service-class
  specific extended negotiation behavior and SOP Class Common Extended
  Negotiation semantics remain outside the current scope.
- **Explicit async boundary**: legacy helpers still assume one outstanding
  operation per association. Multiple operations require negotiated capacity
  and exclusive ownership by `dimse.AsyncSession`; mixing session and legacy
  readers/writers is unsupported.
- **Limited interoperability guarantees**: peers may require capabilities not
  implemented here (e.g. specific transfer syntaxes, SCP/SCU role selection,
  or optional command elements).
- **No complete security profiles**: TLS is available only as an opt-in UL
  transport using caller-supplied `tls.Config` values. User Identity negotiation
  is available as an opt-in association item with caller-supplied accept policy.
  Authentication, authorization, auditing (ATNA), credential storage and access
  policy remain deployment responsibilities. The `net/audit` event model is only
  a local callback surface for deployment wrappers.
- **Not production hardened**: error handling, resource limits, and timeouts are
  suitable for development and targeted integrations, but are not a substitute
  for a full-featured DICOM toolkit.

## Pixel Data Codecs

Supported:

- Native uncompressed frame extraction through `pixeldata.ExtractNativeFrames`.
- Encapsulated Pixel Data fragment preservation.
- Still-image decompression-to-native helpers through
  `pixeldata.DecompressFile` and `pixeldata.DecompressDataSet`. The helpers
  return Explicit VR Little Endian by default, or Implicit VR Little Endian when
  configured, and use the same registered codec path as `DecodeFrames`.
- Built-in JPEG Baseline decoding and opt-in lossy encoding through `pixeldata/jpeg`.
  Encoding requires an explicit quality from 1 through 100 and accepts native
  8-bit unsigned grayscale or interleaved RGB frames; RGB output is declared
  as `YBR_FULL_422` with Planar Configuration 0.
- JPEG Extended (`1.2.840.10008.1.2.4.51`) decoding through `pixeldata/jpeg`:
  Process 2/SOF1 8-bit grayscale and color use the Go standard library;
  Process 4/SOF1 12-bit unsigned monochrome uses the in-tree pure-Go path.
  The Process 4 path bounds compressed and decoded bytes to 512 MiB per frame,
  validates dimensions and referenced quantization/Huffman tables before
  allocation or entropy decode, and supports validated restart intervals.
  JPEG Extended encoding is not provided.
- JPEG Lossless Process 14 (`1.2.840.10008.1.2.4.57`) and Process 14 SV1
  (`1.2.840.10008.1.2.4.70`) SOF3 decoding through
  `pixeldata/jpeglossless`. The decoder accepts grayscale and full-resolution
  unsigned three-component RGB/YBR_FULL streams with interleaved or
  component-separated scans; decoded color samples are interleaved. Frames may
  span Items through the [shared JPEG assembly](ENCAPSULATED_FRAME_ASSEMBLY.md);
  EOT remains restricted to one Item per frame.
- Built-in DICOM RLE Lossless decoding and pure-Go encoding through `pixeldata/rle`.
- JPEG-LS Lossless (`1.2.840.10008.1.2.4.80`) pure-Go decoding and explicitly
  selected pure-Go encoding through `pixeldata/jpegls`. The qualified subset is
  NEAR=0 and ILV=0, plus unsigned RGB ILV=1/2 decoding with 2-16 stored bits
  and MAXVAL=2^precision-1. Default parameters and LSE ID=1 are accepted;
  see [exact profiles and independent evidence](JPEGLS_INTERLEAVE.md).
  NEAR>0, color transforms, restart markers, and
  JPEG-LS Near-Lossless encode (`1.2.840.10008.1.2.4.81`) are outside that
  subset. Independent pydicom/GDCM fixtures are compared bit-exactly, and the
  encoder is additionally gated by CharLS interoperability.
- Separately registered pure-Go JPEG-LS Near-Lossless `.81` decoding: unsigned
  MONOCHROME1/2 ILV=0 and RGB ILV=0/1/2; exact CharLS reconstruction and separate
  source NEAR bounds. See [profiles and exclusions](JPEGLS_NEAR_LOSSLESS.md).
- Explicit pure-Go `.81` encoding for unsigned MONOCHROME1/2 and RGB ILV=0,
  2-16 stored bits, caller-selected positive NEAR and separate encoder/transcoder
  lossy authorization. Derived identity and prior loss history are preserved
  through the existing transcoder. The 216-frame independent CharLS corpus,
  units, lifecycle and exclusions are documented in
  [Near-Lossless encoding](JPEGLS_NEAR_LOSSLESS_ENCODER.md).
- Bounded/context-aware transfer syntax conversion through
  `pixeldata.TranscodeDataSet`, `TranscodeFile`, and transactional
  `TranscodePath`; see [`PIXEL_TRANSCODING.md`](PIXEL_TRANSCODING.md).

Compressed pixel codecs are not registered by package initialization.
Applications explicitly create a caller-owned registry with the complete
built-in decoder baseline and then layer optional codecs on it:

```go
decoders, err := builtin.NewRegistry()
if err != nil {
	panic(err)
}

encoders := pixeldata.NewMemoryEncoderRegistry()
if err := jpeg.RegisterEncoder(encoders, jpeg.DefaultQuality); err != nil {
	panic(err)
}
```

Current metadata support:

| Codec | Bits Allocated | Samples Per Pixel | Pixel Representation | Planar Configuration | Photometric Interpretation |
|---|---:|---:|---:|---:|---|
| JPEG Baseline Process 1 decode | 8 | 1 | 0 | absent or 0 | `MONOCHROME1`, `MONOCHROME2`; SOF0 |
| JPEG Baseline Process 1 decode | 8 | 3 | 0 | absent or 0 | `RGB`, `YBR_FULL`, `YBR_FULL_422`; SOF0; decoded frames are returned as interleaved RGB bytes |
| JPEG Baseline encode (lossy) | 8 stored in 8 | 1 | 0 | absent | `MONOCHROME1`, `MONOCHROME2`; quality 1–100 is required |
| JPEG Baseline encode (lossy) | 8 stored in 8 | 3 | 0 | 0 | native `RGB`; encoded metadata is `YBR_FULL_422`, Planar Configuration 0; quality 1–100 is required |
| JPEG Extended Process 2 decode | 8 | 1 | 0 | absent or 0 | `MONOCHROME1`, `MONOCHROME2`; SOF1 |
| JPEG Extended Process 2 decode | 8 | 3 | 0 | absent or 0 | `RGB`, `YBR_FULL`, `YBR_FULL_422`; SOF1; decoded frames are returned as interleaved RGB bytes |
| JPEG Extended Process 4 decode | 16 allocated, 12 stored, HighBit 11 | 1 | 0 | absent or 0 | `MONOCHROME1`, `MONOCHROME2`; SOF1 precision 12; native output is little-endian; signed samples and 9-11 bit precision are unsupported |
| JPEG Lossless Process 14 | 8 allocated / 2-8 stored; 16 allocated / 2-16 stored; HighBit=BitsStored-1 | 1 | any stored value | absent or 0 | `MONOCHROME1`, `MONOCHROME2`; SOF3 precision equals BitsStored; Huffman lossless, predictors 1-7 and point transforms 0 through precision-1 |
| JPEG Lossless Process 14 | 8 allocated / 2-8 stored; 16 allocated / 2-16 stored; HighBit=BitsStored-1 | 3 | 0 | 0 | `RGB`, `YBR_FULL`; SOF3 precision equals BitsStored; 1x1 sampling; one interleaved scan or three single-component scans; predictors 1-7 and point transforms 0 through precision-1; decoded bytes are interleaved |
| JPEG Lossless Process 14 SV1 | 8 allocated / 2-8 stored; 16 allocated / 2-16 stored; HighBit=BitsStored-1 | 1 | any stored value | absent or 0 | `MONOCHROME1`, `MONOCHROME2`; SOF3 precision equals BitsStored; Huffman lossless, predictor 1 only |
| JPEG Lossless Process 14 SV1 | 8 allocated / 2-8 stored; 16 allocated / 2-16 stored; HighBit=BitsStored-1 | 3 | 0 | 0 | `RGB`, `YBR_FULL`; SOF3 precision equals BitsStored; 1x1 sampling; one interleaved scan or three single-component scans; predictor 1 in every scan; decoded bytes are interleaved |
| RLE Lossless decode/encode | 8 or 16 (stored bits may be 12 in 16 allocated) | 1 | 0 or 1 for monochrome; 0 for palette | absent or 0 | `MONOCHROME1`, `MONOCHROME2`, `PALETTE COLOR` |
| RLE Lossless decode/encode | 8 or 16 | 3 | 0 | 0 | `RGB`; native frames are interleaved sample bytes |
| JPEG-LS Lossless decode/encode | 8 or 16 (stored bits may be 12 in 16 allocated) | 1 | 0 or 1 for monochrome; 0 for palette | absent or 0 | `MONOCHROME1`, `MONOCHROME2`, `PALETTE COLOR`; NEAR=0, ILV=0; default or LSE preset ID=1 |
| JPEG-LS Lossless decode/encode | 8 or 16, 2-16 stored | 3 | 0 | 0 | `RGB`; native frames are interleaved sample bytes; NEAR=0; ILV=0 decode/encode, ILV=1/2 decode; default or LSE ID=1 within [qualified limits](JPEGLS_INTERLEAVE.md) |
| JPEG-LS optional adapter | 8 or 16 | 1 | 0 or 1 | absent | `MONOCHROME1`, `MONOCHROME2`; alternative backend for Near-Lossless and modes outside the builtin subset |
| JPEG-LS optional adapter | 8 or 16 | 3 | 0 or 1 | absent or 0 | `RGB`; alternative backend for Near-Lossless and modes outside the builtin subset; decoded frames are interleaved RGB bytes |
| JPEG 2000 / HTJ2K optional adapter | 8 or 16 allocated; stored bits up to allocated bits | 1 | 0 | absent | `MONOCHROME1`, `MONOCHROME2`; JPEG 2000, JPEG 2000 Part 2 and HTJ2K still-image UIDs; `HighBit` must equal `BitsStored-1` |
| JPEG 2000 / HTJ2K optional adapter | 8 or 16 allocated; stored bits up to allocated bits | 3 | 0 | absent or 0 | `RGB`, `YBR_RCT`, `YBR_ICT`; decoded frames are returned as interleaved RGB bytes; `HighBit` must equal `BitsStored-1` |

Unsupported metadata returns typed errors that include the rejected field name
and value. JPEG Baseline/Extended/Lossless, builtin JPEG-LS and its optional
adapter use [shared bounded Item assembly](ENCAPSULATED_FRAME_ASSEMBLY.md):
validated BOT, normative one-Item EOT/Lengths and structural marker inference
for ambiguous empty-table layouts. Only one compressed frame is joined at a
time; deferred sources use existing parser Item locations. The JPEG-LS adapter
reports typed errors for unavailable decoders, unsupported metadata, malformed
frames returned by the decoder backend, and unsupported fragment layouts. The
JPEG 2000 / HTJ2K adapter validates codestream dimensions,
component count, component precision against `BitsStored` and signedness before
decode, assembles multiple fragments for a single frame, and rejects ambiguous
multi-frame fragment layouts with a typed error. Its current controlled-profile
decision and benchmark evidence are
tracked in
[`examples/codec-adapters/jpeg2000/PRODUCTION_PROFILE.md`](../examples/codec-adapters/jpeg2000/PRODUCTION_PROFILE.md).
The OpenJPEG fallback profile is selected explicitly with `jpeg2000_openjpeg` or
`RegisterOpenJPEG`; it registers OpenJPEG for JPEG 2000 Part 1 still-image UIDs
and keeps JPEG 2000 Part 2 plus HTJ2K on the pure-Go fallback path. It requires
`opj_decompress`, returns a typed OpenJPEG-unavailable error when that runtime
tool is absent, validates PNM precision against `BitsStored`, and bounds
subprocess output before reading decoded frames. Its packaging, transitive
dependency audit, and license notes are tracked in
[`examples/codec-adapters/jpeg2000/OPENJPEG_PROFILE.md`](../examples/codec-adapters/jpeg2000/OPENJPEG_PROFILE.md).

## Enhanced CT/MR to Classic single-frame conversion

`multiframe.ConvertToClassic` implements the PS3.4 C.3.5 Enhanced-to-Classic
conversion profile for Enhanced CT Image Storage
(`1.2.840.10008.5.1.4.1.1.2.1`), Legacy Converted Enhanced CT Image Storage
(`1.2.840.10008.5.1.4.1.1.2.2`), Enhanced MR Image Storage
(`1.2.840.10008.5.1.4.1.1.4.1`), and Legacy Converted Enhanced MR Image
Storage (`1.2.840.10008.5.1.4.1.1.4.4`). It emits one CT Image Storage
(`1.2.840.10008.5.1.4.1.1.2`) or MR Image Storage
(`1.2.840.10008.5.1.4.1.1.4`) Part 10 object per source frame. Every output has
a new SOP Instance UID, all outputs in one conversion use a new Series Instance
UID, and the source Study Instance UID and Frame of Reference UID are retained.
Patient, Study, and remaining spatial or temporal frame-of-reference attributes
are copied without mutation.

The converter applies Shared Functional Groups and then the corresponding
Per-Frame Functional Groups item to each output. It maps the standard classic
attributes required by the target IOD, including:

- Pixel Measures to Pixel Spacing, Slice Thickness, and Spacing Between Slices;
- Plane Position (Patient) and Plane Orientation (Patient) to Image Position
  (Patient) and Image Orientation (Patient);
- CT or general Pixel Value Transformation to Rescale Intercept, Rescale Slope,
  and Rescale Type;
- Frame VOI LUT attributes to their classic top-level equivalents; and
- CT/MR Frame Type and acquisition/timing attributes to the corresponding
  classic CT or MR image attributes.

The output does not contain Number of Frames, Shared/Per-Frame Functional
Groups, Multi-frame Dimension, concatenation, representative-frame, or
frame-increment attributes. Remaining standard and private attributes from
the source top level, Shared Functional Groups, and the selected Per-Frame item
are preserved at the output top level when they do not conflict with a mapped
target-IOD attribute. Ambiguous collisions fail instead of silently choosing a
value. A source that places the same Functional Group Macro in Shared and
Per-Frame Functional Groups is rejected, as required by PS3.3 C.7.6.16.1.1.

Pixel provenance is preserved. A byte-equivalent frame does not become
`DERIVED` merely because its container changed: Image Type is populated from
the applicable Frame Type and remains `ORIGINAL`/`PRIMARY` when those source
semantics still apply. This primitive does not expose a pixel transformation
that would justify changing those provenance terms.

Each output contains Conversion Source Attributes Sequence (0020,9172) with the
source SOP Class UID, source SOP Instance UID, and one-based Referenced Frame
Number. It also appends the required Contributing Equipment Sequence item with
Purpose of Reference `(109106, DCM, "Enhanced Multi-frame Conversion
Equipment")` and Contribution Description `Classic Image created from Enhanced
Image`. Existing contributing-equipment items are retained. These
conversion references are distinct from the optional Source Image Sequence and
are not replaced by it.

The caller supplies the new SOP and Series Instance UIDs. A Query/Retrieve SCP
that uses this primitive for the alternative views defined by PS3.4 C.3.5 is
responsible for supplying deterministic UIDs across repeated retrievals; a
manual conversion workflow may instead create a new derived set on each run.

The output transfer syntax is always Explicit VR Little Endian. Native input is
normalized without changing sample values. Encapsulated still-image input is
decoded through the caller-provided registry; the converter does not perform a
lossy re-encode. Existing lossy-compression history attributes remain present
when the source pixel values were previously lossy. Conversion is bounded and
context-aware, and cancellation or any frame failure returns no partial output
set.

Current limits are deliberate:

- 8-bit or color Enhanced MR, PET, XA/XRF, ultrasound, microscopy, video,
  JPIP-referenced pixels, Float Pixel Data, and Double Float Pixel Data are
  rejected as unsupported source profiles.
- Encapsulated sources require a registered decoder that can decode the complete
  declared frame layout. Missing codecs and ambiguous fragment layouts fail
  with typed pixel-data errors.
- Missing target Type 1 values, a Per-Frame Functional Groups count different
  from Number of Frames, malformed geometry, invalid UIDs, and functional-group
  attributes that cannot be represented without ambiguity are conversion
  errors. Required Type 2 attributes are emitted with an empty value when the
  source provides no value; the converter never invents clinical content.
- The primitive creates in-memory Part 10 objects. Filesystem staging, archive
  transactions, progress reporting, and user-interface policy belong to the
  application.

Unsupported:

- JPEG Extended hierarchical/progressive modes and metadata outside the
  documented Process 2/4 subset, JPEG-LS modes outside the
  qualified builtin Lossless subset, JPEG XL, MPEG and HEVC frame decoding.
  JPEG-LS modes outside that subset, JPEG 2000/HTJ2K, and JPEG XL metadata plus
  encapsulated Pixel Data preservation remains available without an optional
  decoder adapter. JPEG XL decoder selection is tracked in
  [`JPEGXL_DECODER_STRATEGY.md`](JPEGXL_DECODER_STRATEGY.md).
- JPIP, deflated image-frame compression and SMPTE ST 2110 media decoding or
  streaming.
- Lossy encoder implementations and automatic transcoding during DIMSE
  negotiation. The API has a fail-closed lossy extension seam, but the default
  module ships built-in RLE Lossless and JPEG-LS Lossless encoders. JPEG-LS
  Near-Lossless encode is not provided.
- Complete presentation pipelines such as presentation states, overlays,
  shutters, ICC color management, and display calibration. Reusable frame
  helpers cover modality/VOI LUTs and the native color layouts documented below.

## Frame Rendering Helpers

The `pixeldata/frame` helpers and the example viewer can turn decoded frame
bytes into Go `image.Image` values for a small display-oriented subset:

| Samples Per Pixel | Bits Allocated | Pixel Representation | Planar Configuration | Photometric Interpretation | Output |
|---:|---:|---:|---:|---|---|
| 1 | 8 or 16 | 0 or 1 | absent | `MONOCHROME1`, `MONOCHROME2` | `*image.Gray` with rescale/window handling. |
| 3 | 8 | 0 | absent or 0 | `RGB` | `*image.RGBA` using interleaved RGB bytes. |
| 3 | 8 | 0 | 1 | `RGB` | `*image.RGBA` using planar RGB bytes. |
| 3 | 8 | 0 | absent, 0, or 1 | `YBR_FULL` | `*image.RGBA` after full-range YBR conversion. |
| 3 | 8 | 0 | absent or 0 | `YBR_FULL_422` | `*image.RGBA` after native 4:2:2 expansion and YBR conversion. |

Color rendering does not apply VOI/window, modality rescale, presentation
states, overlays, ICC profiles, or display calibration. RGB/YBR samples wider
than 8 bits, signed color samples, planar YBR_FULL_422, other native YBR
variants, and PALETTE COLOR through `pixeldata/frame` remain unsupported and
return typed errors. `pixeldata/display.RenderColor` separately supports
PALETTE COLOR when callers provide the required LUT metadata.

`render.StandardEncodedRenderer` accepts an already-decoded `render.Frame` and
uses this same grayscale modality/VOI and native color path before encoding
`image/png` or 8-bit Baseline `image/jpeg`. An optional source rectangle is
cropped before bilinear scaling. Requested width and height form a bounding box
that preserves aspect ratio; zero dimensions retain or derive the source
matrix. Source dimensions, requested dimensions, pixel count, and encoded
output bytes are bounded by `EncodedImageLimits`. Cancellation is checked
before rendering, for every resize row, and on every encoder write. Resource
lookup, transfer-syntax decoding, annotations, and ICC color management remain
caller-owned.

## JSON

Supported:

- DICOM JSON marshal/unmarshal for the in-memory value types implemented by
  `core`.
- Person Name JSON components.
- Sequences.
- Inline binary values.
- `BulkDataURI` preservation.
- `Value` arrays for AE, AS, AT, CS, DA, DS, DT, FD, FL, IS, LO, LT, PN,
  SH, SL, SQ, SS, ST, SV, TM, UC, UI, UL, UR, US, UT and UV.
- `InlineBinary` or `BulkDataURI` for OB, OD, OF, OL, OV, OW and UN.

Limitations:

- `BulkDataURI` is not fetched or resolved.
- `UN` values are preserved as bytes through `InlineBinary` or `BulkDataURI`;
  JSON `Value` arrays for UN are rejected instead of guessed.
- Unsupported or mismatched VR/value combinations return errors with the JSON
  path, VR and received JSON value type.

## Native DICOM Model XML

Supported:

- PS3.19 Native DICOM Model marshal/unmarshal for the in-memory value types
  implemented by `core`, including sequences, Person Name component groups,
  empty values, `InlineBinary`, and `BulkData` URI/UUID references.
- The required namespace, `xml:space`, tag normalization, sequential value
  numbering, and omission of retired group-length elements.
- Finite element, value, nesting, binary, and aggregate input limits.
- DICOMweb QIDO and WADO metadata negotiation as
  `multipart/related; type="application/dicom+xml"`, with one model per part.

Limitations and safety policy:

- DTDs and processing instructions are rejected; the decoder does not expand
  external entities.
- `BulkData` references are preserved without I/O by default. Rejection or
  bounded resolution requires an explicit caller policy.
- Resolving encapsulated Pixel Data requires an explicit transfer syntax; the
  decoder does not infer compressed syntax from XML metadata.
- Missing or mismatched `keyword` attributes fail by default. A caller may
  explicitly enable missing-keyword tolerance for nonconforming producers.

The Part 10 writer can materialize preserved `BulkData` references through an
explicit streaming resolver. It validates the declared source size, supports
references nested in sequences, pads according to the VR, and owns/closes each
resolved reader. It deliberately does not infer or synthesize encapsulated
Pixel Data.

## Security Considerations

Parser limits are opt-in. Zero-valued numeric limits mean unbounded reads, which
is convenient for trusted batch processing but inappropriate for untrusted
inputs.

For untrusted files, set limits through `object.OpenFileWithOptions` or
`object.ReadFileWithOptions`:

```go
file, err := object.ReadFileWithOptions(r, object.ReadFileOptions{
	MaxElementBytes:               16 << 20,
	MaxDeformableVectorGridBytes: 64 << 20,
	MaxTotalBytes:                 128 << 20,
	MaxSequenceDepth:              32,
	MaxElements:                   100000,
	MaxFragments:                  10000,
	StrictReservedBytes:           true,
})
```

Relevant options:

- `MaxElementBytes`: caps allocation for a single element value.
- `MaxDeformableVectorGridBytes`: caps each Vector Grid Data OF value before
  allocation, including values nested in sequences.
- `MaxTotalBytes`: caps total bytes read.
- `MaxSequenceDepth`: caps nested sequence/item depth.
- `MaxElements`: caps primitive element count per parser reader.
- `MaxFragments`: caps encapsulated Pixel Data fragment count.
- `StrictReservedBytes`: rejects non-zero explicit VR reserved bytes.
- `OddLengthPolicy`: controls handling of odd element lengths.

These limits may reject valid large DICOM objects. Tune them for the deployment
environment.

## Network Security

`v0.1.0` networking uses plain TCP by default. The core UL APIs can opt into
TLS for association initiation and acceptance by setting
`ul.DialOptions.TLSConfig` and `ul.AcceptOptions.TLSConfig`. The caller supplies
the certificate roots, server certificates and verification policy through the
standard `crypto/tls` configuration.

The DIMSE CLI tools still default to plain TCP and do not expose TLS flags.
User Identity negotiation can convey username, username/password and
token-oriented identity material, but the APIs do not authenticate those
credentials, authorize operations or write audit logs by themselves.

The standalone DICOMweb CLI exposes bounded `verify`, `query`, `retrieve`,
`frames`, and `store` client operations. Every operation has explicit service
paths, timeout, response-body limit, and output destination. Basic passwords
and bearer tokens are read only from caller-named environment variables;
credential values are not accepted in command arguments or emitted in
diagnostics. HTTP authentication is rejected unless the caller explicitly sets
`-allow-insecure-auth`; HTTPS remains the production transport expectation.
This is client-side HTTP Authorization header support, not an authentication,
authorization, credential-storage, or audit service.

QIDO result output is not PHI-free and must be handled as clinical data. CLI
diagnostics redact credentials, query strings, identifiers, URLs, and local
paths. WADO output uses bounded part counts and bytes, owner-only temporary
files, atomic publication, and no overwrite. STOW accepts bounded DICOM Part 10
files and distinguishes complete success, warnings, and partial storage. The
normative operational contract and exit-code mapping are documented in
[`DICOMWEB_CLI.md`](./DICOMWEB_CLI.md).

Not supported:

- Server-side authentication or authorization policy.
- Audit logging backend / ATNA.
- Automatic DIMSE retry policy.
- Application-owned DICOMweb endpoint profiles, credential storage, retry
  policy, job progress or archive import/export orchestration.
- Durable queues, scheduler persistence, metrics backend or service-wide retry
  orchestration.

Planned boundaries:

- TLS belongs in the core UL association transport as an opt-in client/server
  configuration. Plain TCP remains the default and never silently upgrades or
  downgrades. Misconfigured TLS handshakes return explicit client/server TLS
  errors. CLI flags may be added later, but they should remain separate from
  authentication and authorization policy.
- User Identity negotiation belongs in the UL association negotiation API rather
  than in DIMSE command helpers. SCUs set `ul.DialOptions.UserIdentity`; SCPs
  set `ul.AcceptOptions.UserIdentityHandler` to accept, reject or ignore
  identity items explicitly. Unsupported identity types, missing server policy
  and invalid requests fail predictably instead of creating an authenticated
  session by accident.
- Authentication, authorization, audit logging, credential storage and external
  identity-provider integration are deployment/application policy topics
  documented below.
- `net/audit` exposes structured local events to deployment code without
  importing a logging framework or emitting raw datasets/credentials by default.
  `cmd/echoscp` emits the first C-ECHO events; future service helpers should
  reuse the same event model.
- Reusable DICOMweb clients/helpers belong in this module next to the DIMSE
  network surface. Callers still own base URLs, TLS roots, bearer/basic
  credentials, site policy, retry scheduling, archive ingestion, receiver
  policy, operation history, UI summaries, auto-query and user-facing operation
  state.
- The current CLIs keep their trust and deployment warnings in focused command
  documentation rather than printing the same warning on every invocation.

Limitations/non-goals:

- The network stack is designed for trusted networks only.
- There is no fuzzy matching or server-side query enhancement beyond PS3.4
  C.2.2 matching in `qrmatch` and what a local handler or remote peer implements
  for Study Root C-FIND.
- Role selection and raw SOP Class Extended Negotiation are implemented at the UL
  boundary, but service-specific extended negotiation semantics are not.
- No authentication, authorization, credential storage, external identity-provider
  integration, ATNA formatting, secure audit transport or audit retention policy
  is implemented by the library.

Use process-level controls, firewalls and trusted test peers when exercising the
network CLIs.

### Authentication, authorization, audit and PHI policy

User Identity negotiation is a wire-level association feature, not a complete
authentication or authorization system. Applications must decide whether
credentials are trusted, which AE titles or users may perform an operation, and
how failed or missing identity information should affect
deployment policy.

Authorization remains outside the core library. Network services should expose
enough metadata for deployment wrappers to make decisions, such as AE titles,
remote address, SOP Class UID, command type and status, but the library does not
own site access-control policy.

Audit event hooks in `v0.1.0` are local observer callbacks, not audit logging by
themselves. `net/audit.Event` carries association and service metadata such as
AE titles, remote address, SOP Class UID, command, status and error class while
avoiding raw datasets and credential material by default. ATNA message
formatting, secure log delivery, audit repository integration, retention policy
and operational monitoring remain deployment responsibilities.

Automatic PHI detection, network-traffic de-identification, and implicit
anonymization are explicit non-goals for the networking module. The opt-in
`deid` package is the reusable offline de-identification surface. Fixtures and
tests remain synthetic or scrubbed, and callers must validate the selected
profile/options and residual-risk report before sharing clinical data.

## Known Limitations

- Odd-length element handling is configurable but not a substitute for full
  dataset validation.
- Undefined length support is implemented for sequences/items and encapsulated
  Pixel Data. Defined-length `UN` values are preserved as raw bytes. Undefined-
  length `UN` values are rejected unless a dictionary resolves the element to
  `SQ`; arbitrary undefined-length primitive values are not a general supported
  interchange path.
- Character set handling covers the terms listed in
  `docs/CHARACTER_SETS.md`, including DICOM G0/G1 ISO 2022 designation and
  delimiter resets. Applications should still qualify deployment-specific
  character repertoires and fonts against their intended clinical languages.
- No automatic PHI/OCR detection or implicit network de-identification is
  provided; the explicit offline `deid` package is described in
  [`DEIDENTIFICATION.md`](DEIDENTIFICATION.md).
- SOP Class-specific required-attribute validation is opt-in through
  `object.ValidateSOPClass` and caller-provided hooks. The library provides a
  narrow `object.RequiredAttributeRule` helper but no complete rule set for
  every SOP Class or IOD.
- Uniform VR/VM, dictionary, File Meta and lifecycle validation is opt-in; the
  library does not bundle complete IOD/module rule sets. Built-in structural
  and SR validation diagnostics omit dataset values; application hooks and
  custom rules are trusted code with direct access to supplied datasets. See
  [`VALIDATION.md`](VALIDATION.md).
- Deferred (lazy, seek-and-replay) value access covers defined-length
  primitive values and undefined-length encapsulated Pixel Data only. General
  `SQ` replay is an intentional non-goal: sequence and item values are always
  materialized in memory while reading. Lazily replaying nested sequences
  would require tracking item boundaries through arbitrary explicit- and
  undefined-length nesting and re-deriving transfer-syntax-specific framing on
  each replay, for a benefit limited to reducing peak memory on datasets with
  very large nested `SQ` metadata (e.g. bulky private sequences). Given that
  narrow payoff against the complexity and round-trip risk of a lazy `SQ`
  reader, this project keeps sequence materialization as the permanent
  behavior. Applications with memory-constrained nested-sequence
  workloads should read the affected sequence eagerly and discard it after
  use, or filter it out via a caller-provided element predicate.
- The `dsa` package reads the DICOM Mask Module (Mask Subtraction Sequence
  (0028,6100) and Recommended Viewing Mode (0028,1090)) — the frame pairing,
  applicable frame range, sub-pixel shift, and contrast-frame-averaging count
  an XA/RF multi-frame object declares. It intentionally does not implement
  the `TID`/`REV_TID` acquisition-time temporal-subtraction algorithms (PS3.3
  C.7.6.7.1.1) or apply contrast-frame averaging: those describe how the
  acquisition device produced frames, not an operation a viewer needs to
  reproduce on already-acquired pixel data. Callers that only need "which
  frame is the mask for this frame" can use `MaskSubtractionItem.
  MaskFrameForContrastFrame` without caring about the operation code.

## Architecture Notes

The optional [OpenJPEG lossless encoder](../examples/codec-adapters/jpeg2000/ENCODER_PROFILE.md)
registers only JPEG 2000 Part 1 `.90`, with explicit runtime preflight and a
bounded subprocess. The qualified encode subset is unsigned MONOCHROME1/2 or
RGB, 8..16 stored bits, on Windows/amd64 and Linux/amd64. The existing transcode
pipeline owns encapsulation and metadata. Independent native FFmpeg verification
checks every sample; this does not extend other JPEG 2000 encode directions.

The [codify generator](CODIFY.md) emits bounded, compiled Go reproductions using
public object/core APIs. Structural substitutions are the default; faithful
values and binary inclusion require explicit policy. It is not a DICOM
serializer or an automatic anonymization/byte-exact reconstruction facility.

The [reference archive example](../examples/referencearchive/README.md) composes
safe Part 10 publication, detached metadata indexing, `qrmatch` and existing
DIMSE services. It supports the documented finite, native-syntax SC/CT/MR Study
Root subset, restart validation and explicit C-MOVE destinations. Storage
Commitment and DICOMweb are disabled. Its opt-in independent pynetdicom/pydicom
profile checks store/find/get/move, cancellation, all fixture pixel samples and
restart; this does not qualify a clinical archive or arbitrary peer/IOD.

C-GET and C-MOVE request parsing accepts every Command Data Set Type value
except 0101H as indicating an identifier, as specified by PS3.7 Tables 9.3-6 and
9.3-9. Builders retain their existing 0000H encoding. Regression tests include
the 0001H encoding used by the independent pynetdicom peer.

The module uses concrete Go structs, small interfaces and explicit registries.
The release documents only the implemented subset.
