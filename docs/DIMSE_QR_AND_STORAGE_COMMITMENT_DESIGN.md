# Design notes: Query/Retrieve, normalized DIMSE and Storage Commitment

This document captures the design scope for **Query/Retrieve**
(C-FIND/C-MOVE/C-GET), the generic **normalized DIMSE** layer and **Storage
Commitment Push Model** (N-ACTION/N-EVENT-REPORT) in `dicom-go`.

It is a design/roadmap document: it describes intended behavior and boundaries, not a claim of conformance.

## Goals

- Provide a clear, minimal, interoperable starting point for:
  - Study Root Query/Retrieve Information Model (FIND/MOVE/GET)
  - Patient Root Query/Retrieve Information Model (FIND/MOVE/GET)
  - Modality Worklist Information Model - FIND
  - All six normalized DIMSE services as reusable protocol primitives
  - Storage Commitment Push Model
- Keep the implementation small and explicit:
  - Prefer deterministic behavior, strict defaults, and tight configuration.

## Non-goals (initial implementation)

- Full DICOM conformance statement.
- UPS, retired Patient/Study Only, Color Palette, or other specialized query
  models beyond the separately documented Modality Worklist profile.
- Asynchronous operation windows greater than one. Initial APIs keep one
  outstanding DIMSE operation per association unless a dispatcher explicitly owns
  that association.
- Service-class-specific extended negotiation semantics beyond raw UL item
  negotiation.
- TLS/security profiles.
- Full storage codec/transcoding support (transfer syntax negotiation only; no transcoding).

## Initial SOP Class scope

### Query/Retrieve

Study Root Query/Retrieve Information Model:

- FIND: `1.2.840.10008.5.1.4.1.2.2.1`
- MOVE: `1.2.840.10008.5.1.4.1.2.2.2`
- GET: `1.2.840.10008.5.1.4.1.2.2.3`

Patient Root Query/Retrieve Information Model:

- FIND: `1.2.840.10008.5.1.4.1.2.1.1`
- MOVE: `1.2.840.10008.5.1.4.1.2.1.2`
- GET: `1.2.840.10008.5.1.4.1.2.1.3`

Supported Patient Root helper levels are `PATIENT`, `STUDY`, `SERIES` and
`IMAGE`. Retired Patient/Study Only and Color Palette Query/Retrieve models are
not part of the current release scope.

Modality Worklist Information Model - FIND:

- FIND: `1.2.840.10008.5.1.4.31`
- Single-level model with exactly one Scheduled Procedure Step item per match.
- Typed streaming SCU/SCP and optional in-memory matching are documented in
  `docs/MODALITY_WORKLIST.md`.

### Storage Commitment

- Storage Commitment Push Model SOP Class: `1.2.840.10008.1.20.1`

## Association negotiation

### AE Titles

- Calling AE (local) and Called AE (remote) are configurable.
- For C-MOVE, **Move Destination AE Title** is provided in the request.

### Presentation contexts and transfer syntaxes

- Propose one presentation context per SOP Class.
- Default transfer syntax proposal list:
  - Explicit VR Little Endian (`1.2.840.10008.1.2.1`)
  - Implicit VR Little Endian (`1.2.840.10008.1.2`)

Identifiers and normalized-service datasets are encoded/decoded using the
transfer syntax accepted for their presentation context.

For ordinary SOP Classes, the command SOP Class UID must match the selected
presentation-context abstract syntax. Meta SOP Classes are the deliberate
exception: callers must explicitly select the Meta SOP Class context or install
an SCP validation policy rather than disabling the check globally.

### Role selection

- For pure SCU usage, SCU role is sufficient.
- For **C-GET**: the SCU must also accept **C-STORE sub-operations on the same association**, so the local side must be willing to act as **Storage SCP** for the negotiated Storage SOP Classes.
- For **C-MOVE**: C-STORE sub-operations occur on a separate association initiated by the remote system to the configured Move Destination; this requires running a C-STORE SCP at the Move Destination.
- The UL layer can now propose and accept SCP/SCU Role Selection items and
  exposes requested/accepted roles on `ul.Association`. DIMSE service helpers
  still need to enforce service-specific role requirements.

### SOP Class Extended Negotiation

- The UL layer can propose raw SOP Class Extended Negotiation items and route
  requested items through an accept-side handler.
- The accept-side policy is explicit: accept with response data, ignore, or
  reject the association. Unknown user-information sub-items remain preserved at
  PDU level but ignored by association negotiation.
- Service-class-specific meaning for extended negotiation bytes remains a DIMSE
  service concern.

### Multiple accepted contexts

If multiple contexts are accepted for a SOP Class, the implementation uses the **first accepted** context.

## Operation runtime policy

The service design is synchronous at the association level: one outstanding
DIMSE operation may own an association at a time. Response channels for pending
C-FIND or C-MOVE results are local delivery helpers, not permission to send a
second independent operation on the same association.

