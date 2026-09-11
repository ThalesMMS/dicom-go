# Reference archive

A small runnable composition of the existing `index`, `qrmatch`, `net/dimse`,
`net/ul` and safe Part 10 persistence helpers. The catalog belongs to this
example; it is not a public library service or the Twin-Viewer archive.

Run from the `dicom-go` module:

```sh
go run ./examples/referencearchive -root ./reference-archive -move-destination MOVEDEST=127.0.0.1:11114
go run ./cmd/storescu -called REFERENCEARCHIVE 127.0.0.1:11113 /path/to/synthetic.dcm
go run ./cmd/findscu -host 127.0.0.1 -port 11113 -called REFERENCEARCHIVE -level study -k StudyInstanceUID= -k 'PatientName=SYNTH*'
```

Use a Study Instance UID returned by C-FIND in place of `STUDY_UID`:

```sh
go run ./cmd/dicom-go-retrieve -method get -remote 127.0.0.1:11113 -called-aet REFERENCEARCHIVE -output ./retrieved -level STUDY -study-uid STUDY_UID
```

For C-MOVE, start the explicitly allowed destination in another terminal:

```sh
go run ./cmd/storescp -address 127.0.0.1:11114 -aetitle MOVEDEST -output ./moved
go run ./cmd/dicom-go-retrieve -method move -remote 127.0.0.1:11113 -called-aet REFERENCEARCHIVE -level STUDY -study-uid STUDY_UID -move-destination MOVEDEST
```

Stop with Ctrl+C, then repeat the archive command with the same root to rebuild
the index. No separate database or migration command is required.

## Deliberate service subset

- Verification and Study Root C-FIND, C-GET and C-MOVE. C-GET negotiates the
  storage role on the requesting association; C-MOVE uses `StoreSession`.
- Secondary Capture, CT Image Storage and MR Image Storage with Implicit VR
  Little Endian or Explicit VR Little Endian. No codec/transcode dependency.
- Canonical Study, Series and SOP UIDs, a nonempty Patient ID, and scalar
  standard-VR query metadata of at most 1024 UTF-8 bytes per selected value.
  This is an archive profile, not full SOP Class/IOD validation.
- Hierarchical queries require exact ancestor UIDs. Retrieval requires unique
  identity keys; UID lists are allowed at the requested level. Known optional
  C-FIND keys use `qrmatch`; unsupported keys fail explicitly. Matching remains
  limited to that package's supported rules, with no relational/fuzzy extension.
- Study metadata and selected patient metadata must agree exactly within a
  Study UID; a Series UID must belong to one study and have consistent series
  metadata. Conflicts fail instead of choosing one patient's metadata.
- Every duplicate SOP Instance UID, including identical content, is rejected
  with A900. Quota/write failures use A700; malformed or inconsistent content
  fails explicitly. Existing instances are never overwritten by this example.
- C-MOVE destinations are an explicit startup `AE=host:port` allowlist (at most
  16 entries). Unknown destinations return A801. Request data cannot provide a
  network destination. C-MOVE batch progress is reported after the bounded batch
  returns; it is not a per-instance real-time progress feed.

**Storage Commitment is disabled and its SOP Class is not negotiated.** A
successful store acknowledges this example's complete file publication and
catalog admission. It does not promise power-loss durability, replicated
storage, recovery after hardware failure, or clinical retention. DICOMweb is
also outside this example.

## Persistence, restart and ownership

The root must be a dedicated directory in a trusted local namespace. The
process holds an OS advisory lock on `.archive.lock` until shutdown; a second
cooperating archive process cannot open it. The lock file remains on disk and
the OS releases the lock after a crash. Do not remove it while an owner runs.
Windows, Linux, macOS and the BSD platforms with the supplied lock backend are
supported; other platforms fail closed.

Storage uses the shared [safe persistence contract](../../docs/INSTANCE_PERSISTENCE.md):
canonical UID filenames, private temporary files, no-follow directory/file
opens, complete-file publication and no overwrite. A serialized, context-aware
transaction checks Part 10 encoded size before publication. The network handler
owns a materialized, bounded input dataset; callers must not mutate it during
the operation. Published instances are admitted to the index only after a
successful re-read. A failure after publication preserves the file and blocks
the archive until restart/reconciliation.

