# Asynchronous Operations Window and multiplexed DIMSE

`dicom-go` keeps the historical synchronous association behavior by default.
`ul.DialOptions.AsynchronousOperationsWindow` and
`ul.AcceptOptions.AsynchronousOperationsWindow` are pointers: `nil` omits User
Information sub-item `0x53`, while a present field value of zero means
unlimited as defined by PS3.7 D.3.3.3. If either side omits the negotiation,
`Association.EffectiveAsynchronousOperationsWindow()` returns `1/1`.

The A-ASSOCIATE-AC fields remain in the association-requestor's perspective.
The public effective window is instead local-oriented:

| Local endpoint | Maximum invoked | Maximum performed |
|---|---:|---:|
| Association requestor | `AC.invoked` | `AC.performed` |
| Association acceptor | `AC.performed` | `AC.invoked` |

An acceptor may reduce a finite offer, including asymmetrically. It cannot turn
a finite offer into zero/unlimited or increase it. The codec requires exactly
one four-byte, Big Endian sub-item; reserved bytes are ignored on input and
written as zero.

## Explicit multiplexed runtime

Negotiating a value greater than one does not make legacy helpers concurrent.
Applications that need multiple messages in flight create
`dimse.NewAsyncSession`. The session:

- acquires the association's legacy operation guard for its entire lifetime;
- is the sole receive-loop owner and assembles every command plus its optional
  dataset before routing;
- reserves Message IDs association-wide until a terminal response;
- keeps `FF00` Pending responses on C-FIND/C-MOVE/C-GET and `FF01` only on
  C-FIND on the same operation;
- serializes every complete outbound command plus dataset, including all
  fragmented P-DATA PDUs;
- enforces separate local invoked/performed windows and bounded per-operation
  response queues;
- routes C-CANCEL only to its target performed Message ID;
- supports reverse C-STORE requests during C-GET through the same performed
  registry; and
- wakes all operations on peer release, abort, EOF, transport error or local
  shutdown.

`StartCEcho`, `StartCStore`, `StartCStoreEncoded`, `StartCFind`, `StartCMove`,
`StartCGet`, and `StartNormalized` are explicit concurrent entry points.
`StartCStoreEncoded` writes a caller-supplied dataset stream and never
materializes `*object.Object`. `Invoke` is the generic command/dataset form.
Incoming services register `AsyncRequestHandler` values, preferably in
`AsyncSessionOptions.Handlers` so they exist before the reader starts.
Handlers receive complete detached messages and must use `Respond`; a
terminal response must be sent before the handler returns.

`StoreSession` stays on the serial `StoreClient` loop unless
`MaxInvokedOperations` is greater than one **and** the peer's effective
invoked window is greater than one. Twin-Viewer Send defaults to 1/1;
`cmd/storescu -max-invoked` defaults to 1. See [`CSTORE_SESSION.md`](CSTORE_SESSION.md).

The context supplied to `Invoke` owns that operation. Cancellation sends
C-CANCEL for C-FIND/C-MOVE/C-GET and continues draining until the final response.
For a non-cancelable operation, cancellation aborts the association because the
wire state is uncertain. A reverse C-STORE started by a C-GET handler is detached
from the parent C-CANCEL while preserving its explicit deadline. `AsyncOperation.Cancel`
is also available explicitly; it never releases the Message ID before the final
response, and a peer that does not return a final response within the cancel-drain
timeout causes a bounded abort.

`cmd/findscu` and `cmd/dicom-go-retrieve` use this lifecycle for SIGINT. Their
first interrupt cancels the active operation and continues reading on an
independent five-second drain context before A-RELEASE. They return exit code
130 for a canceled final status. Signal defaults are restored immediately after
that first interrupt, allowing a second SIGINT to terminate without waiting.

## Local limits and lifecycle

Wire zero is preserved as unlimited in the association result, but never creates
unbounded local resources. `AsyncSessionOptions` applies finite caps. By default,
an unlimited invoked window admits 64 local invocations; an unlimited performed
window retains at most 256 peer requests while running at most 64 handlers.
At that local resource bound, an additional request receives a service-appropriate
out-of-resources or processing-failure response; it is not counted as a wire
window violation and the sole reader remains able to route responses and
C-CANCEL. Each operation queues at most 32 responses. The session retains at
most 1 GiB of command plus dataset bytes across all internal queues, while each
received dataset is additionally bounded to 1 GiB, 1,000,000 elements, and
sequence depth 128. The default cancel-drain timeout is 5 seconds.
A caller may reduce a finite negotiated invoked window, but cannot reduce a
finite performed entitlement after negotiation or configure more than 65,535
local operations.

For a finite performed window, a request that reaches the sole reader while a
terminal response already owns the message writer is backpressured until that
terminal publishes completion; its handler never starts early and occupancy
does not exceed the negotiated limit. This narrow tolerance is required because
a peer can observe the terminal bytes and send its next request before the
local write callback runs. A request that exceeds the window when no terminal
write is in progress is a protocol violation and stops the session.

`AsyncSession.Release` has three explicit policies:

- `AsyncReleaseWait`: stop accepting new invocations, wait until both directions
  are idle, then perform A-RELEASE;
- `AsyncReleaseRejectIfActive`: return `ErrAsyncOperationsActive`; and
- `AsyncReleaseAbort`: abort immediately.

Only the association requestor can initiate normal release, and release is not
accepted in the middle of a DIMSE message or before the requestor's invocations
have terminal responses. `Close` is the bounded aborting cleanup path. While an
`AsyncSession` owns the association, public UL reads, writes, message writers,
release, P-DATA carryover access, `Dispatcher`, and legacy DIMSE entry points
fail with an ownership error; handler contexts never carry the bearer token.

`AsyncSession.Snapshot` reports only exact occupancy peaks/current counts,
retained-message byte counts, and window/duplicate violations. It never includes
command payloads, UIDs, Message IDs, endpoints, or patient values. Arbitrary
handler errors are redacted at the public string boundary while retaining
structural `errors.Is`/`errors.As` access through
`AsyncRequestHandlerError.Unwrap`.

Response ownership is explicit: drain every operation with `Next` or `Wait`.
If the responses are intentionally abandoned, call `DiscardResponses`; queued
objects otherwise remain charged to `MaxQueuedMessageBytes` while the operation
is retained. Discarding does not release the Message ID or invoked slot before a
terminal response and therefore does not weaken DIMSE correlation.

## Verification

Always-on tests cover asymmetric/zero negotiation, malformed and duplicate
sub-items, out-of-order responses, Pending slot retention, slow-peer
backpressure, Message ID wrap-around, targeted cancellation, fragmented
command/dataset atomicity, reverse C-STORE during C-GET, release, abort and race
safety. The independent gate uses pynetdicom as an SCU offering `4/3`; the Go
acceptor reduces it to `3/2` in requestor perspective and completes C-ECHO:

```sh
DICOMGO_PYNETDICOM_INTEGRATION=1 \
  DICOMGO_PYTHON=/path/to/python \
  go test ./net/dimse -run '^TestAsyncSessionAgainstPynetdicomNegotiation$' -count=1 -v
```

pynetdicom currently negotiates this item but deliberately executes its own
DIMSE operations synchronously. The always-on Go peer tests therefore remain
the qualification gate for windows greater than one.
