# Interop (manual): Orthanc peer

This document describes a reproducible setup to test `dicom-go` Query/Retrieve against an external peer.
The broader opt-in test matrix is in `docs/INTEROP_MATRIX.md`.

The goal is to validate the **scoped C-MOVE (Study Root)** workflow.

Storage Commitment interop is documented as a manual, opt-in workflow at the end
of this file. There is no automated external-peer Storage Commitment harness in
this repository.

## Why Orthanc

Orthanc is easy to run locally and can act as a Study Root C-MOVE SCP and Storage SCP.

## Run Orthanc

### Option A: local install

Install Orthanc for your OS, then configure the DICOM port and known peers.

### Option B: Docker (example)

A minimal example (adjust to your environment):

```bash
docker run --rm -it \
  -p 4242:4242 \
  -p 8042:8042 \
  jodogne/orthanc:latest
```

You will likely need to provide an `orthanc.json` to configure:

- the AE Title
- the DICOM port
- known modalities / peers (to allow C-MOVE destinations)

Refer to Orthanc documentation for the exact configuration schema.

## Configure AE titles

Recommended (defaults used by scripts):

- Orthanc (server):
  - AE Title: `ORTHANC`
  - Host: `127.0.0.1`
  - Port: `4242`
- dicom-go retrieve SCU:
  - Calling AE Title: `DICOMGO`
- Move destination (Storage SCP):
  - AE Title: `DICOMSTORE`
  - Must be reachable by Orthanc and listed/allowed in Orthanc configuration.

Important: **C-MOVE transfers instances on a separate association** from Orthanc → Move Destination.
So you must run a Storage SCP at `DICOMSTORE` (not provided by dicom-go yet).

## Seed Orthanc with test data

Upload at least one study/series/instance into Orthanc (e.g., via its UI on port 8042 or REST API).
Then note a `StudyInstanceUID` to retrieve.

## Run the interop script

```bash
export ORTHANC_HOST=127.0.0.1
export ORTHANC_PORT=4242
export CALLING_AET=DICOMGO
export CALLED_AET=ORTHANC
export MOVE_DEST_AET=DICOMSTORE
export STUDY_UID=1.2.840....

./scripts/interop_retrieve_orthanc.sh
```

To run the current Orthanc matrix entry points:

```bash
DICOMGO_INTEGRATION=1 ./scripts/interop_orthanc_matrix.sh
```

The matrix runner always runs C-ECHO. It runs C-MOVE only when `STUDY_UID` is
set.

## Expected outcomes

- The script should complete with a final **C-MOVE-RSP status success** (`0x0000`).
- Orthanc should open a C-STORE association to the Move Destination AE and transfer instances.
- If Move Destination is not configured/available, Orthanc will return a failure (often `0xA801` Move Destination unknown).

## Troubleshooting

- If the association is rejected:
  - check Called AE Title (`ORTHANC`)
  - check the Orthanc DICOM port (`4242`)
  - check that the QR MOVE SOP class is enabled/allowed
- If C-MOVE fails with destination unknown:
  - ensure `MOVE_DEST_AET` is defined as a known peer in Orthanc
  - ensure the destination host/port is reachable from Orthanc

## Storage Commitment manual check

The library exposes Storage Commitment Push Model primitives and the persistent
`StorageCommitmentWorkflow`. External peer support and configuration vary, so
the default offline suite uses independent local associations and this
repository does not require a live peer.

For a manual check with a peer that supports the Push Model:

1. Negotiate `1.2.840.10008.1.20.1` with Implicit VR Little Endian or another
   transfer syntax accepted by both sides.
2. Send N-ACTION-RQ to `1.2.840.10008.1.20.1.1` with Action Type ID `1` and an
   action information dataset built with
   `BuildStorageCommitmentActionInformation`.
3. Treat a successful N-ACTION-RSP as request acceptance only.
4. Release the original association, then receive the eventual callback on a
   separately negotiated association using the same committing AE Title and
   Storage Commitment SCP role selection.
5. Confirm Event Type 1 for all-success and Event Type 2 for a partial-failure
   fixture. Verify every requested SOP reference appears exactly once across the
   success and failed sequences, including the expected Failure Reason.
6. Repeat the identical event after simulating a lost response. It must receive
   success without invoking the result consumer twice.
7. Restart the workflow process while retaining its injected persistent store;
   `ProcessDue`/`DeliverDue` must resume the accepted or ready transaction.

Applications still supply the database, callback AE directory, TLS credentials,
commitment decision, audit sink, worker schedule, and deletion policy. See
`STORAGE_COMMITMENT_WORKFLOW.md` for the exact integration contract.

The automated opt-in independent-peer gate uses pynetdicom:

```sh
DICOMGO_PYNETDICOM_INTEGRATION=1 \
  go test ./net/dimse -run '^TestStorageCommitmentWorkflowAgainstPynetdicom$' -count=1 -v
```

Set `DICOMGO_PYTHON` when the Python environment containing `pydicom` and
`pynetdicom` is not available as `python3`.
