# Interop (manual): Horos peer

This document describes the opt-in DIMSE interoperability harness for a Horos
test node. The broader opt-in test matrix is in `docs/INTEROP_MATRIX.md`.

The goal is to validate safe connectivity and already-supported Study Root
query behavior without making a live Horos node a dependency for ordinary CI.

## Why Horos

Horos is a common DICOM viewer/PACS node in macOS imaging workflows. The harness
checks that `dicom-go` can negotiate an association with the configured Horos
AE, perform C-ECHO, and run a Study Root C-FIND query through the existing SCU
path.

## Default target

The current lab target defaults are:

- AE Title: `HOROS`
- Host: `192.168.100.62`
- Port: `4007`

The tests are skipped unless `DICOMGO_HOROS_INTEGRATION` is set.

## Environment

| Variable | Description | Default |
|---|---|---|
| `DICOMGO_HOROS_INTEGRATION` | Enables the Horos interop tests when non-empty. | unset |
| `HOROS_HOST` | Horos DICOM listener host. | `192.168.100.62` |
| `HOROS_PORT` | Horos DICOM listener port. | `4007` |
| `HOROS_AET` | Called AE title sent to Horos. | `HOROS` |
| `HOROS_CALLING_AET` | Local calling AE title used by `dicom-go`. | `DICOMGO` |
| `HOROS_TIMEOUT` | Go duration used for association, DIMSE response, and release timeouts. | `10s` |

## Run the harness

C-ECHO only:

```bash
DICOMGO_HOROS_INTEGRATION=1 \
go test ./net/dimse -run '^TestInteropHorosCEcho$' -count=1 -v
```

Study Root C-FIND only:

```bash
DICOMGO_HOROS_INTEGRATION=1 \
go test ./net/dimse -run '^TestInteropHorosCFindStudyRoot$' -count=1 -v
```

Both Horos checks:

```bash
DICOMGO_HOROS_INTEGRATION=1 \
go test ./net/dimse -run '^TestInteropHoros' -count=1 -v
```

Override the target when needed:

```bash
DICOMGO_HOROS_INTEGRATION=1 \
HOROS_HOST=192.168.100.62 \
HOROS_PORT=4007 \
HOROS_AET=HOROS \
HOROS_CALLING_AET=DICOMGO \
HOROS_TIMEOUT=10s \
go test ./net/dimse -run '^TestInteropHoros' -count=1 -v
```

## C-GET SCP smoke path

The Study Root C-GET SCP callback workflow is covered by a self-contained
loopback smoke path:

```bash
go test ./net/dimse -run '^TestServeStudyRootCGet' -count=1
```

This local smoke does not require `DICOMGO_HOROS_INTEGRATION` and does not
connect to Horos. A live Horos C-GET smoke must remain opt-in under
`DICOMGO_HOROS_INTEGRATION` once a stable Horos query target exists and Horos is
configured to request C-GET from a local `dicom-go` AE that accepts Study Root
C-GET plus same-association C-STORE storage role negotiation.

## Horos configuration requirements

- Horos must be running and listening on the configured host and port.
- The Horos listener must accept the called AE title in `HOROS_AET`.
- Horos network permissions must allow the local `HOROS_CALLING_AET` and source
  host.
- C-ECHO must be enabled for basic verification.
- The C-FIND check requires Horos to accept the Study Root Query/Retrieve
  Information Model - FIND SOP Class.

The C-FIND query uses `QueryRetrieveLevel=STUDY`, an empty `StudyInstanceUID`
matching key, and common return keys. Empty query results are acceptable. A
failed association negotiation, rejected presentation context, non-success
DIMSE status, timeout, or release failure is a test failure.

## CI policy

Do not enable `DICOMGO_HOROS_INTEGRATION` in ordinary CI. The Horos node is a
live peer with network, data, and configuration dependencies outside this
repository. CI should continue to run the self-contained DIMSE tests without a
Horos dependency.

## Troubleshooting

- Association failure: check `HOROS_HOST`, `HOROS_PORT`, `HOROS_AET`, local
  firewall rules, and Horos network listener state.
- Presentation context rejection: check that Horos supports Verification for
  C-ECHO and Study Root FIND for C-FIND.
- C-FIND failure status: inspect the logged DIMSE status and optional error
  comment; Horos may require a different AE permission or query policy.
- Timeout: increase `HOROS_TIMEOUT` only after confirming the peer is reachable.
