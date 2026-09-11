# Modality Worklist SCU and SCP

`net/dimse` implements the Modality Worklist Information Model - FIND SOP
Class (`1.2.840.10008.5.1.4.31`) as a reusable, single-level C-FIND profile.
It supplies typed query builders, a blocking callback-streamed SCU, a
synchronous-yield SCP, exact SOP Class routing for `ServeAssociation`, and an
optional deterministic in-memory matcher. It does not supply a RIS/HIS,
persistent worklist, HL7 scheduling, or inferred clinical values.

## Public integration surface

- `ModalityWorklistFindPresentationContext` proposes Explicit and Implicit VR
  Little Endian.
- `BuildModalityWorklistIdentifier` preserves absent, present-empty, and
  present-valued keys.
- `ModalityWorklistClient.FindWithOptions` owns one operation on a borrowed
  established association. It streams each pending Identifier to a synchronous
  callback, sends C-CANCEL for local cancellation or match limits, and drains a
  final response before returning the association as reusable.
- `ServeModalityWorklistCFindWithOptions` serves one request directly.
- `NewModalityWorklistCFindRouter` installs MWL beside an optional hierarchical
  `CFindHandler` without changing `AssociationSCPOptions`.
- `ModalityWorklistHandler` receives a borrowed request and synchronously
  yields borrowed candidate objects. The core projects each candidate to the
  requested return keys before encoding it; providers need not materialize the
  complete result set.
- `NewInMemoryModalityWorklistHandler` clones its synthetic input records and
  yields matching records in input order. Production applications should
  implement `ModalityWorklistHandler` over their own bounded RIS/PACS query.

The association remains caller-owned. A clean final response permits Release
or another serialized operation. Failure to send C-CANCEL or drain the final
response is reported as uncertain association state and follows the configured
operation error policy.

## Matching and return-key profile

The typed profile covers the following Table K.6-1 attributes:

| Attribute | Tag | Matching implemented | Return behavior |
|---|---:|---|---|
| Patient's Name | `(0010,0010)` | Single/wildcard; literal and wildcard PN matching are case-insensitive by declared implementation policy | Requested value must be supplied by the provider |
| Patient ID | `(0010,0020)` | Case-sensitive single value | Requested value must be supplied |
| Patient's Birth Date | `(0010,0030)` | DA single/open/closed range | Requested value must be supplied, including explicit zero length when unknown |
| Patient's Sex | `(0010,0040)` | Case-sensitive single value | Requested value must be supplied, including explicit zero length when unknown |
| Accession Number | `(0008,0050)` | Case-sensitive single value | Requested value must be supplied, including explicit zero length when unknown |
| Requested Procedure ID | `(0040,1001)` | Case-sensitive single value | Requested value must be supplied |
| Requested Procedure Description | `(0032,1060)` | Case-sensitive single value | Requested value must be supplied and non-empty; this profile does not expose the alternative Requested Procedure Code Sequence |
| Scheduled Procedure Step Sequence | `(0040,0100)` | Sequence matching with zero or one query item; each match contains exactly one item | Requested nested attributes are projected; a zero-item or empty-item Universal Return Key returns the complete selected item |
| Scheduled Station AE Title | `(0040,0001)` | Case-sensitive single value, VM 1 | Requested value must be supplied |
| Modality | `(0008,0060)` | Case-sensitive single value, VM 1 | Requested value must be supplied |
| SPS Start Date | `(0040,0002)` | DA single/open/closed range | Requested value must be supplied |
| SPS Start Time | `(0040,0003)` | TM single/open/closed range | Requested value must be supplied |
| Scheduled Performing Physician's Name | `(0040,0006)` | Single/wildcard with the same case-insensitive PN policy | Requested value must be supplied, including explicit zero length when applicable |
| SPS Description | `(0040,0007)` | Case-sensitive single value | Requested value must be supplied and non-empty unless a requested, valid Scheduled Protocol Code Sequence supplies the alternative |
| SPS ID | `(0040,0009)` | Case-sensitive single value | Requested value must be supplied |
| Scheduled Station Name | `(0040,0010)` | Case-sensitive single value | Requested value must be supplied, including explicit zero length when applicable |
| SPS Location | `(0040,0011)` | Case-sensitive single value | Requested value must be supplied, including explicit zero length when applicable |

