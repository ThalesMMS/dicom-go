# Private creator resolution

`dictionary.PrivateCatalog` is an optional, immutable catalog supplied by the
caller. Its identity is `(creator, odd group, byte offset)`, independently of the
block reserved in a particular dataset. The separate, opt-in
[`dictionary/curated` package](CURATED_PRIVATE_CATALOG.md) distributes a bounded
selection of reviewed definitions with per-entry provenance and licensing.

The implementation follows [PS3.5 2026c Section 7.8.1](https://dicom.nema.org/medical/dicom/2026c/output/chtml/part05/sect_7.8.html)
and the [LO definition](https://dicom.nema.org/medical/dicom/2026c/output/chtml/part05/sect_6.2.html).
Creators occupy elements `0010..00FF` and reserve data elements `xx00..xxFF` in
the same group. Valid private groups are odd, excluding `0001`, `0003`, `0005`,
`0007` and `FFFF`. A creator is LO/VM1 in the Default Character Repertoire,
regardless of Specific Character Set. Only leading/trailing ASCII SPACE padding
is removed for matching. Case, internal spaces and punctuation remain significant;
empty identifiers, backslash, NUL, controls, escapes, non-ASCII and values longer
than 64 bytes are rejected. The same creator may reserve blocks in different
groups, but cannot reserve two blocks within one group.

## Opt-in parsing and inspection

```go
catalog, err := dictionary.NewPrivateCatalog([]dictionary.PrivateEntry{
    {Creator: "ACME", Group: 0x0011, Offset: 0x01, VR: core.VRUS,
        Keyword: "AcmeCounter", Name: "Acme counter", VM: "1"},
})
if err != nil { return err }
dict := dictionary.Chain{catalog, std.Dictionary}
file, err := object.ReadFileWithOptions(source, object.ReadFileOptions{
    Dictionary: dict,
    MaxPrivateCreators: 4096,
    MaxPrivateDiagnostics: 128,
    RejectInvalidPrivateCreators: true,
})
if err != nil { return err }
defer file.Close()
// With (0011,001F) = "ACME", this resolves the catalog's offset 01.
entry, err := file.Dataset.ResolvePrivateEntry(core.NewTag(0x0011, 0x1F01))
```

`DataDictionary` is unchanged. `PrivateDataDictionary` adds `ByPrivate`;
`PrivateCatalog.ByTag` and `ByKeyword` intentionally return no match because an
unbound definition has no absolute tag. `ByPrivate` returns a block-relative
`Entry.Tag`; `LookupScopedEntry` substitutes the actual dataset tag. `Chain`
preserves its configured order across static overlays and creator catalogs. A
matching static entry, including UN, wins over later dictionaries. Existing
chains containing only ordinary dictionaries keep their existing behavior.

The implicit-VR reader resolves verified definitions before parsing the value,
including private SQ. Every root dataset and sequence item has a fresh scope;
there is no inheritance from parent items or reuse between siblings. The same
block can therefore mean different VRs in different items. Explicit VR remains
authoritative even if it disagrees with the catalog. Inspection resolves names
and keywords without changing stored VRs. A later summary dictionary cannot
retroactively decode an opaque implicit-VR value; supply the catalog when reading
to enable that interpretation.

Parsing is single-pass in wire order, as required by ordered DICOM datasets.
Creators must precede the attributes they reserve. Late creators or conflicts do
not retroactively reinterpret already emitted tokens. A private definition is
never inferred from byte patterns, an assumed `0x10` block, another group or a
parent dataset. Without a verified definition, defined-length values remain raw
UN bytes. Existing undefined-length UN sequence grammar and preservation remain
available. Reserved/forbidden odd-group tags receive no heuristic definition.

## Errors, bounds and mutation

Missing and unknown creators/definitions produce typed diagnostics and raw UN.
An invalid creator leaves its block unusable. Duplicate creator slots or the
same creator in two blocks make the entire affected group ambiguous; other
groups remain independent. `RejectInvalidPrivateCreators` turns invalid or
conflicting reservations into read errors; unknown definitions remain permitted.
`PrivateDiagnostics` on Reader and `ParsedPrivateDiagnostics` on Object return
detached snapshots with tags, offsets, enclosing Item offsets and typed errors,
without creator strings or attribute values. The Object report describes the
original parse and is not rewritten after edits.

The default budget is 4096 reservations per dataset/item and 128 diagnostics per
read. Negative limits fail; zero selects defaults. Budget exhaustion aborts
parsing. Diagnostics are capped and expose a truncation flag. Combine these
limits with ordinary element, depth and total-byte limits for untrusted input.
`PrivateReservationsFromElements` checks one already materialized scope and
returns the first `DefaultMaxPrivateIssues` issues (128), retaining all invalid
and conflicting state even after that list fills. A resource error returns no
usable scope. Construct reservation scopes with `NewPrivateReservations`.

Object preserves duplicate-slot ambiguity from original input even though its
ordinary element map uses last-wins semantics. Editing an unrelated creator
does not erase that ambiguity. Replacing/removing the affected slot explicitly
repairs it. Item facades retain independent mutation behavior. Catalogs support
concurrent reads; Reader, Object and mutable reservation scopes require external
synchronization when the same instance is used across goroutines. Independent
operations may share one immutable catalog.

With an active catalog, selective readers must materialize creator elements.
Streaming lifecycle hooks may observe creators, but cannot skip, defer, filter
or replace their tag/VR/value while dependent attributes are still being parsed.
Completed-item or sequence transformations and later authoring still carry the
caller's responsibility to keep reservations consistent. No automatic remapping
or reinterpretation occurs. Seek-based replay rebuilds reservation scopes when
it reparses, while direct recorded-value replay leaves scope state unchanged.

## Authoring

`Object.ReservePrivateCreator(group, creator)` reuses an existing unambiguous
reservation or inserts an LO/VM1 creator at the first unused block `10..FF`.
A block containing orphaned private data is occupied. Conflicts, malformed
reservations and exhaustion return errors without modifying the object.
`Object.PrivateTag(group, creator, offset)` returns a tag from an existing
reservation; it never creates one. Put the value using its verified VR and call
`ValidatePrivateReservations` before serialization. These helpers do not move
existing attributes or renumber blocks. Generic writing preserves explicit VRs
and raw values; it does not promote UN based on a catalog. Typed creator strings
are encoded using the Default Character Repertoire, including in UTF-8 datasets.

Private catalog entries are not a de-identification allowlist. Existing removal
and caller-attested safe-private policies remain unchanged. A known name or VR
does not establish that an attribute is safe to retain.

## Qualification

Local tests cover dynamic blocks, group/creator isolation, nested private SQ and
empty items, implicit LE and explicit LE/BE, explicit-VR precedence, PS3.18
JSON/XML preservation, malformed/duplicate reservations, bounded diagnostics,
authorship collisions, replay, selective/lifecycle guards, charset and concurrent
independent reads. A bounded parser fuzz target exercises malformed input.

Additional bidirectional preservation evidence for the tested explicit LE/BE
private attributes and nested scope layout lives in the
[separate validation workspace](../../dicom-go-validation/docs/REFERENCE_NOTES.md).
This does not qualify arbitrary vendor catalogs. Run the local tests with:

```sh
go test -race ./dictionary ./parser ./object
go test ./parser -run '^$' -fuzz '^FuzzPrivateCreatorScopes$' -fuzztime 15s
```
