# dicom-go

`dicom-go` is a Go library that supports resource-bounded DICOM file and dataset processing,
metadata interchange, pixel pipelines, DIMSE and DICOMweb networking, headless
rendering, and reusable clinical-object workflows.

The module favors explicit options, concrete value types, caller-owned
registries, context-aware operations, and typed errors. Application catalogs,
credentials, job orchestration, audit retention, and user interfaces remain the
responsibility of applications built on the library.

This project does not claim complete DICOM conformance. Review the maintained
[capability and limitation scope](./docs/CAPABILITIES.md) before processing
untrusted data or connecting to production peers.

## Install

The base module requires Go 1.22 or newer.

```sh
go get github.com/ThalesMMS/dicom-go
```

The default module is pure Go. Optional codec adapters and their runtime
requirements are documented in the
[codec capability matrix](./docs/CODEC_CAPABILITY_MATRIX.md) and
[dependency policy](./docs/CODEC_DEPENDENCY_POLICY.md).

## Quick start

### Read and write a Part 10 file

`object` is the primary high-level package for Part 10 files and datasets.
Opened files must be closed because deferred-value options may retain the input
file.

This minimal example assumes a locally trusted input. For untrusted input, use
`object.OpenFileWithOptions` and set non-zero `MaxTotalBytes`,
`MaxElementBytes`, `MaxPixelDataBytes`, `MaxDeformableVectorGridBytes`,
`MaxSequenceDepth`, `MaxElements`, and `MaxFragments` budgets appropriate to
the deployment; zero numeric limits are unlimited.

```go
package main

import (
	"fmt"
	"os"

	"github.com/ThalesMMS/dicom-go/object"
)

func main() {
	file, err := object.OpenFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	defer file.Close()

	metadata := file.Metadata()
	fmt.Printf("SOP class: %s\nTransfer syntax: %s\n",
		metadata.SOPClassUID, metadata.TransferSyntaxName)

	output, err := os.Create("copy.dcm")
	if err != nil {
		panic(err)
	}
	if err := object.WriteFile(output, file); err != nil {
		_ = output.Close()
		panic(err)
	}
	if err := output.Close(); err != nil {
		panic(err)
	}
}
```

Use `object.OpenFileWithOptions` for explicit parsing and resource policy. Raw
datasets require an explicit transfer syntax. Transfer-syntax recovery is a
separate opt-in API; strict readers never infer it automatically. See
[Transfer Syntax Recovery](./docs/TRANSFER_SYNTAX_RECOVERY.md).

### Encode DICOM JSON and Native DICOM Model XML

Both encoders operate on the same `*object.Object`. Native XML uses finite
defaults. For untrusted DICOM JSON, set every `dicomjson.Limits` field
explicitly because zero limits retain compatibility behavior.

```go
package main

import (
	"fmt"
	"os"

	"github.com/ThalesMMS/dicom-go/dicomjson"
	"github.com/ThalesMMS/dicom-go/dicomxml"
	"github.com/ThalesMMS/dicom-go/object"
)

func main() {
	file, err := object.OpenFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	defer file.Close()

	jsonData, err := dicomjson.Marshal(file.Dataset, dicomjson.DefaultOptions())
	if err != nil {
		panic(err)
	}
	xmlData, err := dicomxml.Marshal(file.Dataset, dicomxml.DefaultOptions())
	if err != nil {
		panic(err)
	}
	fmt.Printf("DICOM JSON: %d bytes; Native XML: %d bytes\n", len(jsonData), len(xmlData))
}
```

Bulk-data references are preserved without implicit network or filesystem
access. Resolution must be configured explicitly and remains resource-bounded.

Runnable examples cover [file reading](./examples/readfile),
[file writing](./examples/writefile), [DICOM JSON](./examples/json),
[pixel data](./examples/pixeldata), [rendering and ROI](./examples/render-roi),
[DIMSE echo](./examples/echo), and [DIMSE storage](./examples/store).

## Choose the right package

### Dataset and file foundation

