# Opt-in validation and lifecycle hooks

`dicom-go` provides a uniform validation engine for VR/VM, dictionary,
dataset, File Meta and caller-owned rules. Validation is deliberately opt-in:
the existing `parser.NewReader`, `object.ReadFile`, `object.ReadDataSet`,
`object.WriteFile` and `object.WriteDataSet` paths do not run validation or
hooks and retain their established wire and error behavior.

This package is a structural and value-representation validator. It does not
ship complete IOD/module rules and is not, by itself, a DICOM Conformance
Statement.

## Basic use

Validate an already materialized dataset:

```go
result, err := validation.ValidateDataSet(ctx, dataset, validation.Options{
	Mode:        validation.ModeStrict,
	Dictionary:  std.Dictionary,
	MaxFindings: 128,
})
if err != nil {
	// errors.Is(err, validation.ErrValidationFailed) identifies strict
	// validation rejection. result.Report remains available.
}
dataset = result.DataSet
```

Parse a raw dataset while retaining parser offsets and lifecycle events:

```go
reader, err := parser.NewReaderWithValidation(
	ctx,
	source,
	transfer.ExplicitVRLittleEndian,
	parser.ReaderOptions{Dictionary: std.Dictionary},
	validation.Options{Mode: validation.ModeWarn, MaxFindings: 128},
)
if err != nil {
	return err
}
dataset, readErr := reader.ReadDataSet()
report := reader.ValidationReport()
```

At the object facade, `ReadDataSetWithValidation`,
`OpenDataSetWithValidation`, `Object.ValidateDataSet`, `File.ValidateFile`
and `WriteDataSetWithValidation` expose the same engine. The root `dicom`
package re-exports the common raw-dataset entry points. File validation and
serialization remain explicit composition: call `File.ValidateFile`, then the
existing `WriteFile` only when the selected policy permits it.

`parser.Reader.Next` exposes header and primitive decoded-element events, but
streaming tokens alone cannot supply a complete dataset to dataset rules.
Call `ReadDataSet` on a validation reader when item/sequence completion,
dataset-complete, pre/post-validation hooks or dataset rules are required.

`pixeldata.MetadataValidationRule` is an optional dataset-rule adapter for
cross-field image metadata and native Pixel Data length. It is not enabled
automatically.

## Modes and collection bounds

- `ModePreserve` (zero value) records findings at their natural severity and
  returns transformed/preserved data without rejecting it.
- `ModeWarn` downgrades validation errors to warnings.
- `ModeStrict` returns `ErrValidationFailed` when the bounded report contains
  an error.

`MaxFindings`, `MaxDepth` and `MaxElements` bound diagnostics and traversal.
`StopFirst` stops validation after the first finding. A truncated report sets
`Truncated` and increments `Dropped`; callers must not interpret truncation as
a clean result.

Reports contain tags, VRs, stable rule codes, paths, offsets and
library-generated messages. They do not contain element values. Hook and rule
callbacks are trusted in-process code: they receive dataset/element copies and
are themselves responsible for not exporting PHI.

## Dictionary and VR/VM behavior

The standard dictionary preserves contextual VR alternatives (`xs`, `ox`,
`px`, `lt` and `up`) through the optional `dictionary.VRSpecDictionary`
contract while keeping `dictionary.Entry` comparable and source-compatible.
`dictionary.Chain` gives the first matching overlay ownership of both the
entry and its VR specification. A caller can resolve additional contextual
cases through `Options.ResolveVR`.

Unknown and private tags are retained; they do not become false standard-VR
mismatches. VM is derived from the actual typed or raw value. Sequences,
fragment sequences and Other-byte/word VR payloads each have VM 1 regardless
of item or byte count.

### UID syntax and normalization

Canonical DICOM UIDs contain at least two numeric OID components, use root arc
`0`, `1` or `2`, limit the second arc to `0` through `39` when the root is `0`
or `1`, contain no leading zeroes in multi-digit components, and occupy at most
64 bytes. `core.IsValidUID` checks only this canonical syntax;
`core.NormalizeUID` removes encoded trailing space or NUL padding when the
calling format permits it.

