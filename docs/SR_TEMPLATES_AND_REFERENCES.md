# Structured Report references and template validation

Package `sr` provides structural support for DICOM SR content-item trees,
including by-reference relationships and caller-defined template/context-group
validation. This is a bounded library surface, not a claim of complete SR IOD,
Template ID (TID), or Context Group ID (CID) conformance.

## By-reference content items

`ContentItem.ReferencedContentItemIdentifier` preserves Referenced Content Item
Identifier (0040,DB73) as a one-based path from the document root. Both forward
and backward references are supported. A by-reference relationship item carries
only Relationship Type (0040,A010) and the referenced path; value type, concept
name, value fields, child content, and other by-value macros are rejected when
writing.

`ReadDocument` decodes and preserves reference paths but does not resolve or
validate the graph automatically. Applications that need navigation or
validation can use either:

- `ReadDocumentWithOptions` with `ResolveReferences` enabled; or
- `ResolveReferences` after constructing or reading the complete `Document`.

Resolution happens after the full content tree is available, so forward
references work. The returned `ReferenceIndex` supports edge enumeration,
source-to-target lookup, reverse lookup, and lookup by relationship slot. It is
immutable and bound to the indexed document shape. Structural mutation makes
subsequent index queries return `ErrStaleReferenceIndex`; callers must resolve
the document again.

Generic document serialization validates references with bounded strict
defaults before emitting a dataset. This prevents malformed by-reference
macros, dangling/self/ancestor references, cycles, and relationships forbidden
by the implemented SOP Class profile from being written silently.

### Implemented SOP Class profile checks

The resolver applies the following profile rules:

| SOP Class | By-reference policy |
|---|---|
| Basic Text SR Storage | Prohibited. |
| Enhanced SR Storage | Prohibited. |
| Key Object Selection Document Storage | Prohibited. |
| Comprehensive SR Storage | Permitted only for the implemented compatible source/target value-type combinations; `CONTAINS` and `HAS CONCEPT MOD` cannot be conveyed by-reference. |
| Comprehensive 3D SR Storage | Same relationship rules as Comprehensive SR, with `SCOORD3D` additionally accepted as a target of compatible `HAS PROPERTIES`, `INFERRED FROM`, and `TCOORD` `SELECTED FROM` relationships. `SCOORD3D` is not a by-reference source for `SELECTED FROM`. |
| Extensible SR Storage | Permitted structurally except for `CONTAINS`; no broader template conformance is inferred. |

For an unrecognized SOP Class UID, the resolver still checks the graph,
by-reference macro, path, and non-empty relationship type, but it does not infer
an IOD-specific relationship matrix. Callers must not treat a clean structural
report for such a UID as SOP Class conformance.

## Strict and warning modes

Reference resolution/validation during reading and all template validation are
opt-in operations. Existing `ReadDocument` callers do not receive new
reference-validation failures merely because a document contains a relationship
that needs application-specific handling. Serialization remains the fail-closed
boundary described above and always checks the reference graph.

`ValidationModeStrict` records error-severity findings and returns a typed error
(`ErrReferenceResolution` or `ErrTemplateValidation`) when violations exist.
The document/report and, for reference resolution, the built index remain
available for inspection. `ValidationModeWarn` returns the same structural
findings as warnings without rejecting the operation. `DefaultReferenceOptions`
and `DefaultTemplateValidationOptions` are strict; the opt-in reference step in
`DefaultReadOptions` uses warning mode when `ResolveReferences` is enabled.

This distinction does not make warning mode a repair mechanism. Encoded paths
are never rewritten, and applications decide whether a warning is acceptable
for their workflow.

## Caller-owned template and context-group registry

The package does not embed or silently update normative DICOM TID or CID
definitions. Applications or generated build artifacts supply
`TemplateDefinition` and `ContextGroupDefinition` values to
`NewTemplateRegistry`.

Each `TemplateKey` and `ContextGroupKey` contains a mapping resource,
identifier, and explicit version. Context-group definitions can additionally
record source/checksum provenance. The constructor validates definitions,
duplicates, includes, and include cycles, then deep-copies the input into a
frozen registry. Lookups return caller-owned copies, and `WithTemplate` and
`WithContextGroup` return new registries rather than mutating an existing one.
Registries and validators may therefore be shared across goroutines.