| Package | Use it for |
|---|---|
| `dicom` | Compatibility facade over common file/dataset read, write, and validation APIs; `object` owns the high-level implementation. |
| `core` | Tags, VRs, lengths, elements, datasets, sequences, fragments, and primitive values. |
| `object` | High-level datasets, Part 10 read/write, metadata access, cloning, walking, and deferred values. |
| `parser` | Token-level dataset parsing and writing with explicit syntax and limits. |
| `encoding` | Endian-aware primitive and DICOM character-set helpers. |
| `transfer` | Transfer-syntax definitions, lookup, negotiation helpers, and registries. |
| `dictionary`, `dictionary/std`, `dictionary/tags`, `dictionary/uid` | Dictionary contracts, generated standard data, common tags, and UID lookup. |
| `dcmtime`, `personname` | Typed DICOM DA/TM/DT and PN parsing. |
| `validation` | Opt-in element/dataset validation and lifecycle hooks with structured findings. |
| `dicominspect`, `index` | Bounded, presentation-neutral inspection and detached metadata indexing. |

### Interchange, filesets, and derived objects

| Package | Use it for |
|---|---|
| `dicomjson`, `dicomxml` | PS3.18 DICOM JSON and PS3.19 Native DICOM Model XML. |
| `dicomdir` | Bounded DICOM file-set reading, validation, querying, and transactional writing. |
| `deid` | Conservative metadata de-identification and Basic Application Confidentiality Profile workflows. |
| `encapdoc` | Encapsulated PDF object construction. |
| `multiframe` | Enhanced CT/MR to classic single-frame conversion. |
| `seriesderive` | Non-destructive derived-series copies. |
| `nifti` | Geometry-validated NIfTI export primitives. |

### Pixel data, rendering, and ROI

| Package | Use it for |
|---|---|
| `pixeldata` | Pixel metadata, native/encapsulated frame extraction, codec registries, decompression, and transcoding. |
| `pixeldata/builtin` | A fresh caller-owned registry of the built-in pure-Go decoders. |
| `pixeldata/frame`, `pixeldata/display` | Typed frame decoding and the DICOM grayscale/color display pipeline. |
| `pixeldata/jpeg`, `pixeldata/jpeglossless`, `pixeldata/jpegls`, `pixeldata/rle` | Built-in JPEG and RLE codec implementations for their documented subsets. |
| `pixeldata/codecfixture`, `pixeldata/codecprofile` | Synthetic conformance fixtures and auditable codec-profile metadata. |
| `render` | Headless 2D rendering, stacks, MPR/MIP/slabs, CPR, VR, encoded images, and bounded volume storage. |
| `render/qualification` | PHI-free renderer qualification inputs, traces, and comparison evidence. |
| `roi` | Vector/raster ROI geometry, measurement, segmentation, interpolation, and statistics. |
| `video` | Validation and streaming extraction of encapsulated DICOM video without decoding. |
| `jpip` | Policy-bounded retrieval and decode of JPIP-referenced complete pixel streams. |

For native frame delivery with bounded channel backpressure and consumer
cancellation, see [frame channel streaming](docs/FRAME_CHANNEL_STREAMING.md)
and the [runnable example](examples/stream-native-frames/main.go).

### Clinical objects and workflows

| Package | Use it for |
|---|---|
| `clinicalrelation` | Normalized references and relationships among clinical DICOM objects. |
| `dsa`, `dynamic` | Digital-subtraction mask metadata and temporal/spatial acquisition modeling. |
| `gsps`, `vps` | Grayscale Softcopy and volumetric presentation-state read/write/application models. |
| `hangingprotocol` | Bounded, viewer-neutral Hanging Protocol decoding. |
| `microscopy` | Tiled whole-slide microscopy pyramids, tiles, channels, annotations, and viewport composition. |
| `parametricmap`, `rtdose` | Parametric Map and RT Dose parsing, lazy sampling, and analysis. |
| `rtstruct`, `seg` | RT Structure Set and Segmentation read/write plus ROI conversion. |
| `spatialreg` | Affine and deformable spatial registration parsing and application. |
| `sr` | Structured Reporting documents, templates, references, measurement reports, and KOS content. |
| `ultrasound` | Ultrasound region calibration, Doppler measurements, and biometry. |
| `ups` | Unified Procedure Step state, query, subscription, event, and delivery workflows. |
| `waveform` | Calibrated waveform reading, sampling, annotations, and display layout. |
| `qrmatch` | PS3.4 Query/Retrieve matching independent of a database. |

### Networking

