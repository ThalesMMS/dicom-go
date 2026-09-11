# Hanging Protocol and Color Palette Query/Retrieve profiles

Implemented for issue #918 / Epic #899. Normative baseline: DICOM PS3.4 **2026c**,
Annex U (U.4.1–U.4.3, Table U.6-1) and Annex X (X.4.1–X.4.3, Table X.6-1),
retrieved directly from NEMA on 2026-09-07. These profiles are explicit subsets;
they do not claim complete IOD or service-class conformance.

- [Hanging Protocol attributes](https://dicom.nema.org/medical/dicom/current/output/chtml/part04/sect_U.6.html)
- [Color Palette attributes](https://dicom.nema.org/medical/dicom/current/output/chtml/part04/sect_X.6.html)
- [HP retrieval Identifier](https://dicom.nema.org/medical/dicom/current/output/chtml/part04/sect_U.4.2.html)
- [Palette retrieval Identifier](https://dicom.nema.org/medical/dicom/current/output/chtml/part04/sect_X.4.3.html)

## Existing seams and demonstrated gaps

| Surface before this change | Reused behavior | Added profile behavior |
| --- | --- | --- |
| Dictionary | All eight Storage/FIND/MOVE/GET UIDs already existed | Named model helpers, not a claim based on UID registration |
| AsyncSession / generic SCU clients | Arbitrary accepted SOP class; correlation, cancellation, reverse C-STORE | Validate identifiers and require this model's accepted Storage role before C-GET |
| Streaming C-FIND router | Exact routes, bounded request/response serialization, status, cancel monitor | Model key validation, matching, projection and application catalog callback |
| Association SCP C-MOVE/C-GET | Patient/Study Root dispatch and shared suboperation machinery | Exact HP/Palette routes, no patient hierarchy, selection and identity validation |
| C-MOVE batch provider | Session reuse and ordered descriptor accounting | Preserve batch interface and validate every selected descriptor before Run |
| qrmatch | Text, UID and same-item sequence matching | Restrict allowed matching per key and normalize binary US separately |

## Query contract

`dimse.HangingProtocolModel` and `dimse.ColorPaletteModel` expose
`ValidateFindIdentifier`, `Match`, `FindRoute`, `RetrieveUIDs`, `SOPClasses`,
`PresentationContexts` and `StartCFind/StartCMove/StartCGet` helpers. A zero model
is invalid. Catalog objects and yielded projections are borrowed synchronously;
callers must not mutate them concurrently or retain the yield callback.

Both models accept universal empty return keys, single SOP Class UID and
single SOP Instance UID for C-FIND. No PatientID, StudyInstanceUID,
SeriesInstanceUID or QueryRetrieveLevel is accepted, even as an empty key.
Unknown keys and matching values on return-only attributes produce A900;
this strict subset does not silently ignore unsupported optional attributes.

| Model | Matching keys | Return-only keys |
| --- | --- | --- |
| HP | Name (single/wildcard/universal), Level (single/universal), Definition Sequence (same-item matching), Number of Priors, User Identification Code Sequence, User Group Name, Number of Screens | Description, Creator, Creation DateTime, Nominal Screen Definition Sequence and its six screen attributes |
| Palette | Content Label (single/wildcard/universal) | Content Description, Content Creator's Name |

HP Definition Sequence accepts Modality and Laterality with single/universal
matching, plus Anatomic Region, Procedure Code and Reason for Requested
Procedure Code sequences. Coded entries support the short Code Value,
Coding Scheme Designator and Coding Scheme Version; Code Meaning is return-only.
Long Code Value, URN Code Value, context-group extensions, alternate palette
descriptions and other optional keys are unsupported. A query sequence has zero
or one item; each candidate item must satisfy all of that query item's keys.
Response projections retain only matching sequence items and requested nested
attributes; a universal empty sequence requests the whole supplied sequence.
Responses always carry the object's SOP Class/Instance UIDs and character set
when present. Missing requested return fields are empty, not fabricated values.

Text profile: default ASCII/ISO_IR 6 and UTF-8 ISO_IR 192. Other character sets
are rejected. Matching is case-sensitive for the supported matching VRs; there
is no fuzzy PN or relational/extended negotiation. Return-only names are not
matching keys. The provider supplies metadata separate from bulk object data;
full IOD construction, validation, indexing, persistence and authorization are
application responsibilities. The profile does not validate an HP layout or
palette LUT as an IOD and never requires synthetic patient identifiers.

Finite profile ceilings: 1 MiB Identifier, 256 request elements, 1024 catalog
metadata elements, depth 8 (including sequence/item boundaries), 1024 bytes per
matching value, one value per FIND key, 10,000 enumerated candidates, and the
existing qrmatch wildcard budget. `StreamingCFindLimits` can impose tighter
transport/response/match limits. Providers must stop at the first yield error
and honor cancellation. Response overflow reports A700; cancellation FE00;
provider failures C000. Shared SCPControls retain timeout/cancel-grace behavior.

## Retrieval and negotiation

C-MOVE/C-GET identifiers contain **only SOP Instance UID**, with one canonical
UID or a list of at most 128 distinct canonical UIDs. Wildcards, empty lists,
duplicates, invalid UIDs and hierarchy keys are rejected before calling the
catalog. Missing catalog objects produce zero suboperations, as in the existing
Q/R implementation. The provider may return only requested objects of this
model's Storage class, once each. C-GET additionally checks the loaded dataset
against the selected class and instance before C-STORE; a mismatch is a failed
suboperation and is never sent. C-MOVE callbacks/batches own destination
transport; they must preserve their declared identities in the sent datasets.

Wire SOPs use roots `1.2.840.10008.5.1.4.38` (HP) and `.39` (Palette), with
suffixes `.1` Storage, `.2` FIND, `.3` MOVE and `.4` GET. Identifier helpers offer
Explicit/Implicit VR Little Endian. Negotiation is opt-in: the application
selects supported abstract syntaxes; nothing is installed globally.

C-GET requires an accepted context for the matching Storage class, negotiated
requestor SCP role, `AsyncSessionOptions.CGetStorageSOPClassUIDs` including that
class, and a reverse C-STORE handler. An unrelated accepted Storage class does
not qualify. C-MOVE uses a caller-controlled AE destination policy and existing
StoreClient/StoreSession; unknown destinations can report A801. Neither helper
adds a dispatcher, a second receive loop, a persistent server or a destination
lookup service. No retired classes or other non-patient models are enabled.

## Evidence and independent qualification

Offline tests cover match/no-match, unsupported/wrong-VR keys, sequence item
semantics, binary US values in both byte orders, UID selection, returned-object
identity, batch descriptors, status limits, exact Storage role requirements and
C-CANCEL for FIND/MOVE/GET. Real loopback tests exercise both synchronous clients
and AsyncSession against the same existing Association SCP.

`TestNonPatientProfilesAgainstPynetdicom` uses pinned **pydicom 3.0.2 /
pynetdicom 3.0.4**, opt-in through `DICOMGO_PYNETDICOM_INTEGRATION=1` and
`DICOMGO_PYTHON`. On Windows/amd64 it verified both models over independently
negotiated FIND/GET/MOVE contexts, four find cases, two reverse stores and two
destination stores with full synthetic dataset equality, invalid Identifier
A900 and unknown destination A801. This extends the #903 test gate; it does not
qualify every optional key, a complete IOD or HOROS support. Additional
qualification scope is tracked in the
[separate validation inventory](../../dicom-go-validation/docs/CAPABILITY_AUDIT.md).

Run `go run ./examples/nonpatientqr` for synthetic discovery followed by actual
retrieval on loopback. Its minimal metadata fixtures are protocol demonstrations,
not clinical HP/palette objects. Tests remain offline by default; no GitHub
workflows or external datasets are added.