Restart validates each file through the existing parser, reading and discarding
Pixel Data to detect truncated payloads, then extracts detached metadata with
`index.Read`. Pixels are not materialized during rebuild, but all file bytes
are read for integrity of the parse. Incomplete `.partial` entries, unexpected
files, invalid identities, conflicting metadata, excess quotas and truncated
instances prevent startup. Nothing is silently deleted or advertised. An
operator must reconcile such entries offline before retrying.

Queries verify indexed file identity, size and modification time. Missing or
changed files fail selection; retrieval reopens each selected file. C-GET loads
one bounded instance at a time, while C-MOVE uses the existing rooted store
sources. File handles are closed after validation/read; no deferred pixel value
outlives its file. Stop the association server before closing the backend.
These checks are not protection against an adversarial local principal changing
files while preserving their attributes. External modifications and multiple
noncooperating writers are unsupported.

## Finite resource defaults

| Resource | Default | Configurable ceiling |
| --- | --- | --- |
| Stored instances | 1000 | 10000 (`-max-instances`) |
| Instances selected per query/retrieve | 1000 | Instance limit (`-max-results`) |
| Part 10 bytes per instance | 16 MiB | 64 MiB (`-max-instance-bytes`) |
| Committed Part 10 bytes | 1 GiB | 16 GiB (`-max-archive-bytes`) |
| Concurrent incoming associations | 4 | Fixed; saturation rejected |
| PDU | 64 KiB | Fixed |
| Parsed elements / nesting / pixel fragments | 10000 / 32 / 10000 | Fixed |
| Non-pixel element bytes | 1 MiB | Fixed |
| Negotiation / progress / idle / operation | 5 / 10 / 30 / 30 seconds | Fixed |

`-max-results` counts selected instances before grouping C-FIND results, so a
study with many instances can exceed it even when the final response count is
small. Oversized requests fail instead of silently truncating answers. The disk
quota covers instance bytes; the small persistent lock file and filesystem
metadata are additional. Per-association parsing and retrieval can consume up
to the configured instance size; these are not global RSS guarantees.

## Validation

Default tests are synthetic and offline:

```sh
go test -race ./examples/referencearchive ./net/dimse
```

The independent profile uses the pinned requirements shared with the DIMSE
interop suite (pydicom 3.0.2, NumPy 2.1.3 and pynetdicom 3.0.4):

```sh
python -m venv /path/to/interop-venv
/path/to/interop-venv/bin/python -m pip install -r scripts/requirements-pynetdicom-interop.txt
DICOMGO_PYNETDICOM_INTEGRATION=1 DICOMGO_PYTHON=/path/to/interop-venv/bin/python go test -race ./examples/referencearchive -run TestReferenceArchiveIndependent -count=1 -v
```

On PowerShell, set `$env:DICOMGO_PYNETDICOM_INTEGRATION='1'` and
`$env:DICOMGO_PYTHON` to the venv's `Scripts/python.exe` before the Go command.
This test launches only loopback peers: six synthetic multi-frame instances,
two studies, four series, C-STORE, C-FIND, C-GET, C-MOVE, duplicate rejection,
unknown destination rejection, C-GET cancellation counters and a disk restart.
The independent reader/receiver compares every metadata field in the fixture
and all 3876 pixel samples per store/get/move pass, before and after restart.
It also verifies that Storage Commitment is not negotiated. Unit tests cover
failed writes, concurrent duplicates, byte/result/instance quotas, truncated
pixels, missing files, inconsistent identities and post-publication failure.

The peer qualification is specific to these native syntax, synthetic fixtures
and service flows; it is not a claim of parity with another archive or PACS.

## Deployment boundary

The default listener is `127.0.0.1:11113`. This is a development reference, not
a clinical PACS: no authentication, TLS policy, authorization, backup, deletion,
retention, audit retention or migration system is supplied. AE allowlisting
controls outbound C-MOVE routing and does not authenticate incoming clients.
Use synthetic/non-PHI fixtures. The example explicitly enables patient and
description index profiles to serve queries; it does not anonymize stored data.
Production logs omit dataset values, UIDs and storage paths. Host/network access
controls and any decision to expose a non-loopback listener remain deployment
responsibilities.