| Package | Use it for |
|---|---|
| `net/ul` | Upper Layer PDU codecs, association negotiation, TLS hooks, User Identity items, and bounded servers. |
| `net/dimse` | DIMSE commands, SCU/SCP helpers, Query/Retrieve, normalized services, async sessions, and Storage Commitment. |
| `net/dicomweb` | QIDO-RS, WADO-RS, and STOW-RS client/server primitives with typed errors and finite limits. |
| `net/telemetry` | PHI-safe operational events for UL and DIMSE. |
| `net/audit` | Optional structured audit-event seam for service wrappers. |

Network packages implement protocol mechanics, not deployment policy. DIMSE
defaults to plain TCP unless callers supply TLS configuration. DICOMweb Basic
authentication requires certificate-verified HTTPS. Applications must provide
authorization, protected credential storage, endpoint policy, durable audit
retention, retries, and user-facing job state.

## Commands

Run command help before connecting to a peer:

```sh
go run ./cmd/dcmdump -h
go run ./cmd/dicomweb -h
go run ./cmd/echoscu -h
go run ./cmd/storescu -h
go run ./cmd/findscu -h
go run ./cmd/dicom-go-retrieve -h
go run ./cmd/dicom-go-codify -h
```

The command tree also contains SCP counterparts, UID and dictionary utilities,
codec-manifest tooling, and repository-maintenance generators. Live-peer
interoperability is opt-in; follow the
[Orthanc runbook](./docs/INTEROP_ORTHANC.md) and
[interoperability matrix](./docs/INTEROP_MATRIX.md). Never use real patient data
in repository fixtures or default tests.

## Authoritative documentation

- [Current capability and limitation scope](./docs/CAPABILITIES.md)
- [Architecture and dependency direction](./docs/ARCHITECTURE.md)
- [Validation](./docs/VALIDATION.md), [character sets](./docs/CHARACTER_SETS.md),
  [metadata indexing](./docs/DICOM_METADATA_INDEX.md), and
  [DICOM file-sets](./docs/DICOM_FILE_SETS.md)
- [Codec capability matrix](./docs/CODEC_CAPABILITY_MATRIX.md),
  [codec dependency policy](./docs/CODEC_DEPENDENCY_POLICY.md), and
  [pixel transcoding](./docs/PIXEL_TRANSCODING.md)
- [Asynchronous DIMSE](./docs/ASYNCHRONOUS_OPERATIONS.md),
  [C-STORE sessions](./docs/CSTORE_SESSION.md), and
  [network observability](./docs/NETWORK_OBSERVABILITY.md)
- [DICOMweb client CLI](./docs/DICOMWEB_CLI.md),
  [client response limits](./docs/DICOMWEB_CLIENT.md),
  [embeddable server](./docs/DICOMWEB_SERVER.md), and
  [DICOMweb ownership boundary](./docs/DICOMWEB_EXTRACTION.md)
- [De-identification](./docs/DEIDENTIFICATION.md),
  [Modality Worklist](./docs/MODALITY_WORKLIST.md),
  [Unified Procedure Step](./docs/UNIFIED_PROCEDURE_STEP.md), and
  [Structured Reporting templates](./docs/SR_TEMPLATES_AND_REFERENCES.md)
- [NIfTI export](./docs/NIFTI_EXPORT.md),
  [volumetric presentation state](./docs/VOLUMETRIC_PRESENTATION_STATE.md), and
  [geometry guardrails](./docs/GEOMETRY_GUARDRAILS.md)

## Development

```sh
make fmt-check
make vet
make test
make build
make check
```

`make check` uses the read-only `fmt-check`: malformed source, tool errors, and
unformatted files fail with diagnostics, without rewriting source files.
Use `make fmt` explicitly to apply formatting.

`make race` and `make leak` are opt-in offline concurrency gates documented in
[Concurrency Testing](./docs/CONCURRENCY_TESTING.md). Longer fuzz campaigns are
documented in [Fuzzing](./docs/FUZZING.md). Large parser and Part 10 benchmark
comparisons use the informational, benchstat-based
[large-dataset workflow](./docs/LARGE_DATASET_BENCHMARKS.md). Regenerate the
standard dictionary with `go generate ./dictionary/std`.

## License

Copyright 2026 Thales Matheus Mendonça Santos.

Unless a file states otherwise, this project is licensed under the
[Apache License, Version 2.0](LICENSE). Third-party data and components retain
their own licenses and attribution requirements; see
[third-party notices](THIRD_PARTY_NOTICES.md) and the notices accompanying
individual files.
