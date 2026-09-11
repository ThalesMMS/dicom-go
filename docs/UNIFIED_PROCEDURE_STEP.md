# Unified Procedure Step Push, Watch, Pull and Query

Package `ups` implements the reusable DICOM Unified Procedure Step workflow
defined by PS3.4 Annex CC. It owns the UPS state machine and persistence
contracts; `net/dimse` remains the transport layer. The package does not bundle
a worklist database, scheduler, clinical prioritization rules, AE directory, or
UI.

## Supported SOP Classes

| SOP Class | UID | Implemented operations |
|---|---|---|
| UPS Push | `1.2.840.10008.5.1.4.34.6.1` | N-CREATE, N-GET, Request UPS Cancel N-ACTION. |
| UPS Watch | `1.2.840.10008.5.1.4.34.6.2` | N-GET, Request UPS Cancel, Subscribe, Unsubscribe and Suspend Global Subscription N-ACTION; C-FIND route. |
| UPS Pull | `1.2.840.10008.5.1.4.34.6.3` | N-GET, N-SET, Change UPS State N-ACTION; C-FIND route. |
| UPS Event | `1.2.840.10008.5.1.4.34.6.4` | N-EVENT-REPORT SCU delivery and optional SCP event handler. |
| UPS Query | `1.2.840.10008.5.1.4.34.6.5` | C-FIND route. |

For normalized operations, the command SOP Class UID is UPS Push even when the
accepted presentation context is Pull, Watch, or Event. C-FIND request and
response command sets use the negotiated Pull, Watch, or Query SOP Class; the
SOP Class UID returned inside each response Identifier is UPS Push.
`PresentationContext` and
`PresentationContexts` build stable proposals for Explicit and Implicit VR
Little Endian.

`Service.NormalizedOptions(assoc)` returns the normalized SCP handlers for one
borrowed association. Use `dimse.CombineNormalizedSCPOptions` when UPS shares an
association with another normalized service such as Storage Commitment. A
single `ServeAssociation` or `Dispatcher` must remain the sole reader of an
association; do not run a `NormalizedClient` receive loop concurrently on that
same association.

## State and ownership rules

- N-CREATE creates only `SCHEDULED` UPS instances. A duplicate SOP Instance UID
  returns `0111`. All SCU Type 2 attributes are required; conditional
  Study/Patient IDs that are Type 2 for the SCP are created zero-length when
  the condition does not apply. A present Worklist Label may be zero-length and
  is then assigned the configured default; an absent Worklist Label returns
  `0120`. Progress and performed-procedure sequences must initially be empty.
  Populated content-item, referenced-request, patient-identifier, and referenced-
  instance/access sequences are validated against their UPS macros. Replaced
  Procedure Step Sequence items are accepted at N-CREATE and validated as SOP
  Instance References. Attributes outside the documented UPS creation subset
  fail closed with `0105`.
- `SCHEDULED` can be claimed only by Change UPS State to `IN PROGRESS` with a
  valid new Transaction UID. The atomic repository CAS permits exactly one
  claimant.
- N-SET and final state changes on an `IN PROGRESS` step require the exact
  Transaction UID. The Transaction UID is never returned by N-GET.
- `IN PROGRESS` can become `COMPLETED` or `CANCELED` only after the required
  final-state attributes are present. Terminal steps reject later mutation.
  `BuildPerformedProcedure` constructs the performed-procedure macro for
  completion, and `BuildDiscontinuationProgress` constructs the coded reason
  and cancellation DateTime macro required before performer discontinuation.
- Request UPS Cancel on an `IN PROGRESS` step records and emits the request but
  does not itself cancel the step. A request on a `SCHEDULED` step causes the
  SCP performer transition required by Annex CC and emits the intermediate and
  terminal state reports.
- `Step`, `Event`, `Subscription`, and `Delivery` values returned by stores are
  detached. Request datasets and inbound event datasets are borrowed only for
  the documented synchronous call.

The service returns PHI-free typed errors. `StatusError.Status` contains the
UPS/DIMSE status. The wrapped cause is available to the direct caller through
`errors.Is`/`errors.As`, but is not interpolated by `Error()`.

## Persistence boundary

`Store` embeds five separate responsibilities: step snapshots, subscriptions,
events, delivery claims, and `AtomicCommitter`. `CommitUPS` is the required
transaction boundary. A state or subscription mutation and every resulting
event/outbox delivery must become durable atomically; an implementation based
on independent `SaveStep` and `SaveEvent` calls is not conformant to this API.

Persistent implementations must provide:

- versioned compare-and-swap for step claims and mutations;
- detached input/output values;
- stable, unique event and delivery IDs;
- due-delivery claims with opaque leases and CAS completion;
- atomic recipient snapshots so later unsubscribe cannot erase an event that
  was already committed; and
