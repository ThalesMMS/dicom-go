# Embedded DICOMweb server profile

`net/dicomweb.Server` is an embeddable, backend-neutral HTTP handler for a
bounded first server profile of PS3.18 QIDO-RS, WADO-RS, and STOW-RS. It does
not require a database, filesystem layout, or PACS schema. This document is a
capability statement, not a claim of complete Studies Service conformance.

## Security and ownership

The handler denies every request unless `ServerOptions.Authorize` is present or
the application deliberately sets `AllowUnauthenticated`. CORS is disabled by
default. The optional middleware hook is the deployment boundary for TLS-aware
authentication, CORS, tracing, and rate policy. Never expose an unauthenticated
plaintext handler outside a trusted test environment.

This package does not provide TLS certificate management, an identity store,
OAuth/OIDC validation, CORS policy, rate limiting, a WAF, or durable audit-log
storage. A production deployment must supply those controls. The supported
deployment shape is:

1. an internet- or enterprise-facing reverse proxy terminates TLS, rejects
   oversized requests early, applies rate policy, and normalizes only trusted
   forwarded headers;
2. deployment middleware validates credentials and applies an explicit CORS
   allowlist when browser access is required;
3. `ServerOptions.Authorize` independently authorizes the routed DICOMweb
   operation before backend work; leaving it nil remains deny-by-default;
4. the handler and backend run on a private/trusted network segment with finite
   timeouts and least-privilege access to storage.

Proxy body/header/time limits should be no larger than the policy intended for
the service and must not accidentally bypass the lower `ServerLimits` bounds.
Set proxy timeouts slightly above the handler's `MaxDuration` so the handler can
cancel backend work and clean spool files before the proxy disconnects. CORS is
not authentication: never combine a permissive origin policy with cookies or
bearer credentials.

`AuditEvent` contains only operation, request ID, status, item count, duration,
and a closed error code. It never contains paths, URLs, query values, UIDs,
DICOM attributes, backend errors, or remote text. Request IDs are restricted to
a small header-safe alphabet.

Do not log raw request URLs, query strings, headers, multipart filenames,
DICOM JSON, UIDs, backend errors, or response bodies: each can contain PHI or
secrets. Access logs at a reverse proxy must apply the same redaction rule.

Retrieve and bulk readers are borrowed from the backend for one synchronous
callback and are closed exactly once by the handler. STOW parts are staged in
bounded mode 0600 temporary files before the first backend call. Metadata-based
STOW resolves `BulkDataURI` only against an exact `Content-Location` in the
current multipart request, streams the bytes into validated Explicit VR Little
Endian Part 10 staging files, and performs no URL or filesystem dereference.
This makes a
truncated or malformed multipart fail before any commit and provides the
backend with a size and SHA-256 digest. `StoreBackend.Store` must consume the
reader synchronously and atomically decide stored, identical duplicate,
warning, or conflict; a separate existence check is not a safe commit gate.

QIDO and metadata results are also spooled to 0600 temporary files so the
handler can validate the complete dataset representations and set pagination warnings
without retaining a second response-sized copy in memory. `SpoolDirectory`
therefore may contain PHI from both requests and responses. Configure it on
access-controlled storage (and encrypted storage when required by deployment
policy); the handler closes and removes spool entries on success and error, and
reports a redacted backend failure if synchronous cleanup cannot complete. At
startup, the server scavenges only inactive regular files named
`.dicomweb-json-*` or `.dicomweb-stow-*`; unrelated files, directories,
symlinks, and entries new enough to belong to an active bounded request are
preserved. `SpoolRetentionAge` controls stale-file age and
`SpoolAggregateQuotaBytes` removes the oldest inactive owned entries until the
aggregate quota is met; zero values use finite defaults.

Backend-provided object and frame locations are not accepted. The handler
synthesizes relative same-origin locations from validated identifiers.
Outbound QIDO/WADO DICOM JSON `BulkDataURI` values are accepted only under the configured
same-origin `{ServiceRoot}/bulkdata/...`
tokens and are reauthorized through the bulk route on every request. Absolute,
cross-origin, query-bearing, or fragment-bearing values fail closed.

