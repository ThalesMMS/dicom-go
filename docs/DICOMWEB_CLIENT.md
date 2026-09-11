# DICOMweb client response policy

The `net/dicomweb` client validates response media types and applies finite
limits before returning buffered WADO-RS parts or delivering streamed parts to
a callback. The zero-valued `dicomweb.Options` remains usable and selects the
defaults below.

## Defaults

| Option | Zero-value behavior | Scope |
| --- | --- | --- |
| `Options.MaxBodyBytes` | 512 MiB | Complete HTTP response body |
| `ResponseLimits.MaxParts` | 100,000 | WADO-RS multipart parts |
| `ResponseLimits.MaxPartBytes` | Effective `MaxBodyBytes` | Each single or multipart representation |
| `ResponseLimits.MaxPartHeaderBytes` | 64 KiB | Raw MIME headers for each part, before parsing/allocation |
| `ResponseLimits.MaxMultipartDepth` | 1 | The single `multipart/related` layer defined for these resources |
| `Options.MaxMetadataParts` | 10,000 | DICOM XML metadata parts |

`MaxMetadataParts` is retained for compatibility. DICOM XML applies the lower
of that limit and `ResponseLimits.MaxParts`; it also applies the shared part
byte and header limits. Negative limits and malformed configured media types
are invalid configuration, except that the established `MaxBodyBytes <= 0`
behavior continues to select 512 MiB. WADO-RS responses are flat: nested multipart is
unsupported, and `MaxMultipartDepth` accepts only zero (the default) or one.

The default part-byte limit follows the effective body limit. Existing callers
that raise `MaxBodyBytes` therefore continue to accept a single object up to
that configured ceiling. Large study retrievals may require raising the total
body limit even when each individual instance is small.

## Accepted representations

Object retrieval accepts a single `application/dicom` representation or
`multipart/related` whose declared type and every part are
`application/dicom`. Study and series retrieval require multipart responses.

Frame retrieval requires `multipart/related`. Parts may use the DICOMweb frame
media types supported by the package:

- `application/octet-stream` and `application/x-deflate`
- `image/jpeg`, `image/jls`, `image/jp2`, `image/jpx`, `image/jphc`,
  `image/jxl`, and `image/dicom-rle`
- `video/mpeg`, `video/mp4`, and `video/h265`

When a frame part supplies a `transfer-syntax` parameter, its media type must
match that transfer syntax.

Metadata negotiation remains controlled by `MetadataMediaTypes`. DICOM JSON
uses `application/dicom+json`; the existing `application/json` and `text/plain`
aliases remain accepted for legacy servers when JSON was requested. DICOM XML
uses `multipart/related; type="application/dicom+xml"` with homogeneous
`application/dicom+xml` parts.

`ResponseLimits.AllowedMediaTypes` optionally narrows these
operation-specific sets. It cannot enable a representation that is invalid for
the requested resource. Because the option belongs to the client, include all
representations needed by that client's object, frame, and metadata calls.

## Streaming frames

`RetrieveFrames` remains the compatibility API and returns all requested frame
payloads in memory. It now collects over the streaming core, avoiding the
former extra allocation for the complete multipart response. Use
`RetrieveFramesStreamWithOptions` when the consumer can process one frame at a
time:

```go
stop := errors.New("enough frames")
err := client.RetrieveFramesStreamWithOptions(ctx, ref, []int{1, 20, 40}, opts,
    func(frame dicomweb.FramePartStream) error {
        if err := consumeFrame(frame.FrameNumber, frame.Reader); err != nil {
            return err
        }
        if frame.FrameNumber == 20 {
            return stop
        }
        return nil
    })
if err != nil && !errors.Is(err, stop) {
    return err
}
```

The callback is synchronous. Its `Reader` is valid only until the callback
returns and must not be retained. Returning `nil` lets the client drain any
unread bytes under the configured limits and advance to the next frame.
Returning an error stops without draining the remaining response, closes the
HTTP body, and preserves the callback error for `errors.Is`. The client also
closes the response on cancellation, HTTP errors, and decode failures.

A successful return validates the terminal multipart boundary and requires one
part for every requested frame. A callback can therefore have observed valid
earlier frames before a later truncation or count mismatch is reported. Stage
side effects until success when atomic consumption is required. An intentional
early stop cannot validate bytes the caller chose not to consume; the response
is closed instead.

## Tuning

```go
client := dicomweb.Client{
    Endpoint: endpoint,
    Options: dicomweb.Options{
        MaxBodyBytes: 2 << 30,
        ResponseLimits: dicomweb.ResponseLimits{
            MaxParts:           150_000,
            MaxPartBytes:       1 << 30,
            MaxPartHeaderBytes: 64 << 10,
            MaxMultipartDepth:  1,
        },
    },
}
```

Raise limits only from trusted deployment knowledge. Keep a total body limit
even for streaming calls; the total limit bounds a large number of individually
valid parts.

Buffered methods return no parts after a structural decode failure. Streaming
callbacks can observe valid earlier parts before a malformed closing boundary
is discovered. Consumers that require atomic behavior should stage callback
output and publish it only after the method returns `nil`.

Malformed media types, missing boundaries, incompatible representations,
truncation, and multipart structural-limit failures are returned as
`*dicomweb.Error` with `Kind == dicomweb.ErrorKindDecodeResponse`. Use
`errors.As` to inspect the nested `*dicomweb.ResponseDecodeError`; diagnostics
do not include response payloads, credentials, or raw endpoint queries. The
pre-existing whole-body limit retains `ErrorKindRequestFailure` for backward
compatibility.

The allocation benchmark uses an immutable 32-frame, 32 MiB multipart fixture:

```sh
go test ./net/dicomweb -run '^$' -bench '^BenchmarkRetrieveFramesBufferedVsStreaming$' \
  -benchmem -benchtime=1x -count=5
```

Run buffered and streaming sub-benchmarks in separate processes when measuring
peak RSS so retained Go heap arenas from the first case do not bias the second.
