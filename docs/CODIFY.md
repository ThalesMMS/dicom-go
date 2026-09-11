# Bounded Go reproductions

`codify.Generate(ctx, obj, options)` returns deterministic, gofmt-formatted Go
source and a value-free report of substitutions/omissions. It uses the existing
bounded object traversal and public `object`/`core` constructors. The generator
does not parse DICOM, serialize DICOM, read files, fetch BulkDataURI, run the
generated code, publish an issue or send data anywhere.

The CLI reads Part 10 with the existing object parser. From the module directory:

```sh
go run ./cmd/dicom-go-codify -output ./repro.go /path/to/synthetic.dcm
```

Review `repro.go` and the JSON report on stderr before manually running:

```sh
gofmt -d ./repro.go
go run ./repro.go
```

The default generated `main` constructs an object and prints only its element
count. A custom `-package repro -function BuildFixture` emits a package builder.
Package/function names must be valid Go identifiers; the builder cannot shadow
generated imports, `main`, `init`, or predeclared names such as `len` and `byte`.

## Explicit data policy

**Structural mode is the default.** Tags, VRs, sequence/item shape and typed
scalar multiplicities remain; text becomes empty, numeric/tag values become
zero, raw primitive values become empty, and bulk/unknown binary or unresolved
references become explicit `core.DiscardedValue` placeholders. Encoded lengths
and parser offsets are omitted. Every element is listed as substituted in the
report and annotated in the source. This is a structural example, not a
byte-exact reproduction or an automatic anonymization guarantee. Even a dataset's
structure may be sensitive; review it before sharing.

**Faithful mode is local opt-in and may contain PHI**, including private and
nested data. The warning appears both in source and on stderr:

```sh
go run ./cmd/dicom-go-codify -faithful -output ./local-repro.go /path/to/synthetic.dcm
```

Supported faithful values include raw bytes, multi/empty strings, all core typed
integer values (including signed/unsigned 64-bit edges), tag values, sequences,
and floats reconstructed from their exact bit patterns, including negative
zero, infinities and NaN payloads. Raw binary numeric byte order is restored on
the object. Standard dictionary lookup is used in generated code; custom
dictionary overlays, inherited fallback text options and other caller-specific
facade state are outside the reproduction contract.

File meta, preamble, source filenames, timestamps and parser item offsets are
not copied. Header lengths are retained only for faithfully retained payloads;
this still reconstructs an in-memory object rather than original file bytes.
Unsupported custom/deferred values fail in faithful mode without resolving
them. Explicit discarded values remain placeholders and are reported. Nil
values are accepted as faithful empties only with an explicit zero length.

Binary inclusion requires **both** `-faithful` and `-inline-binary`:

```sh
go run ./cmd/dicom-go-codify -faithful -inline-binary -max-binary-bytes 65536 -output ./local-pixels.go /path/to/small-synthetic.dcm
```

Without this option, Pixel Data, other bulk VRs, unknown `UN` bytes and fragments
are explicit discarded placeholders. The CLI consumes Pixel Data without
materializing it. Inline mode preserves bounded offset tables/fragments and
binary values; exceeding its byte budget fails instead of truncating data.
To reproduce a large image, retain a separately reviewed external fixture and
attach it explicitly in your own test. This generator does not generate an
automatic fixture loader. A discarded placeholder cannot accidentally serialize
as an empty but apparently complete image.

`core.BulkDataValue` is retained as an unresolved URI only when faithful policy
allows the containing attribute; it is never fetched. The default substitutes
it, including URIs in ordinary, private and nested attributes.

To request existing de-identification before generation:

```sh
go run ./cmd/dicom-go-codify -deid-basic -output ./structure.go /path/to/synthetic.dcm
```

This calls `deid.ApplyBasicProfile` on the CLI-owned object and prints its
residual-risk report. It neither guarantees pixel/PHI removal nor implements a
second de-identification policy. Adding `-faithful` can retain residual data;
review both reports. The profile's UID remapping may differ between invocations;
`Generate` itself is deterministic for the same unchanged input/options.
API callers can likewise apply their explicit `deid` profile before `Generate`.

## Resource and output contract

Zero option fields resolve to finite defaults. Negative values or values above
the ceilings fail; `codify.NormalizeOptions` exposes the same normalization used
by the CLI before parsing.

| Limit | Default | Ceiling |
| --- | --- | --- |
| Elements | 10000 | 100000 |
| Sequence depth | 32 | 64 |
| Sequence items, including empty items | 10000 | 100000 |
| Scalar values/fragments | 100000 | 1000000 |
| Retained text/raw bytes | 4 MiB | 64 MiB |
| Embedded binary bytes, total | 64 KiB | 1 MiB |
| Formatted source bytes | 1 MiB | 16 MiB |
| CLI input bytes | 64 MiB | 256 MiB |

The existing parser has its own combined item/sequence depth accounting. Its
limits can reject a file before generator traversal. The generated source
budget is checked before/after formatting; no partial source is returned on an
API error. These limits bound work and allocations but are not an RSS quota.
Context cancellation is checked during traversal, value budgeting and emission.
Keep the input object unchanged while generating; no ownership transfers occur.

`-output` publishes only complete generated source into a **new** filename, using
the shared no-follow, descriptor-anchored publication helper in a trusted local
directory. It never replaces an existing file or symlink. Errors before
publication remove the private temporary; post-publication errors may leave a
complete file. There is no `--force`. Stdout is useful for inspection, but shell
redirection has the shell's own overwrite semantics and is not covered by this
file-publication contract. CLI errors omit source/output paths and data values.

The source header records generator/schema, mode, binary policy, element count
and change count. The separate JSON report identifies every substitution by
tag/item path and a fixed reason. It intentionally carries no source hash or
clinical identifiers; callers can maintain private provenance outside the
generated artifact when required.

## Local evidence

`go test ./codify ./internal/codifycli` compiles generated programs/packages in
temporary modules with networking disabled. Reconstructed fixtures are compared
using the independent in-memory semantic oracle introduced in #901, in both
byte orders. Additional checks cover exact float bits, fragments, BulkDataURI,
escaped Unicode/control characters, nil/empty/multiple values, nested sequences,
integer edges, deterministic formatting and structural non-disclosure canaries.
CLI tests cover deid policy, finite zero defaults, cancellation, binary/output
limits and preservation of an existing destination. No clinical fixture is used.

This implementation uses this repository's object model and independently
written generation logic; no external code or fixtures were copied.
Source-inspection notes live in the
[separate validation workspace](../../dicom-go-validation/docs/REFERENCE_NOTES.md).