## Endpoints and media types

The implemented HTTP contract is summarized below. `{study}`, `{series}`, and
`{instance}` are validated DICOM UIDs; `{frames}` is a comma-separated list of
one-based frame numbers; `{token...}` is a server-issued same-origin bulk token.

| Service | Method and route | Request / `Accept` | Success response | Normal success status |
|---|---|---|---|---:|
| QIDO | `GET /studies`, `GET /series`, `GET /instances` | DICOM JSON or Native XML metadata | DICOM JSON, or `multipart/related; type="application/dicom+xml"` | `200` |
| QIDO | `GET /studies/{study}/series`, `GET /studies/{study}/instances` | DICOM JSON or Native XML metadata | DICOM JSON, or `multipart/related; type="application/dicom+xml"` | `200` |
| QIDO | `GET /studies/{study}/series/{series}/instances` | DICOM JSON or Native XML metadata | DICOM JSON, or `multipart/related; type="application/dicom+xml"` | `200` |
| WADO metadata | `GET /studies/{study}/metadata`, `GET /studies/{study}/series/{series}/metadata`, `GET /studies/{study}/series/{series}/instances/{instance}/metadata` | DICOM JSON or Native XML metadata | DICOM JSON, or `multipart/related; type="application/dicom+xml"` | `200` |
| WADO objects | `GET /studies/{study}`, `GET /studies/{study}/series/{series}`, `GET /studies/{study}/series/{series}/instances/{instance}` | negotiated DICOM media | `multipart/related` (direct instance representation when explicitly negotiated) | `200` |
| WADO frames | `GET /studies/{study}/series/{series}/instances/{instance}/frames/{frames}` | negotiated frame media | `multipart/related` | `200` |
| WADO rendered | append `/rendered` to a study, series, instance, or selected-frames route | negotiated rendered media; optional `viewport`, `window`, `quality`, `annotation`, and `iccprofile` | direct representation for one result or `multipart/related` for collections/multiple frames | `200` |
| WADO thumbnail | append `/thumbnail` to a study, series, instance, or selected-frames route | direct rendered image media; optional two-value `viewport` | one direct rendered image | `200` |
| WADO bulk | `GET /bulkdata/{token...}` | negotiated bulk media | `multipart/related` or explicitly negotiated direct representation | `200` |
| STOW | `POST /studies`, `POST /studies/{study}` | `multipart/related` with root type `application/dicom`, `application/dicom+json`, or `application/dicom+xml`; `Accept: application/dicom+json` | `application/dicom+json` | `200`, `202`, or `409` |

Common failures are `400` malformed request, `401` unauthenticated, `403`
forbidden, `404` unknown resource, `405` wrong method (with `Allow`), `406`
unacceptable response media, `413` resource limit, `415` unsupported request
media, `499` canceled client request, `504` deadline, and `500` redacted backend
failure. Error bodies are `text/plain; charset=utf-8` and deliberately contain
no backend or DICOM detail.

DICOM JSON remains the default representation. Native XML is selected through
`Accept: multipart/related; type="application/dicom+xml"`; each QIDO result or
WADO metadata instance occupies one bounded part. WADO XML parts include a
same-origin `Content-Location`, and the client verifies that its study, series,
and instance identifiers match the decoded dataset before returning it.

QIDO-RS implements all six native search resources below. When the service is
mounted below the origin root, prefix each route with `ServerOptions.ServiceRoot`
and mount the handler with `http.StripPrefix` using the same value.

- `GET /studies`
- `GET /series`
- `GET /instances`
- `GET /studies/{study}/series`
- `GET /studies/{study}/instances`
- `GET /studies/{study}/series/{series}/instances`

