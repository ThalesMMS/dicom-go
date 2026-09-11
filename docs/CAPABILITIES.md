# Current capability and limitation scope

This document is the current entry point for deciding whether `dicom-go`
matches an integration. A package name or recognized UID does not imply full
support for every service, transfer syntax, SOP Class, clinical workflow, or
deployment profile. Detailed documents linked below define narrower contracts.

The [separate validation inventory](../../dicom-go-validation/docs/CAPABILITY_AUDIT.md)
records additional execution evidence. Local regression and codec checks remain
part of this module's `make check` baseline.

## Supported surfaces

| Area | Current library surface | Verification and detail |
|---|---|---|
| Data model and dictionaries | Tags, VRs, values, sequences, fragments, standard/private dictionary composition, UID lookup, and typed date/time and person-name helpers. | `core/`, `dictionary/`, `dcmtime/`, `personname/`; `make check` |
| File and dataset I/O | Part 10 and raw-dataset read/write, explicit and implicit VR, little and big endian, deferred values, selective reads, transfer-syntax recovery as an explicit opt-in, and configurable resource limits. | `object/`, `parser/`; [Transfer syntax recovery](./TRANSFER_SYNTAX_RECOVERY.md) |
| Metadata interchange and validation | DICOM JSON, Native DICOM Model XML, bounded inspection/indexing, and opt-in structured validation hooks. | `dicomjson/`, `dicomxml/`, `dicominspect/`, `index/`, `validation/`; [Validation](./VALIDATION.md) |
| Pixel data and rendering | Native and encapsulated frame extraction, caller-owned codec registries, the documented built-in codec subsets, transcoding, grayscale/color display, headless 2D/3D rendering, MPR/CPR/VR, and ROI operations. | `pixeldata/`, `render/`, `roi/`; [Codec matrix](./CODEC_CAPABILITY_MATRIX.md), [pixel transcoding](./PIXEL_TRANSCODING.md) |
| Clinical objects | Focused APIs for DICOMDIR, de-identification, GSPS, VPS, SEG, RTSTRUCT, RT Dose, SR, spatial registration, parametric maps, ultrasound, waveforms, microscopy, NIfTI, and related derived-object workflows. | Corresponding public packages and package tests; task-specific documents in `docs/` |
| DIMSE and Upper Layer | Association negotiation, optional TLS and User Identity items, C-ECHO/C-STORE, Query/Retrieve, normalized services, Storage Commitment, async sessions, cancellation, bounded service queues, and PHI-safe telemetry seams. | `net/ul/`, `net/dimse/`, `net/audit/`, `net/telemetry/`; [asynchronous operations](./ASYNCHRONOUS_OPERATIONS.md) |
| DICOMweb | Backend-neutral QIDO-RS, WADO-RS, and STOW-RS client/server primitives, DICOM JSON/XML metadata, bounded streaming object/frame retrieval, multipart store, rendered/thumbnail resources, typed errors, finite client/server limits, and deny-by-default server authorization. | `net/dicomweb/`; [client response and streaming policy](./DICOMWEB_CLIENT.md), [server profile](./DICOMWEB_SERVER.md), [CLI](./DICOMWEB_CLI.md), [ownership boundary](./DICOMWEB_EXTRACTION.md) |
| Unified Procedure Step | UPS state, query, subscription, event, delivery, and workflow primitives. | `ups/`; [UPS scope](./UNIFIED_PROCEDURE_STEP.md) |

## Required caller policy

- Simple file readers preserve compatibility by treating zero numeric budgets
  as unlimited. Untrusted inputs require explicit non-zero read, parse, pixel,
  nesting, element, and fragment limits appropriate to the deployment.
- Codec support is limited to the exact bit depth, component, photometric, and
  encode/decode profiles in the [codec capability matrix](./CODEC_CAPABILITY_MATRIX.md).
  Optional adapters may require separately installed runtimes.
- DIMSE uses plain TCP unless callers configure TLS. User Identity transports
  identity claims but does not authenticate or authorize them.
- DICOMweb Basic authentication requires certificate-verified HTTPS. The
  embeddable server requires an `Authorizer` unless the caller explicitly opts
  into unauthenticated operation.
- Network packages provide protocol mechanics, not protected credential
  storage, endpoint trust policy, durable audit retention, retry orchestration,
  archive persistence, or user-facing job state.
- De-identification is metadata-policy enforcement, not automatic detection or
  removal of burned-in pixel PHI or recognizable visual features. Applications
  must review residual pixel and workflow risk.
- Repository tests and fixtures are synthetic or redistribution-safe. Live-peer
  interoperability is opt-in and must not use repository-default PHI data.

## Verification policy

`make check` is the baseline for the module. `make race` and `make leak` are the
offline concurrency gates. Native codec, long fuzz, and live interoperability
profiles remain explicit opt-ins documented by their dedicated runbooks.

Keep this file in present tense. Add a capability only with a public package or
command plus repository-local tests; describe prospective outcomes separately
without treating them as shipped behavior.
