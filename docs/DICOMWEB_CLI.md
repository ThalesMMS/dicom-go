# DICOMweb operational CLI

`dicomweb` is the standalone operational client for the reusable
`net/dicomweb` QIDO-RS, WADO-RS, and STOW-RS APIs. It is intended for bounded,
scriptable interoperability checks that do not depend on a graphical viewer.

The command has five subcommands:

```text
dicomweb verify
dicomweb query
dicomweb retrieve
dicomweb frames
dicomweb store
```

Examples in this document use only synthetic identifiers and the reserved
`example.test` domain. Do not substitute clinical identifiers in retained
terminal transcripts, issue attachments, or CI logs.

## Common endpoint and execution flags

Every subcommand accepts:

| Flag | Contract |
| --- | --- |
| `-base-url URL` | Required HTTP(S) DICOMweb base URL. URL user information is rejected. |
| `-qido-path PATH` | QIDO-RS service path relative to the base URL. |
| `-wado-path PATH` | WADO-RS service path relative to the base URL. |
| `-stow-path PATH` | STOW-RS service path relative to the base URL. |
| `-timeout DURATION` | Positive bound for the operation, including response streaming. |
| `-max-body-bytes N` | Positive bound for the HTTP response body. |
| `-output PATH` | Textual report/output file or WADO output directory; `-` selects stdout only for textual output. |
| `-metadata MODE` | Metadata negotiation order: `json`, `xml`, or `both`; the default is `json`. |

Only the service path relevant to an operation is used. Keeping all three path
flags common makes it possible to apply the same endpoint profile to verify,
query, retrieve, and store commands without constructing service URLs by hand.
The defaults are a 20-second timeout and a 512 MiB response-body limit.
Retrieve/frames default to at most 10,000 parts; store defaults to at most
10,000 files and 2 GiB of aggregate input. Operators should lower these bounds
to the smallest values appropriate for the synthetic check.

`verify` performs a QIDO study request with `limit=1` and emits only a bounded
JSON status/duration summary. It does not emit the returned dataset. For
verify, query, and store JSON, an omitted `-output` or `-output -` writes to
stdout.

`query <studies|series|instances>` accepts the required synthetic Study/Series
Instance UID scope and repeatable `-query key=value` parameters. Its canonical
DICOM JSON output can contain PHI. Direct it only to an approved destination;
stderr contains diagnostics, never result datasets.

`retrieve <study|series|instance>` accepts the UIDs required by its scope,
repeatable transfer syntax preferences, and a positive maximum part count.
Responses are streamed one part at a time into the required output directory as
`part-000001.dcm`, `part-000002.dcm`, and so on.

`frames` requires Study, Series, and SOP Instance UIDs plus a comma-separated
`-frames` list of unique, positive, one-based frame numbers. Its total response
bytes and part count are bounded, and parts use ordinal `.bin` names.

`store` accepts one or more DICOM Part 10 files and an optional synthetic study
scope. Raw datasets and arbitrary byte streams are rejected. File count and
total upload bytes are bounded before the request starts.

Use `dicomweb <subcommand> -h` for the exact operation-specific UID,
transfer-syntax, part-count, file-count, and upload-limit flags.

## Credentials and transport

Authentication is opt-in. Secret credential values are never accepted directly
as command-line arguments; the Basic username is not treated as a secret:

- `-basic-user USER` supplies the Basic username;
- `-basic-password-env NAME` reads the Basic password from environment variable
  `NAME`;
- `-bearer-token-env NAME` reads the bearer token from environment variable
  `NAME`.

Basic and bearer modes are mutually exclusive. Naming an unset or empty
credential environment variable is an input error. Password and token values
are never written to stdout, stderr, help, summaries, or errors.

Sending Basic or bearer credentials to an `http://` endpoint is rejected by
default. `-allow-insecure-auth` is the explicit per-invocation opt-in for a
trusted test environment. It does not enable TLS, suppress warnings, or change
certificate validation for HTTPS. Production credentials should use HTTPS.

Diagnostics redact Authorization headers, credential values, URL user
information, query strings, UIDs, and local paths. They retain only the
operation, error class, HTTP status where available, and bounded counts. Output
datasets and retrieved files are operator-selected data outputs, not diagnostic
logs, and remain subject to the site's PHI handling policy.