- a bounded distinct-AE projection for restart/status fan-out, without loading
  every specific subscription into the workflow (`ListActiveReceivingAETitles`);
- durable records that can be resumed after process restart; and
- the canonical, detached `Subscription.Filter` on a filtered global
  subscription. The filter is repository state, not process-local compiled
  state, and must survive restart so matching future UPS instances continue to
  inherit the subscription; and
- explicit opt-in through `FilteredGlobalSubscriptionCommitter`. Stores that
  do not implement this complete atomic materialization/inheritance contract
  reject filtered-global subscribe with `C307` instead of silently accepting a
  filter they cannot persist or apply. Implementations can call
  `SubscriptionFilter.Matches` to reuse the package's C-FIND semantics.

`MemoryStore` is a bounded, process-local reference. Its single mutex
demonstrates the required state-plus-outbox atomicity and deterministic lease
behavior. It is not a production database.

## Watch and callback delivery

Specific subscribe always commits an initial state event. Unfiltered and
filtered global subscriptions materialize per-instance instructions for
existing UPS instances and arrange inheritance for future instances. A deletion
lock commits initial state events for matching existing instances; without a
lock, existing instructions are materialized without that initial fan-out.
Ordinary events from a future matching UPS are delivered through its inherited
per-instance instruction. Specific instructions override the global
instruction.