The generic validator, clinical relationship resolver, DICOMweb server,
DICOMDIR, UPS and DIMSE storage planning use the same syntax decision after
their format-specific normalization. DICOMweb route identifiers are not
padded and also obey the server's configurable `MaxUIDBytes` limit. Each
package retains its own typed diagnostic, error or DIMSE status mapping.

For compatibility, digit-and-dot strings that are not valid OIDs, including
`3.1`, `1.40` and `1.02`, are rejected consistently. Empty optional UI values
remain permitted by the generic validator; required UID policy is evaluated
separately.

## Lifecycle hooks

Register hooks explicitly in a `HookChain`. A validation operation snapshots
the chain once, so additions affect only later operations. Duplicate or
unstable hook names are rejected. Hooks marked `ConcurrentSafe: false` are
serialized across operations.

| Point | Observe/diagnose/reject | Replace element | Filter element | Skip value | Defer value |
|---|---:|---:|---:|---:|---:|
| `HookElementHeaderRead` | yes | no | no | yes | yes |
| `HookAfterElement` | yes | yes | yes | no | no |
| `HookSequenceComplete` | yes | yes | yes | no | no |
| `HookItemComplete` | yes | no | no | no | no |
| `HookDataSetComplete` | yes | no | no | no | no |
| `HookPreValidation` / `HookPostValidation` | yes | no | no | no | no |
| `HookPreSerialization` | yes | yes | yes | no | no |
| `HookPostWrite` | yes | no | no | no | no |

Mutually exclusive actions, or an action illegal for its point, return
`ErrHookAction` before applying the decision. Parser-side skip/defer initially
supports only defined-length primitive values. Defer additionally requires a
seekable source. Structural framing is always consumed, so filtering cannot
desynchronize the stream.

Panic and callback errors are contained. `HookFailureReject` returns a redacted
`HookError`; `HookFailureFinding` emits a stable diagnostic and discards the
decision. `HookRegistration.Timeout` includes time waiting for a non-concurrent
hook gate, but callback cancellation is cooperative: Go cannot safely preempt a
hook that ignores its context.

Lifecycle callbacks fire once for the original parse. Deferred-value replay,
`CopyElementValueTo`, and `Object.CopyValueTo` suppress replay events. The
writer's post-write event reports logical DICOM bytes accepted by the
serialization layer and whether that element was fully serialized. The object
facade's `WriteValidationResult.BytesWritten` separately reports bytes accepted
by the caller's external writer, including compression effects. A deflated
writer is created lazily, so validation rejection before serialization emits no
empty compressed stream. `ValidationWriteError.Complete` lets callers avoid
retrying an element whose bytes were already committed but whose post-write
hook failed.

## Paths, offsets and object limitations

Parser reports retain absolute offsets, including a configured `BaseOffset`,
and nested paths with zero-based item indexes. Duplicates and original ordering
must be validated before conversion to `object.Object`: Object intentionally
uses last-wins tag lookup and cannot reconstruct collapsed duplicates.

Deferred native and encapsulated Pixel Data remains streamable through the
validated object read/write path. General deferred sequence serialization has
the same existing limitation as the ordinary object writer.

## Testing and performance contract

Synthetic fuzz targets cover VR strings, VM expressions and bounded nested
sequences. Run them independently because Go fuzzing accepts one `-fuzz`
target per process:

```sh
go test ./validation -run='^$' -fuzz=FuzzValidateVRStringsBounded -fuzztime=10m
go test ./validation -run='^$' -fuzz=FuzzValidateMultiplicityDelimitersBounded -fuzztime=10m
go test ./validation -run='^$' -fuzz=FuzzValidateNestedSequencesBounded -fuzztime=10m
```

The ordinary reader/writer output is covered byte-for-byte with validation
disabled. The pre-declared default-path performance gate is no additional
allocation and no more than 5 percent median `ns/op` regression against the
same benchmark before this feature. The opt-in cost is reported separately:

```sh
go test ./parser -run='^$' -bench=BenchmarkReaderValidationLifecycle -benchmem -count=5
```