## Output safety

WADO files are created with owner-only permissions where the platform supports
them. Each part is written to a temporary file in the required destination
directory, closed, and atomically published under a collision-safe ordinal name
only after successful completion. Existing files are never overwritten. A
failed or canceled operation removes the files it created. `Content-Location`
is correlation metadata only and is never used as a local path.

STOW opens and streams validated Part 10 inputs. Its JSON report contains only
`status_code`, `stored_count`, `warning_count`, and `failed_count`. A response
that reports both stored and failed instances is partial success, not complete
success.

## Exit codes

| Code | Meaning |
| ---: | --- |
| `0` | Operation completed successfully. |
| `1` | Request, timeout, filesystem, or other runtime failure. |
| `2` | Invalid command, endpoint, flags, credential configuration, Part 10 validation, or resource scope. |
| `3` | Authentication/token failure or HTTP 401/403. |
| `4` | Other non-success HTTP status. |
| `5` | DICOMweb response, media, multipart, or DICOM decode failure. |
| `6` | STOW returned partial/accepted-for-processing status, reported failed instances, or preserved some stored results alongside an error. |
| `130` | Interrupted by SIGINT. |

For STOW, code `6` takes precedence over code `4` when a valid STOW response
explicitly identifies failed instances. Warnings without failures remain a
successful operation and are reported in the summary.

## Synthetic examples

Set credentials without placing their values in process arguments:

```sh
export SYNTHETIC_DICOMWEB_PASSWORD='value supplied by the test environment'
export SYNTHETIC_DICOMWEB_TOKEN='value supplied by the test environment'
```

Verify an unauthenticated synthetic endpoint:

```sh
go run ./cmd/dicomweb -- verify \
  -base-url https://pacs.example.test/dicom-web \
  -qido-path qido -wado-path wado -stow-path stow \
  -timeout 20s -max-body-bytes 1048576 -output -
```

Run a bounded synthetic QIDO study query using Basic authentication:

```sh
go run ./cmd/dicomweb -- query \
  -base-url https://pacs.example.test/dicom-web \
  -qido-path qido -timeout 20s -max-body-bytes 16777216 \
  -basic-user synthetic-operator \
  -basic-password-env SYNTHETIC_DICOMWEB_PASSWORD \
  -query PatientID=SYNTHETIC-001 \
  -output ./synthetic-qido.json studies
```

Retrieve one synthetic instance atomically:

```sh
go run ./cmd/dicomweb -- retrieve \
  -base-url https://pacs.example.test/dicom-web -wado-path wado \
  -study-uid 1.2.826.0.1.3680043.10.543.1001 \
  -series-uid 1.2.826.0.1.3680043.10.543.1001.1 \
  -instance-uid 1.2.826.0.1.3680043.10.543.1001.1.1 \
  -timeout 30s -max-body-bytes 67108864 -max-parts 1 \
  -output ./synthetic-retrieve instance
```

Retrieve two frames from the same synthetic instance:

```sh
go run ./cmd/dicomweb -- frames \
  -base-url https://pacs.example.test/dicom-web -wado-path wado \
  -study-uid 1.2.826.0.1.3680043.10.543.1001 \
  -series-uid 1.2.826.0.1.3680043.10.543.1001.1 \
  -instance-uid 1.2.826.0.1.3680043.10.543.1001.1.1 \
  -frames 1,2 -timeout 30s -max-body-bytes 67108864 \
  -output ./synthetic-frames
```

Store one approved synthetic Part 10 file using a bearer token:

```sh
go run ./cmd/dicomweb -- store \
  -base-url https://pacs.example.test/dicom-web -stow-path stow \
  -bearer-token-env SYNTHETIC_DICOMWEB_TOKEN \
  -timeout 30s -max-body-bytes 16777216 \
  -max-files 1 -max-upload-bytes 67108864 -output - \
  /path/to/approved-synthetic-part10.dcm
```

External PACS checks remain opt-in and must use approved synthetic fixtures.
The ordinary command tests use local HTTP test servers and do not require or
contact an external endpoint.
