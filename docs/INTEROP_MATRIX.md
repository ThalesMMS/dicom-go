# Interop Matrix

External interoperability checks are opt-in. Normal `make test` and CI runs
must stay self-contained and skip peer-dependent tests unless an explicit peer
gate such as `DICOMGO_INTEGRATION=1` or `DICOMGO_HOROS_INTEGRATION=1` is set.

The [separate validation inventory](../../dicom-go-validation/docs/CAPABILITY_AUDIT.md)
records additional qualification scope. Its base evidence runner does not
execute external peer gates; skipped tests never qualify interoperability.

For concurrent SCU probes and full-content Python C-STORE, C-GET and C-MOVE
checks, see [independent DIMSE evidence](../../dicom-go-validation/docs/DIMSE_INDEPENDENT_EVIDENCE.md).
That document records pinned peers, race commands and explicit concurrency limits.

The same pinned Python gate also runs `TestNonPatientProfilesAgainstPynetdicom`
for [Hanging Protocol and Color Palette Q/R](NON_PATIENT_QR.md): both models'
FIND/GET/MOVE contexts, reverse/destination Storage roles, full synthetic dataset
equality and rejection statuses. Complete IOD and optional-key qualification
remain outside that test's scope.

The Python profiles require pydicom 3.0.2, the patched version for
[CVE-2026-32711](https://github.com/advisories/GHSA-v856-2rf8-9f28).
Historical fixture provenance can still identify 3.0.1; it does not authorize
using that older interpreter in the current test profiles.

## Required Tools

The base tests compare objects created in memory with objects read from files
through Part 10, `WriteDataSet`, DICOM JSON and Native XML. Additional metadata
validation is documented in the
[separate validation workspace](../../dicom-go-validation/docs/CAPABILITY_AUDIT.md).
These gates complement the pydicom matrix below and do not require a network peer.

- Go 1.22 or newer.
- Optional external DICOM peer for network checks. The documented local target
  is Orthanc on `ORTHANC_HOST:ORTHANC_PORT`.
- Optional Horos peer for manual lab checks. The documented target is Horos on
  `HOROS_HOST:HOROS_PORT`.
- Optional Storage SCP reachable by the external peer when running C-MOVE.
- Optional sample DICOM files or seeded external studies, depending on the row.
- Python 3.12 with the exact packages in the
  [pinned interop requirements](../scripts/requirements-pydicom-interop.txt)
  for the independent Part 10 gate.

## Environment

Common variables:

- `DICOMGO_INTEGRATION=1`: enables external-peer tests.
- `ORTHANC_HOST`: defaults to `127.0.0.1`.
- `ORTHANC_PORT`: defaults to `4242`.
- `CALLING_AET`: defaults to `DICOMGO`.
- `CALLED_AET`: defaults to `ORTHANC`.
- `MOVE_DEST_AET`: defaults to `DICOMSTORE`.
- `STUDY_UID`: required only for the Study Root C-MOVE row.
- `DICOMGO_HOROS_INTEGRATION=1`: enables Horos-only external-peer tests.
- `DICOMGO_PYNETDICOM_INTEGRATION=1`: enables independent pynetdicom DIMSE
  tests; `DICOMGO_PYTHON` may select the virtual-environment interpreter.
- `DICOM_GO_PYDICOM_PART10=1`: enables the synthetic general Part 10 matrix;
  `DICOM_GO_PYTHON` selects its pinned virtual-environment interpreter.
- `HOROS_HOST`: defaults to `192.168.100.62`.
- `HOROS_PORT`: defaults to `4007`.
- `HOROS_AET`: defaults to `HOROS`.
- `HOROS_CALLING_AET`: defaults to `DICOMGO`.
- `HOROS_TIMEOUT`: defaults to `10s`.

## Matrix

| Area | Check | Command | Default behavior |
|---|---|---|---|
| File parsing | Local Part 10 and dataset parser tests | `go test ./parser ./object ./internal/dcmdump` | Always runs locally. |
| General Part 10 | Bidirectional independent checks for Implicit VR Little Endian, Explicit VR Little Endian and retired Explicit VR Big Endian, including sequences, undefined lengths, UTF-8 text and native Pixel Data | `make pydicom-interop` | Opt-in. Creates only synthetic temporary files and requires exactly pydicom 3.0.2 and NumPy 2.1.3. The same command also runs the existing ISO 2022, RLE and DICOMDIR independent gates. |
| RLE Lossless output | Independent pydicom read and pixel-array comparison of a file produced by `TranscodePath` | `DICOM_GO_PYDICOM_RLE=1 DICOM_GO_PYTHON=/path/to/python go test ./pixeldata -run PydicomRLE -count=1 -v` | Skipped unless explicitly enabled; Python must import pydicom and NumPy. |
| DICOM file-set | Independent pydicom read of an authored multi-patient DICOMDIR | `DICOM_GO_PYDICOM_DICOMDIR=1 DICOM_GO_PYTHON=/path/to/python go test ./dicomdir -run PydicomInterop -count=1 -v` | Skipped unless explicitly enabled; Python must import pydicom. |
| DICOM ISO 2022 text | Bidirectional Part 10 roundtrip for Japanese, Korean, and mixed repertoires through pydicom 3.0.2; Chinese IR 58 is covered by the byte-exact current PS3.5 Annex K fixture because this gate does not qualify independent IR 58 ISO 2022 writing | `DICOM_GO_PYDICOM_CHARSET=1 DICOM_GO_PYTHON=/path/to/python go test ./object -run PydicomISO2022Interop -count=1 -v` | Skipped unless explicitly enabled; the Python environment must contain exactly pydicom 3.0.2. |
| C-ECHO | Verification association against Orthanc | `DICOMGO_INTEGRATION=1 go test ./net/dimse -run '^TestInteropOrthancCEcho$' -count=1` | Skipped unless `DICOMGO_INTEGRATION=1`. |
| C-ECHO | Verification association against Horos | `DICOMGO_HOROS_INTEGRATION=1 go test ./net/dimse -run '^TestInteropHorosCEcho$' -count=1 -v` | Skipped unless `DICOMGO_HOROS_INTEGRATION=1`. |
| C-STORE | Local SCU/SCP integration and CLI smoke coverage | `go test ./net/dimse ./cmd/storescu ./cmd/storescp` | Always runs locally; no external peer required. |
| Study Root C-FIND | Local DIMSE C-FIND SCU/SCP and `findscu` tests | `go test ./net/dimse ./cmd/findscu -run '(C)?Find'` | Always runs locally; external peer row can be added when a stable fixture is available. |
| Study Root C-FIND | Horos Study Root C-FIND using the configured lab node | `DICOMGO_HOROS_INTEGRATION=1 go test ./net/dimse -run '^TestInteropHorosCFindStudyRoot$' -count=1 -v` | Skipped unless `DICOMGO_HOROS_INTEGRATION=1`; empty results are acceptable. |
| Modality Worklist C-FIND | Typed Go SCU against pynetdicom SCP and pynetdicom SCU against streaming Go SCP | `DICOMGO_PYNETDICOM_INTEGRATION=1 DICOMGO_PYTHON=/path/to/python go test ./net/dimse -run '^TestModalityWorklistSC[UP]AgainstPynetdicom$' -count=1 -v` | Skipped unless explicitly enabled; validates multiple matches, nested SPS Sequence and final status in both directions. |
| Study Root C-MOVE | Orthanc C-MOVE using seeded `STUDY_UID` and configured move destination | `DICOMGO_INTEGRATION=1 STUDY_UID=... go test ./net/dimse -run '^TestInteropOrthancCMoveStudy$' -count=1` | Skipped unless `DICOMGO_INTEGRATION=1`; fails fast if `STUDY_UID` is missing. |
| Generic normalized DIMSE | MPPS N-CREATE/N-SET with pynetdicom in both SCU and SCP directions | `DICOMGO_PYNETDICOM_INTEGRATION=1 DICOMGO_PYTHON=/path/to/python go test ./net/dimse -run 'PynetdicomMPPS' -count=1 -v` | Skipped unless `DICOMGO_PYNETDICOM_INTEGRATION=1`; `DICOMGO_PYTHON` is optional when `python3` can import pydicom and pynetdicom. |
| Asynchronous Operations Window | pynetdicom SCU offers `4/3`; Go SCP negotiates asymmetric `3/2` in requestor perspective and serves C-ECHO through `AsyncSession` | `DICOMGO_PYNETDICOM_INTEGRATION=1 DICOMGO_PYTHON=/path/to/python go test ./net/dimse -run '^TestAsyncSessionAgainstPynetdicomNegotiation$' -count=1 -v` | pynetdicom negotiates 0x53 but executes DIMSE synchronously; always-on Go stress/race tests qualify runtime windows greater than one. |
| Unified Procedure Step | Normative message harness over in-memory UL associations for Push/Pull/Watch and Query, including filtered-global subscribe, state/outbox/restart and concurrency tests | `go test ./ups -run 'UPS\|Query\|Watch\|FilteredGlobal\|ServiceClaims' -count=1` | Always runs locally; exercises real N-DIMSE/C-FIND command and dataset messages, bounded existing/future filter materialization, restart persistence and atomic limit rollback without a production worklist or PHI. |
| Storage Commitment Push Model | N-ACTION acceptance followed by a separate-association, partial-failure N-EVENT-REPORT from pynetdicom | `DICOMGO_PYNETDICOM_INTEGRATION=1 DICOMGO_PYTHON=/path/to/python go test ./net/dimse -run '^TestStorageCommitmentWorkflowAgainstPynetdicom$' -count=1 -v` | Skipped unless `DICOMGO_PYNETDICOM_INTEGRATION=1`; validates Event Type 2, role negotiation, callback correlation, and release of the original association. |
| Storage Commitment | Local primitive/service tests plus manual external workflow | `go test ./net/dimse -run 'StorageCommitment|NAction|NEventReport'` | Local tests always run; external peer check is documented manually in `docs/INTEROP_ORTHANC.md`. |

## Part 10 capability and skip profile

The independent Part 10 gate qualifies only semantics that both sides exercise;
opening File Meta Information does not imply that a pixel codec was decoded.
The profile is therefore explicit:

| Representation | Gate status | Capability reference |
|---|---|---|
| Implicit VR Little Endian, Explicit VR Little Endian and retired Explicit VR Big Endian | Covered bidirectionally, including endian-sensitive values. | [`CONFORMANCE.md`](CONFORMANCE.md#supported-transfer-syntaxes) |
| Undefined-length sequences, UTF-8 (`ISO_IR 192`) and native 16-bit Pixel Data | Covered bidirectionally with synthetic, non-PHI values. | [`CONFORMANCE.md`](CONFORMANCE.md#supported-transfer-syntaxes) |
| ISO 2022 Japanese, Korean and mixed repertoires | Covered by the existing pydicom 3.0.2 gate; Chinese IR 58 remains covered by the byte-exact PS3.5 fixture because this gate does not qualify independent IR 58 ISO 2022 writing. | [`CHARACTER_SETS.md`](CHARACTER_SETS.md) |
| RLE Lossless | Covered by the existing pydicom pixel-array comparison and included by `make pydicom-interop`. | [`PIXEL_TRANSCODING.md`](PIXEL_TRANSCODING.md#verification) and [`CODEC_CAPABILITY_MATRIX.md`](CODEC_CAPABILITY_MATRIX.md) |
| JPEG Baseline/Extended/Lossless and JPEG-LS | Explicitly skipped by this Part 10 gate. pydicom pixel decoding depends on separately versioned handlers, while dicom-go qualification is codec- and metadata-subset-specific. | [`CODEC_CAPABILITY_MATRIX.md`](CODEC_CAPABILITY_MATRIX.md) |
| JPEG 2000, HTJ2K and JPEG XL families | Explicitly skipped by this Part 10 gate because release qualification requires the named optional or `codecfull` runtime and its independent corpus. | [`CODEC_CAPABILITY_MATRIX.md`](CODEC_CAPABILITY_MATRIX.md) and [`CODECFULL_PROFILE.md`](CODECFULL_PROFILE.md) |
| Video, JPIP data-bin streams and Deflated Image Frame Compression | Explicitly skipped as unsupported Part 10 pixel roundtrip representations; dataset-level Deflated Explicit VR Little Endian is a distinct supported feature. | [`CONFORMANCE.md`](CONFORMANCE.md#video-jpip-and-streaming-transfer-syntax-policy) |

The skip rows are capability statements, not successful tests. A compressed
syntax moves into this gate only with a pinned independent decoder, a synthetic
redistribution-safe fixture, and the matching codec-profile evidence.

## Pinned pydicom runner

Create an isolated environment and run the complete independent matrix from
`dicom-go`:

```sh
python3 -m venv .interop-pydicom
.interop-pydicom/bin/python -m pip install --requirement scripts/requirements-pydicom-interop.txt
make pydicom-interop PYDICOM_INTEROP_PYTHON=.interop-pydicom/bin/python
```

On Windows, use `.interop-pydicom/Scripts/python.exe`. The Make target verifies
the exact pydicom and NumPy versions before enabling any opt-in Go test. The
same disposable environment can be prepared locally on any supported platform
without adding Python to the ordinary offline baseline. These are local opt-in
commands; no GitHub Actions workflow is required.

## Matrix Runner

`scripts/interop_orthanc_matrix.sh` is the opt-in network runner:

```bash
DICOMGO_INTEGRATION=1 ./scripts/interop_orthanc_matrix.sh
```

It runs C-ECHO first. If `STUDY_UID` is set, it also runs Study Root C-MOVE.
Without `DICOMGO_INTEGRATION=1`, the script exits successfully after printing a
skip message.

Horos uses a separate opt-in gate so it can be run without requiring Orthanc:

```bash
DICOMGO_HOROS_INTEGRATION=1 go test ./net/dimse -run '^TestInteropHoros' -count=1 -v
```

See `docs/INTEROP_HOROS.md` for Horos environment variables, peer setup, and
troubleshooting notes.

## Skip Policy

- Go tests that require the Orthanc peer must call `testutil.SkipIfIntegration`.
- Go tests that require the Horos peer must skip unless
  `DICOMGO_HOROS_INTEGRATION` is set.
- Missing external fixtures should skip or fail with a clear required variable,
  not silently pass a partial external check.
- New external rows must document peer setup, environment variables and expected
  failure modes before being enabled by script.