The shared operation executor for SCU helpers guards one active operation per
association, accepts a context plus overall
and per-response timeouts through `OperationOptions`, reports local cancellation,
response timeout and uncertain association state through typed errors, and makes
release-vs-abort behavior explicit after uncertain errors.

The bounded SCP service queue provides these controls for server workflows:

- maximum concurrent associations;
- maximum active operations;
- queue depth and enqueue timeout;
- deterministic behavior when the queue is full; and
- shutdown/cancellation behavior for queued work.

`dimse.ServiceQueue` provides reusable worker slots, bounded queue depth,
enqueue timeout and clean shutdown semantics. `cmd/echoscp` exercises the policy
first: if capacity is exhausted after accepting an association but before
accepting a command, it aborts the affected association with a clear diagnostic.
Future service paths should return the service-specific refusal or
out-of-resources status when the protocol path supports it; otherwise they
should abort the affected association. Automatic retry remains a caller/service
policy decision, not a default DIMSE command behavior.

The shared dispatcher routes normalized requests by command field, and SCU
responses are correlated by Message ID. Asynchronous operation windows greater
than one remain outside this layer; each `NormalizedClient` serializes confirmed
operations on an association.

## Generic normalized DIMSE layer

The `Normalized*Request` and `Normalized*Response` types cover N-EVENT-REPORT,
N-GET, N-SET, N-ACTION, N-CREATE and N-DELETE without embedding MPPS, UPS,
Print Management or Storage Commitment state. `NormalizedClient` sends the six
confirmed operations; `NormalizedSCPOptions` supplies matching handler callbacks
to `ServeAssociation` or the existing `Dispatcher`.