Filtered global subscribe uses the well-known SOP Instance UID
`1.2.840.10008.5.1.4.34.5.1`. Its matching keys use the same scalar tag registry
and value matchers as UPS C-FIND: exact text, `*`/`?` wildcard outside UI,
case-insensitive CS, and DA/TM/DT range matching with the declared timezone.
Only the scalar attributes in the [Pull and Query subset](#pull-and-query-subset)
are predicates. Sequence matching is rejected; Specific Character Set and
Timezone Offset From UTC may supply matching context but do not themselves
count as a predicate. At least one actual matching predicate is required.

The canonical filter is bounded to 16 keys, 16 values per key, and 4 KiB total
matching-value bytes. Materializing existing instances scans at most
`Limits.MaxSubscriptionFilterScanned` steps (100,000 by default) and remains
subject to the repository fan-out and storage limits. A limit failure returns
`0213` and leaves the subscription, per-instance instructions, events, and
outbox unchanged. An unknown or malformed argument tag returns `0114`; a
duplicate key, wrong VR, empty or invalid value, sequence predicate, or missing
actual predicate returns `0115`. Supplying matching keys with a specific or
unfiltered-global SOP Instance UID returns `C314`.

Unsubscribe using the filtered-global SOP Instance UID removes only that
filtered-global instruction. The broader global Unsubscribe action uses the
unfiltered global SOP Instance UID and removes both forms of global instruction
plus the Receiving AE's materialized specific instructions. Suspend Global Subscription removes only future
inheritance, whether filtered or unfiltered, and preserves materialized
specific instructions. Neither action removes delivery rows already committed
to the durable outbox. Recreating `Service` over the same durable `Store`
retains the canonical filter, future inheritance, event identity, attempts, and
pending delivery state. `RefuseDeletionLocks` can downgrade a requested lock
and reports `B301`.

Subscribe, Unsubscribe, and Suspend Global Subscription distinguish a missing
Type 1 Receiving AE/Deletion Lock (`0120`) from a present zero-length value
(`0121`) before applying subscription policy.

The application supplies `CallbackResolver`, which maps only the authorized
Receiving AE Title to an address and `ul.DialOptions`. The source association's
remote address is never used as a callback target. The workflow owns every
association returned by `AssociationDialer`: success releases and closes it;
failure aborts and closes it. An Event presentation context is added when the
resolver did not provide one. TLS configuration is cloned and TLS failures
never fall back to plaintext.

Delivery is at-least-once. A transport result that cannot prove peer receipt is
retried after a deterministic bounded backoff, so subscribers must treat the
stable event/delivery identity idempotently. A DIMSE failure status is terminal
by default. Cancellation stops the current attempt, records a retryable safe
failure class, and does not roll back the already committed UPS mutation.
`AttemptTimeout + CleanupTimeout` must remain below `LeaseDuration`, preventing
a second worker from reclaiming an attempt that is still active.

UPS Assigned Event Type 5 flattens each scheduled human performer into the
normative top-level Human Performer Code Sequence and Human Performer's
Organization attributes; the Scheduled Human Performers wrapper itself is not
placed in Event Information. Clearing all assignment attributes emits an empty
Event Type 5 dataset because every Event Information attribute is conditional
on still being populated.

`ReportSCPStatusChange` atomically queues Event Type 4 for the de-duplicated
union of `FallbackReceivingAETitles` and every active specific/global
subscriber. Restart reports validate the normative warm/cold list-status
values (`WARM START` or `COLD START`). Attempt count is persisted when a
delivery lease is claimed, so a crash during a callback cannot bypass
`MaxAttempts`. A worker claims one delivery immediately before its network
attempt; `MaxBatch` bounds the number attempted in one pass without allowing
later leases to expire in a local queue.

The in-memory global fan-out is one bounded atomic transaction controlled by
`MemoryStoreOptions.MaxFanOut`; exceeding it fails before mutation. A production
repository with a larger population should implement a durable paginated
expansion job behind the same atomic commit/outbox contract rather than raising
this process-local cap without an operational memory budget.

## Pull and Query subset

`Query`, `QueryKey`, `Match`, and `ReturnKey` preserve absent, matching, and
present-empty universal/return-key states. `Service.QueryRoutes` returns exact
streaming routes for Pull, Watch, and Query. `QueryClient` invokes a synchronous
callback for each pending Identifier and supplies socket backpressure.

The implemented top-level matching/return attributes are:

- Specific Character Set and Timezone Offset From UTC (declarations and
  conditional output, never matching keys);
- SOP Class UID, SOP Instance UID, Study Instance UID;
- Patient Name, Patient ID, Issuer of Patient ID, Patient Birth Date, Patient
  Sex and Admission ID;
- Scheduled Procedure Step Priority, Procedure Step Label, Worklist Label;
- Scheduled Station Name/Class/Geographic Location Code Sequences, Scheduled
  Human Performers Sequence, Scheduled Start DateTime, Expected Completion
  DateTime and Scheduled Workitem Code Sequence;
- Input Readiness State, Modification DateTime and Procedure Step State; and
- Progress Information and Performed Procedure Sequences.

Primitive text supports exact matching, `*`/`?` wildcard matching outside UI,
case-insensitive CS matching, and DA/TM/DT range matching by temporal meaning.
Timezone Offset From UTC declarations are applied to values without an embedded
offset before comparison. Per PS3.4 C.2.2.2, a hyphen in a DA/TM/DT query value
selects Range Matching; an application that starts from a local DT with a
negative offset must convert it to the equivalent `+0000` value before using
Single Value Matching. Sequence keys are currently universal return keys only;
non-empty sequence matching is rejected as an Identifier mismatch. This is an
explicit partial capability, not silent sequence matching. A query with no
matching key returns zero matches. Responses contain only requested keys plus
required character-set or timezone context; declarations themselves never
cause `FF01`, while a missing optional return key does.

`dimse.StreamingCFindClient` sends C-CANCEL and drains the final response after
local cancellation, result limits, callback errors, or callback panic. A failed
drain marks the association state uncertain and follows `OperationErrorPolicy`.
If a correlated operation completes immediately before its C-CANCEL arrives,
the association dispatcher validates and ignores that stale C-CANCEL so the
association remains reusable.
The SCP serializes the complete command-plus-dataset response, invalidates a
retained yield after the provider returns, and applies byte/element/depth/match
limits before sending.

## Limits and security

All zero-value limits normalize to finite defaults. Deployments should choose
explicit budgets for dataset bytes/elements/depth, CAS attempts, steps,
subscriptions, events, deliveries, subscriber fan-out, scanned query steps,
filtered-subscription scans (`MaxSubscriptionFilterScanned`), matches, callback
attempts/backoff, attempt duration, lease duration and cleanup duration. SCP
status-change fan-out is separately bounded by `MaxStatusRecipients`. Filter
shape is additionally capped at 16 keys, 16 values per key, and 4 KiB of total
matching-value data.

`C307` is returned when the configured Store does not implement the explicit
filtered-global persistence capability. It is not used for an individual bad
key or value.

The package does not authenticate an AE Title, authorize UPS actions, provide
ATNA output, or protect PHI at rest. Applications must wrap the handlers with
their authorization/audit policy and use encrypted durable storage where
required. Tests use synthetic UIDs and values only.

## Validation

The always-on normative harnesses use in-memory UL associations but exchange
real N-CREATE/N-GET/N-SET/N-ACTION and streaming C-FIND command and dataset
messages:

```sh
go test ./ups -run 'UPS|Query|Watch|FilteredGlobal|ServiceClaims' -count=1
go test -race ./ups ./net/dimse -run 'UPS|StreamingCFind|Normalized|FilteredGlobal' -count=1
```

They cover concurrent claim, stale Transaction UID, final-state immutability,
cancel-request semantics, restart over a caller-owned store, durable delivery
failure, Push/Pull/Watch round trips, Pull/Query streaming, filtered-global
existing/future materialization across restart, outbox preservation,
concurrent subscribe/create, resource-limit rollback, and malformed
Identifiers. No production PACS or patient data is required.
