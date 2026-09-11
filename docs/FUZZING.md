# Fuzzing and adversarial checks

The `fuzz` package contains Go fuzz targets for untrusted decoding surfaces:

- `FuzzDICOMParse`: Part 10, raw dataset and parser decoding across supported transfer syntaxes.
- `FuzzDICOMJSON`: DICOM JSON unmarshal followed by compact and pretty marshal.
- `FuzzULPDU`: Upper Layer PDU decoding plus DIMSE command-set decoding.

The [parser regression corpus](../../dicom-go-validation/docs/UPSTREAM_PARSER_REGRESSIONS.md) adds persistent
synthetic seeds for undefined byte lengths, tiny raw datasets, malformed charset
representations and private UN sequences with nested pixels. The parse target
also exercises charset interpretation and writing after successful reads.

The `net/dicomweb` package keeps client response fuzzing beside its private
parsing seams:

- `FuzzDICOMwebMetadataJSONInstanceRefs`: DICOM JSON metadata, instance
  reference extraction, response media types, transfer-syntax parameters and
  deterministic decoding.
- `FuzzDICOMwebMetadataReferences`: bounded metadata-reference extraction with
  complete, fallback, empty and truncated JSON seeds.
- `FuzzWADOResponseParsing`: single-part and multipart buffered objects,
  streamed objects and streamed frames through an offline HTTP transport,
  including boundaries, raw headers, truncation, media/transfer-syntax
  compatibility and interacting body/part/count limits.

These targets use synthetic valid and malformed seeds. The DICOMweb targets
cap input and header values before parsing and configure deliberately small
body, part-count, part-size, raw-header and multipart-depth limits. Those hard
limits are the allocation guardrails. The smoke command additionally uses two
fuzz workers, a per-target test timeout and Go's soft `GOMEMLIMIT`; it does not
use the much larger production response defaults.

The `validation` package additionally contains bounded synthetic fuzz targets
for VR strings, VM delimiters and nested sequence/report limits. The exact
commands are documented in [`VALIDATION.md`](VALIDATION.md#testing-and-performance-contract).

Normal `go test ./...` execution runs only the deterministic seed inputs, so it
stays fast enough for the standard baseline. Longer fuzz campaigns are opt-in:

```sh
make fuzz-smoke

go test ./fuzz -run='^$' -fuzz=FuzzDICOMParse -fuzztime=10m
go test ./fuzz -run='^$' -fuzz=FuzzDICOMJSON -fuzztime=10m
go test ./fuzz -run='^$' -fuzz=FuzzULPDU -fuzztime=10m
go test ./net/dicomweb -run='^$' -fuzz=FuzzDICOMwebMetadataJSONInstanceRefs -fuzztime=10m
go test ./net/dicomweb -run='^$' -fuzz=FuzzDICOMwebMetadataReferences -fuzztime=10m
go test ./net/dicomweb -run='^$' -fuzz=FuzzWADOResponseParsing -fuzztime=10m
```

`make fuzz-smoke` runs each selected DICOMweb target independently for five
seconds with a two-minute outer timeout, at most two workers, and a 1 GiB soft
memory target. It uses and removes an isolated Go build/fuzz cache so an
accumulated machine cache cannot make the seed-baseline phase grow across
runs. Override `FUZZ_SMOKE_TIME`, `FUZZ_SMOKE_TIMEOUT`,
`FUZZ_SMOKE_PARALLEL`, or `FUZZ_SMOKE_MEMORY` for a deliberate longer campaign.
Set `FUZZ_SMOKE_GOCACHE` to an absolute disposable directory only when a CI job
needs to own cleanup itself.
Do not use `FUZZ_SMOKE_MEMORY` as a substitute for target-local byte, count and
depth caps: `GOMEMLIMIT` is a garbage-collector target, not a hard address-space
limit.

Go minimizes a failing input and writes it under
`net/dicomweb/testdata/fuzz/<Target>/<hash>` (or the corresponding package for
another target). Reproduce it with
`go test ./net/dicomweb -run '<Target>/<hash>'`. Keep only a minimal synthetic
regression input; never add PHI, credentials, real endpoint identifiers, or
site-specific metadata to a seed or crash corpus.

Long fuzz and race runs should be separate. Allocation assertions based on an
exact `AllocsPerRun` count are intentionally avoided because parser allocations
vary with valid element count and Go version. The enforced contract is that
accepted work remains bounded by the target input cap and configured parser
byte/count/depth limits, and oversized inputs fail before unbounded parsing.