When both SPS Start Date and Start Time contain ranges, the matcher treats them
as one continuous interval, from the lower date/time boundary through the upper
date/time boundary. A single Date or Time value continues to match independently
even when its paired attribute is a range. A present zero-length value is Universal Matching and a
return-key request; an absent attribute is neither matched nor returned. An
Identifier without a non-universal matching key produces no matches and final
Success.

`TimezoneOffsetFromUTC` is accepted only with a TM key, validated in the
DICOM `+/-HHMM` range, and carried into responses that return the Scheduled
Procedure Step. The in-memory matcher treats candidate DA/TM values as being in
that declared offset when the candidate omits its own value; a candidate with a
different explicit offset does not match. It does not convert between offsets.

`SpecificCharacterSet` declarations use the library character-set registry.
The SCU includes the caller-supplied declaration in its Identifier, so the
dataset writer encodes textual `StringValue` query keys with that repertoire;
it interprets response text through the normal `object` character-set APIs.
The SCP decodes raw request keys under the request declaration before matching,
and the in-memory matcher independently decodes provider candidates under each
candidate's declaration. Raw textual response values are validated under that
candidate declaration, and `(0008,0005)` is projected only when returned text
needs a non-default repertoire, including ISO 2022 escape sequences. Neither
role replaces or infers a missing declaration.

The raw Identifier parser recognizes Scheduled Protocol Code Sequence
`(0040,0008)` as an in-model optional return key that this matcher does not use
for matching. It is returned when the provider supplies it. If it is absent for
a particular match, that response uses `FF01`; otherwise pending responses use
`FF00`. Each returned item follows the optional-meaning coded-entry macro:
exactly one Code, Long Code or URN Code Value is present; Coding Scheme
Designator is required for Code/Long Code and optional for URN; Code Meaning is
optional. Attributes outside this declared profile are rejected with `A900`
instead of being silently reinterpreted.

Fuzzy PN, Empty Value Matching, Multiple Value Matching, cross-timezone
conversion, and SOP Class Extended Negotiation of those behaviors are not
implemented.

## Statuses, cancellation, and bounds

- `FF00`: pending match; requested optional keys were supplied.
- `FF01`: pending match; an optional return key was unavailable.
- `0000`: matching complete, without a final Identifier.
- `FE00`: matching terminated after C-CANCEL.
- `A700`: configured resource or match limit exceeded.
- `A900`: Identifier does not match the MWL SOP Class/profile.
- `C000`: provider/output failure, with a closed PHI-free Error Comment.

Zero-valued limits use finite defaults. The SCP defaults to 10,000 matches,
4 MiB per Identifier/response, 256 Identifier element/item nodes at depth 4,
and 1,024 response element/item nodes at depth 8. The SCU uses the same 4 MiB,
1,024-node, depth-8 response bounds and a one-second final-response drain
timeout. Responses are structurally checked and encoded into a bounded discard
writer before any pending response bytes are committed. Provider yields are
serialized through the entire command/dataset pair, and the yield callback is
invalidated before the final response is sent.

## Examples and independent interoperability

Run the synthetic SCP and typed SCU in separate terminals:

```sh
go run ./examples/modalityworklist-scp -listen 127.0.0.1:11112 -aet MWLSCP
go run ./examples/modalityworklist-scu -address 127.0.0.1:11112 \
  -called MWLSCP -patient-name 'SYNTHETIC*' -date 20260810
```

The opt-in pynetdicom gate exercises Go as both SCU and SCP, including multiple
pending results and the single nested SPS item:

```sh
DICOMGO_PYNETDICOM_INTEGRATION=1 \
DICOMGO_PYTHON=/path/to/python \
go test ./net/dimse -run '^TestModalityWorklist(SCU|SCP)AgainstPynetdicom$' -count=1 -v
```

The examples use synthetic values only. Networking is plain TCP unless the
caller supplies UL TLS configuration; authentication, authorization, audit
retention, and deployment policy remain application responsibilities.