The wire rules follow [DICOM PS3.7 Chapter 10](https://dicom.nema.org/medical/dicom/current/output/chtml/part07/chapter_10.html),
including the [Annex C status encodings](https://dicom.nema.org/medical/dicom/current/output/chtml/part07/chapter_C.html).

The layer preserves optional command status fields and follows these dataset
rules:

- N-GET request and N-DELETE request/response do not carry a dataset.
- N-SET request always carries a Modification List.
- N-EVENT-REPORT, N-ACTION and N-CREATE requests may carry their service-defined
  datasets; their responses and N-SET responses may also carry datasets.
- A successful N-GET response carries an Attribute List.
- On decode, `CommandDataSetType == 0x0101` is absent and every other value is
  present. Encoding uses `0x0000` for present datasets.

Status `0x0000` is success. Annex C warnings (`0x0001`, `0x0107`, `0x0116` and
`0xB000`-`0xBFFF`) are surfaced as typed warnings; other values are surfaced as
typed failures while preserving Error Comment, Error ID, Offending Element and
Attribute Identifier List when supplied. Response datasets are preserved on
warnings and failures because Annex C uses them for invalid argument/attribute
detail as well as for successful replies. A missing SCP handler returns
`0x0211` (Unrecognized Operation).

## QR workflow sketches

### C-FIND (Study Root)

1. Establish association (QR FIND presentation context).
2. Send C-FIND-RQ with Identifier dataset.
3. Read C-FIND-RSP loop:
   - Pending responses may include a response Identifier.
   - Final response indicates Success/Cancel/Warning/Failure.

### Modality Worklist C-FIND

1. Negotiate the Modality Worklist FIND presentation context.
2. Build a single-level Identifier that preserves absent, universal/return and
   matching values, including the one-item Scheduled Procedure Step Sequence.
3. Stream pending `FF00`/`FF01` Identifiers through a synchronous callback.
4. On local cancellation or match limit, send C-CANCEL and drain `FE00` (or
   another final response) before reusing the association.

The SCP uses an exact SOP Class router rather than treating MWL as Study Root.
Its provider callback yields one worklist item at a time, and byte/element/depth
limits are checked before pending response output. See
`docs/MODALITY_WORKLIST.md` for the matching matrix and unsupported-key policy.

### C-MOVE (Study Root)

1. Establish association (QR MOVE presentation context).
2. Send C-MOVE-RQ with:
   - Query/Retrieve Level key(s) in Identifier
   - Move Destination AE Title
3. Read C-MOVE-RSP loop:
   - Pending responses include sub-operation counters.
4. Final response indicates overall status.

Notes:

- The actual instances are transferred on a different association (remote → move-destination) using C-STORE.
- Study Root C-MOVE includes an SCU receive loop and a scoped SCP callback
  workflow. The SCP handler validates or resolves the Move Destination AE,
  returns matching storage sub-operations and reports remaining/completed/failed
  and warning counters. It does not include a built-in archive or destination
  registry.
- Patient Root C-MOVE uses the same SCU/SCP workflow shapes and callback
  contracts, but accepts `PATIENT`, `STUDY`, `SERIES` and `IMAGE` query levels.

### C-GET (Study Root)

1. Establish association with:
   - QR GET presentation context, and
   - Storage SOP Classes presentation contexts with SCP role enabled.
2. Send C-GET-RQ with Identifier dataset.
3. Peer issues one or more C-STORE-RQ sub-operations on the same association.
4. Read C-GET-RSP loop until final status.

Notes:

- Study Root C-GET now has an SCU helper with a scoped receive loop that accepts
  interleaved C-STORE requests, invokes a caller-provided store callback, sends
  C-STORE responses and then continues reading C-GET responses.
- Study Root and Patient Root C-GET SCP wrappers use caller-provided callbacks
  to resolve sub-operations, send same-association C-STORE requests and report
  C-GET remaining/completed/failed/warning counters.
- The helper requires accepted Storage SOP Class contexts with SCP role enabled
  for the requester. It is not a production archive.

### Patient Root wrappers

Patient Root C-FIND, C-MOVE and C-GET reuse the same handler interfaces as the
Study Root wrappers. The wrappers differ only in SOP Class UID validation and
Query/Retrieve level validation:

- Patient Root accepts `PATIENT`, `STUDY`, `SERIES` and `IMAGE`.
- Study Root accepts `STUDY`, `SERIES` and `IMAGE`.

The model wrappers deliberately avoid archive/index policy. Applications own
identifier matching, destination lookup and storage object loading through the
existing callback APIs.

## Storage Commitment workflow sketches

### Push Model (SCU + SCP notification handling)

1. Association A (SCU → Storage Commitment SCP)
   - Send N-ACTION-RQ with transaction UID and Referenced SOP Sequence.
   - Receive N-ACTION-RSP.
2. Association B (SCP → SCU-as-SCP)
   - Remote sends N-EVENT-REPORT-RQ to deliver results.
   - Local returns N-EVENT-REPORT-RSP.

Notes:

- `ServeStorageCommitmentSCP` implements the N-ACTION accept path for one
  request on an accepted Storage Commitment presentation context. It parses
  Transaction UID and Referenced SOP Sequence, calls application code and sends
  N-ACTION-RSP success or failure status.
- `BuildStorageCommitmentActionInformation` and
  `ParseStorageCommitmentActionInformation` cover N-ACTION datasets.
- `BuildStorageCommitmentEventInformation` and
  `ParseStorageCommitmentEventInformation` cover N-EVENT-REPORT result datasets,
  including per-instance failures through Failed SOP Sequence and Failure Reason.
- `StorageCommitmentWorkflow` persists a request before N-ACTION success,
  validates that a result is the exact disjoint partition of that request, and
  uses versioned CAS plus expiring leases for restart-safe processing and
  callback delivery. It supports Event Type 1 (all success) and Event Type 2
  (failures exist).
- Same-association delivery is an exclusive-reader sequence through
  `ServeAction` and `DeliverOnAssociation`. Separate callback delivery uses an
  application allowlist resolver, mandatory role selection, frozen TLS/AE
  policy, bounded retry, and `ServeListener`/`NormalizedOptions` on the receiver.
- See `STORAGE_COMMITMENT_WORKFLOW.md` for the injected store contract, states,
  ownership, retention, and manual/external mode.

## Status handling

### Retrieve status buckets

Interpret response **Status (0000,0900)**:

- Pending: typically `0xFF00`.
- Success: `0x0000`.
- Cancel: `0xFE00`.
- Warning: any warning status as defined by the service class.
- Failure: anything else.

Preserve raw status for diagnostic/error reporting.

Retrieve responses may include (informational):

- (0000,1020) Remaining
- (0000,1021) Completed
- (0000,1022) Failed
- (0000,1023) Warning

### Storage Commitment status buckets

Interpret N-ACTION / N-EVENT-REPORT response Status:

- Success: `0x0000`
- Warning: warning statuses (implementation-defined)
- Failure: any other non-zero

For N-EVENT-REPORT, parse the result dataset for per-instance outcomes.

## Configuration knobs (intended API surface)

- Remote host/port
- Calling AE title, Called AE title
- Max PDU length
- Timeouts:
  - association dial/handshake
  - per-response DIMSE timeout
  - overall operation timeout (caller-managed or option)
- Operation/runtime policy:
  - one active operation per association by default
  - release or abort policy after cancellation/timeouts
  - queue depth and enqueue timeout for SCP services
  - maximum concurrent associations and active operations
- For C-MOVE:
  - move destination AE title
- Optional limits:
  - maximum pending responses processed
  - maximum dataset size for identifiers

## Testing strategy

- Unit tests for:
  - command field encoding/decoding
  - status bucket mapping
  - response loop semantics (pending → final)
- Interop harness (documented) against an external peer (Orthanc or pynetdicom).
  The normalized layer has an opt-in MPPS N-CREATE/N-SET test in both SCU and
  SCP directions against pynetdicom. Modality Worklist also runs in both SCU
  and SCP directions against pynetdicom; see `docs/INTEROP_MATRIX.md`.

## Known risks / complexities

- Interleaved unrelated operations and asynchronous windows greater than one
  still require a higher-level association owner.
- Storage Commitment requires transaction UID correlation and potentially a listener for inbound N-EVENT-REPORT.
- Service-specific use of role selection and extended negotiation may need
  refinement as C-GET and broader Storage Commitment workflows are added.