The handler validates case-sensitive standard-keyword filters, `includefield`, `limit`,
`offset`, `fuzzymatching`, `emptyvaluematching`, and
`multiplevaluematching`. Results use `application/dicom+json`; an empty result
is `200` with `[]`. Backends report the exact remaining count and which optional
matching modes were applied so the handler can emit `Warning: 299` without guessing.
Private-tag and File Meta match keys are rejected in this initial profile rather
than accepting a private key without its required Private Creator.

WADO-RS implements study, series, and instance retrieval; study, series, and
instance metadata; selected one-based frames; and opaque same-origin bulk data.
Object and frame payloads stream as `multipart/related`; instance and bulk data
also support explicitly negotiated direct representations. Every transaction
that may return a payload requires an `Accept` header; absence returns `406`.
An explicit `*/*` selects the standard multipart default. The backend receives ordered media type and Transfer
Syntax preferences. It must return bytes in the syntax it declares. The handler
returns `406` rather than relabeling, rendering, or implicitly transcoding.

The implemented response matrix is explicit:

- `application/dicom` accepts recognized DICOM Transfer Syntaxes except
  Implicit VR Little Endian and retired Explicit VR Big Endian, which PS3.18
  forbids for Web Services;
- uncompressed frame `application/octet-stream` accepts Explicit VR Little
  Endian and Encapsulated Uncompressed Explicit VR Little Endian;
- `image/jpeg`, `image/jls`, `image/jp2`, `image/jpx`, `image/jphc`,
  `image/jxl`, `image/dicom-rle`, and `application/x-deflate` accept only the
  corresponding standard Transfer Syntax family;
- `video/mpeg`, `video/mp4`, and `video/H265` accept only their corresponding
  MPEG-2, AVC/H.264, and HEVC/H.265 Transfer Syntax families.

The selected media type and Transfer Syntax are validated independently for
each multipart response part, as required by PS3.18.

Rendered and thumbnail routes are enabled when `ServerOptions.Renderer`
implements the backend-neutral `dicomweb.Renderer` interface. Without it these
routes return `501`. The renderer owns authorized resource lookup,
transfer-syntax decode, SOP-class support, presentation selection, and ordered
frame production. The handler owns query validation, media negotiation,
multipart framing, same-origin content locations, `private, no-store` cache
policy, and request-wide pixel/output budgets. It stages every rendered part
before publishing `200`, revalidates reported dimensions and cumulative bytes,
and returns a DICOMweb error when the renderer rejects an unsupported SOP class
or transfer syntax.

The `render.StandardEncodedRenderer` is the default reusable still-image
mechanism for an application renderer. It consumes an already-decoded
`render.Frame`, applies the shared modality/VOI grayscale or native color path,
crops before cancel-aware bilinear scaling, preserves aspect ratio, and emits
bounded `image/png` or 8-bit Baseline `image/jpeg`. A storage adapter interprets
the decimal DICOMweb source region, including defaults, clipping, and negative
width/height flips, then passes an integer `SourceRect` plus flip flags to the
encoder. Resource resolution remains outside `render` so neither library
package depends on Twin or an application database.

STOW-RS implements `POST /studies` and `POST /studies/{study}` for
`multipart/related; type="application/dicom"`, one Part 10 instance per part,
and metadata-based payloads rooted at `application/dicom+json` or
`application/dicom+xml`. JSON uses one non-empty array part; XML uses one strict
Native DICOM Model per metadata part. Metadata must precede bulk, must not
contain group 0002, and every unique `BulkDataURI` must match exactly one used
`application/octet-stream` part by `Content-Location`. References are opaque
request-local keys: absolute-looking values are never fetched, and values from
an earlier request are never retained. The server reconstructs, bounds, hashes,
and reinspects every Explicit VR Little Endian Part 10 file before the first
backend call.
The collection route may contain multiple studies; the scoped route UID is
checked against every instance before any backend call. Responses use DICOM JSON Retrieve URL, Referenced
SOP Sequence, Failed SOP Sequence, and Other Failures Sequence. HTTP status is
`200` for complete success, `202` for mixed success/warning/failure, `409` when
no instance is stored, `400` for malformed input, `413` for limits, and `415`
for unsupported request media.