`TemplateValidator` can check row cardinality and order, by-value versus
by-reference mode, relationship/value types, coded concept identity, context
group membership, includes, conditional rows, and caller-supplied encoded code
or template-identification metadata. Validation occurs only when the caller
invokes `Validate` or `ValidateWithMetadata`. A successful validation means the
document matches only the exact registered definition and metadata supplied for
that operation; it is not proof that the registry reproduces the current DICOM
standard.

Enhanced Code Sequence attributes and Content Template Sequence identities are
available through the opt-in `ReadResult.TemplateMetadata` sidecar. The
sidecar keeps `CodedEntry` comparable while preserving coding-scheme versions,
context-group identifiers and versions, extension metadata, mapping-resource
identities, long/URN code values, equivalent codes, and template identities.
Use `Document.ElementsWithTemplateMetadata` or
`Document.DatasetWithTemplateMetadata` to re-emit that metadata. The legacy
`Elements` and `Dataset` methods intentionally retain their existing output;
callers that need enhanced-code round trips must use the explicit sidecar API.

Metadata entries are keyed by one-based content paths. After inserting,
removing, or reordering content items, callers must discard or explicitly
rebuild the sidecar before writing; otherwise the old path can describe a
different item. Reference indexes detect this class of mutation themselves,
but the caller-owned metadata sidecar intentionally has no live document link.

## Resource and diagnostic policy

Reference resolution defaults to at most 64 levels, 100,000 content items,
100,000 references, 65 path components, and 1,024 findings. Template validation
defaults to at most 64 levels, 100,000 items, 1,000,000 matching steps, and
1,024 findings. Callers may set different positive bounds. Registry construction
is also bounded for definition, row, code, include, and string counts. Reports
set `Truncated` when the finding limit is reached.

Resource-limit exhaustion is fatal in both strict and warning modes. Warning
mode preserves conformance findings, but it never returns a partial reference
index or silently accepts input that exceeded a traversal or metadata budget.

Built-in error strings and `DiagnosticFinding` messages are value-free: they do
not echo SR text, person names, UIDs, dates, code meanings, or registry
definition contents. Findings identify structural paths and targets, stable
codes, severity, generic messages, and, where applicable, a caller-provided
template row `RuleID`. Callers must keep `RuleID` values free of PHI. The
diagnostic boundary also does not cover application logging or caller-owned
condition callbacks, which remain trusted application code.

## Measurement Report scope

`MeasurementReport`, `FromMeasurements`, `WriteMeasurementReport`, and
`ReadMeasurementReport` implement the library's focused internal measurement
exchange model. It groups tracking identifiers, image/segment/spatial
references, and numeric measurements and can be encoded using the supported
Enhanced, Comprehensive, or Comprehensive 3D SR Storage UIDs.

That convenience model is not a complete implementation or validator of DICOM
TID 1500 (Measurement Report) or TID 1501 (Measurement Group). In particular,
the package does not ship the complete normative template/context-group
definitions or assert every conditional row and IOD requirement. Applications
must not label output as TID 1500/1501 conformant unless they supply the
appropriate versioned definitions, perform the required validation, and meet
the remaining SOP Class/IOD obligations outside this focused model.

## Independent interoperability checks

The offline unit suite covers forward/backward reference round trips in
Implicit VR Little Endian, Explicit VR Little Endian, and Explicit VR Big
Endian. Optional independent-fixture checks are also available:

- Set `DICOM_GO_PYDICOM_SR_FIXTURE` to pydicom's `test-SR.dcm` fixture to read
  and navigate its encoded by-reference relationships.
- Set `DICOM_GO_SR_INTEROP_OUTPUT` to emit a synthetic Comprehensive SR fixture
  with forward and backward references for inspection by an independent DICOM
  reader.

These checks demonstrate structural interchange for their fixtures. They do
not establish general SR, TID, CID, or IOD conformance.
