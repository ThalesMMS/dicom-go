# Network observability and operational controls

`net/telemetry`, `net/ul`, and `net/dimse` expose one framework-neutral seam
for wire metadata, association lifecycle, DIMSE command summaries, bounded SCP
admission, and shutdown. All options are additive; the zero value keeps the
existing silent transport behavior.

## Safe event model

Every negotiated association receives a cryptographically random opaque ID.
The ID is stored on `ul.Association`, not in its context, so it remains stable
when an application replaces `Association.Context`.

`telemetry.Event` is a closed struct. It can carry PDU type and length,
presentation context, SOP Class/transfer syntax, command field, status category,
duration, byte counts, lifecycle, and a safe error class. It has no raw error,
arbitrary map, command object, Message ID, SOP Instance UID, query identifier,
or dataset field. The default endpoint policy omits addresses and AE Titles.
Callers may explicitly choose plaintext or domain-separated HMAC-SHA256 through
`telemetry.EndpointPolicy`.

Observers are asynchronous and bounded. `telemetry.Dispatcher` preserves order
within its queue, drops new events when full, recovers observer panics, and
reports drop/panic counters. A server shares one dispatcher across associations,
so a blocked observer does not create one blocked worker per connection.

```go
dispatcher := telemetry.NewDispatcher(mySink, 256)
defer dispatcher.Close()

observability := &ul.ObservabilityOptions{
    Dispatcher: dispatcher,
    EndpointPolicy: telemetry.EndpointPolicy{
        AETitles: telemetry.EndpointHMACSHA256,
        Addresses: telemetry.EndpointHMACSHA256,
        HMACKey: deploymentSecret,
    },
}
```

## Raw PDU capture

Raw capture is disabled by default and is a separate API from high-level
events. It can contain patient identifiers, query keys, clinical metadata, and
pixel data. DICOM User Identity may contain passwords, Kerberos tickets, SAML
assertions, or JWTs; therefore A-ASSOCIATE bytes are never sent to the raw sink.
Only P-DATA-TF is eligible.

Enabling raw capture requires all of:

- an explicit `RawPDUSink` or shared `RawPDUDispatcher`;
- a positive per-PDU capture limit;
- a positive per-association capture budget;
- application-owned access control, transport, retention, and deletion policy.

The library copies at most the configured limit and marks truncated captures.
Delivery is bounded, non-blocking for the protocol path, panic-isolated, and
accounted. Do not route raw captures to ordinary application logs.

## Bounded server and shutdown

`ul.AssociationServer` bounds sockets before spawning negotiation/handler
goroutines. The limit includes silent negotiations and established handlers.
`SaturationBackpressure` stops accepting until a slot is free;
`SaturationReject` accepts one excess connection at a time and, after a valid
A-ASSOCIATE-RQ, returns transient RJ / Presentation Provider / local-limit-
exceeded (result 2, source 3, reason 2).

`Shutdown(ctx)` first stops admission and lets handlers drain. At the deadline
it cancels handler contexts, marks active associations aborted, and closes their
connections. Handler callbacks must cooperate with context cancellation. Go
cannot safely kill a callback that ignores its context; after forced shutdown
such a callback has no usable association stream, but application code still
owns any detached work it created.

```go
server, err := ul.NewAssociationServer(listener, ul.AssociationServerOptions{
    Accept: ul.AcceptOptions{
        AETitle:       "MY_SCP",
        Observability: observability,
    },
    MaxConcurrentAssociations: 100,
    Handler: func(ctx context.Context, assoc *ul.Association) error {
        return dimse.ServeAssociation(ctx, assoc, dimse.AssociationSCPOptions{
            Controls: dimse.SCPControls{OperationTimeout: 5 * time.Minute},
        })
    },
})
```

Existing `Listener.AcceptAssociation` loops remain supported. This server seam
lets CLIs and applications such as Twin-Viewer adopt the common admission,
correlation and shutdown policy incrementally without replacing their clinical
workflow callbacks or product counters.

`dimse.ServiceQueue` separately bounds active and queued service jobs. Close
cancels job contexts, uses one shared completion channel, recovers job panics,
and exposes exact active/queued/admitted/completed/rejected/panic counters.
`MaxInFlightBytes` plus `EnqueueBytes` optionally bounds a caller-supplied
estimate of memory or object bytes retained by active and queued work; the
ordinary `Enqueue` path preserves its existing behavior with a zero estimate.

## Timeout phases

UL options distinguish negotiation, idle wire, read progress, write progress,
and release timeouts. A progress timeout is renewed after bytes move; it is not
an absolute transfer deadline. DIMSE dataset readers mark their context as an
active transfer, so association idle time does not become a deadline between
dataset PDUs; the read-progress timeout (or overall context deadline) remains
authoritative. During release, the release timeout governs the wait for the
first reply byte and read progress governs gaps inside a reply PDU.
`dimse.SCPControls` can override command and dataset progress timeouts, set an
explicit absolute operation timeout, and add a cancel grace. Cancel grace wraps
the complete FIND/MOVE/GET operation, including application sub-operation
callbacks and pending-response writes. If work does not return during that
window, the association is aborted and closed. A final failure/cancel status is
attempted through a separately bounded response context, so a peer that stopped
reading cannot retain the server indefinitely.

Object/dataset limits remain separate from UL/PDU limits. In particular,
`StorageSCPOptions.MaxDataSetBytes` is an object policy; it must not be replaced
by association or PDU byte limits.

## Metrics and overhead

Association, server, queue, dispatcher, and raw-dispatcher snapshots use atomic
counters and do not impose Prometheus or OpenTelemetry. Event delivery is best
effort; protocol counters do not depend on observer delivery. With tracing off,
PDU bodies and headers are not copied for telemetry and wire counters are not
updated. The opaque association ID and lifecycle counters remain available.
Per-association snapshots count that association's queue drops. Sink-panic
counters belong to the dispatcher worker: they appear on an association only
when it owns the dispatcher, and on `AssociationServer` when the server owns the
shared dispatcher. Callers that inject a shared dispatcher read its `Stats`
directly.

Run the three modes with allocation reporting:

```sh
go test ./net/ul -run '^$' -bench BenchmarkAssociationPDUTracing -benchmem
```

The benchmark covers tracing off, safe header events, and bounded payload
capture. Performance comparisons should use the same machine and Go version.

## Compatibility boundaries

- Existing `Dial`, `Accept`, `Listener`, `Association`, and DIMSE service APIs
  remain valid; new option fields default to no-op behavior.
- The association ID does not depend on `Association.Context`.
- `MaxDataSetBytes` and application object limits are not UL admission limits.
- Telemetry does not change presentation-context negotiation. Applications
  should not add normalized-service SOP Classes to storage defaults merely to
  enable observation.

Relevant security guidance: DICOM PS3.7 warns that User Identity credentials
may be disclosed through logs, and DICOM PS3.15 treats AE/network identifiers as
potentially identifying operational data. Deployments remain responsible for
authentication, authorization, secure log transport, and retention.