## Limits and cancellation

`DefaultServerLimits` is finite for request, metadata, encoded and decoded part bytes,
response, rendered pixels/output, URI, headers, query values/pairs, result count, offset, frame count,
multipart count, concurrency, and duration. Zero option fields receive these defaults; negative values are
invalid. The optional owned HTTP server also sets finite read-header, read,
write, idle, and header limits and supports graceful `Shutdown`.

`ServiceRoot` is a path, not an externally trusted origin override. When a TLS
terminator or reverse proxy changes scheme or authority, the application must
apply its usual trusted-forwarded-header policy before the handler; the core
does not trust arbitrary forwarded headers.

Request cancellation is checked before authorization, during query/result
work, while staging STOW parts, between backend calls, and during every streamed
copy. A backend reader that can block independently of its context remains
responsible for making its own `Read` context-aware.

## Minimal fail-closed localhost example

The example below intentionally binds only to loopback and requires an
authorizer. Use a synthetic backend in local tests; never put patient data in
examples, fixtures, URLs, or logs.

```go
limits := dicomweb.DefaultServerLimits()
limits.MaxRequestBytes = 512 << 20
limits.MaxPartBytes = 256 << 20
limits.MaxMetadataBytes = 32 << 20
limits.MaxDuration = 2 * time.Minute

handler, err := dicomweb.NewServer(dicomweb.ServerOptions{
    Backend:        syntheticBackend,
    ServiceRoot:    "/dicomweb",
    Authorize:      authorizer, // return ErrUnauthorized/ErrForbidden on denial
    Limits:         limits,
    SpoolDirectory: protectedTemporaryDirectory,
    Audit:          phiFreeAuditSink,
})
if err != nil {
    log.Fatal(err)
}

mux := http.NewServeMux()
mux.Handle("/dicomweb/", http.StripPrefix("/dicomweb", handler))
server := &http.Server{
    Addr:              "127.0.0.1:8042",
    Handler:           mux,
    ReadHeaderTimeout: 5 * time.Second,
    ReadTimeout:       3 * time.Minute,
    WriteTimeout:      3 * time.Minute,
    IdleTimeout:       30 * time.Second,
    MaxHeaderBytes:    1 << 20,
}
log.Fatal(server.ListenAndServe())
```

For production, replace loopback HTTP with the reverse-proxy architecture
above or configure an application-owned TLS server. Do not set
`AllowUnauthenticated`; that switch exists for isolated tests and explicitly
trusted deployments only.

## Deliberate initial limitations

This profile does not transform compressed STOW bulk data, and does not
implement WADO-URI, HTTP Range, DELETE, a built-in archive-to-frame resolver,
or a transcoding pipeline inside the handler. Presentation State rendering,
annotations, and ICC transforms depend on the injected renderer. These omissions mean the handler does not claim
the complete mandatory media-type surface of a current PS3.18 Studies Service.
Unsupported response media returns `406`; unsupported request media returns
`415`. Applications may add capabilities behind the backend interfaces without
weakening the default policy.

The package tests use the existing `dicomweb.Client` as an internal black-box
peer for all implemented transactions. An opt-in independent curl smoke gate is
available through `DICOMGO_DICOMWEB_CURL=1`; broader OHIF, dcm4che, and
dicomweb-client validation remains a deployment interoperability gate.

The primary contract tests are
[`server_test.go`](../net/dicomweb/server_test.go),
[`server_hardening_test.go`](../net/dicomweb/server_hardening_test.go), and
[`server_characterization_test.go`](../net/dicomweb/server_characterization_test.go),
plus [`server_rendered_830_test.go`](../net/dicomweb/server_rendered_830_test.go)
and [`server_stow_metadata_test.go`](../net/dicomweb/server_stow_metadata_test.go)
with its independent security suite,
and [`../render/encoded_test.go`](../render/encoded_test.go).
