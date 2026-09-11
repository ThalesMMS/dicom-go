# Received-instance persistence

`storescp` and `dicom-go-retrieve -method get` use
`internal/netstore.SavePart10WithContext`. Both validate the C-STORE command,
accepted presentation context and dataset identity before persistence. The
library's generic DIMSE services still leave persistence policy to their caller.

## Identity and names

`core.IsValidUID` is the single semantic validator inherited from #805: numeric
OID components, canonical component spelling and a 64-byte UI limit. This follows
[PS3.5 Chapter 9](https://dicom.nema.org/medical/dicom/current/output/chtml/part05/chapter_9.html).
Dataset UI padding uses the existing `core.NormalizeUID` behavior (trailing
SPACE/NUL only); leading/interior bytes are not repaired. Both SOP identities
must be UI with one value. Dataset and command SOP Class/Instance UIDs must agree,
and the SOP Class must equal the accepted abstract syntax. Command parsing now
rejects multiple UID values instead of silently selecting the first.

Invalid identities are refused. There is no compatibility switch that silently
sanitizes or replaces an invalid identity. `SafeFileBase` remains a legacy name
transform, but it is not used to authorize received identities or form their
filenames. Valid instances use `<UID>.dcm`, then `<UID>.1.dcm` through
`<UID>.999.dcm` for collisions. Each attempt is exclusive. Existing regular files,
directories and symlink destinations are not replaced. Separate invalid UIDs
that previously sanitized to the same name now both fail before file creation.
Repeated valid instances retain their actual identities inside the files.

## Publication and cancellation

Persistence opens the trusted output directory without following links, creates
a random `.netstore-<token>.partial` entry exclusively with private permissions,
and writes the complete Part 10 file through the existing object writer.
Cancellation is checked before preparation, between output chunks of at most
32 KiB, after writing and immediately before publication. A blocked OS write,
sync or close cannot be interrupted by these checks; existing network/parser
limits continue to bound incoming datasets.

The temporary is synced and closed before it can become a `.dcm` entry. The
directory is reopened using no-follow traversal and compared by file identity
before publication. A replaced directory causes failure and cleanup stays
anchored to the original open directory. Publication rechecks temporary identity.

| Platform | Publication | File protection | Durability scope |
| --- | --- | --- | --- |
| Unix families supported by `x/sys/unix` | `linkat` into an absent destination, then remove the temporary alias | `0600`, applied to the opened descriptor | File sync before publication; directory sync after the destination link |
| Windows | Relative rename by handle with replacement disabled | Private DACL at creation; verified protected DACL for current user and SYSTEM before writing, applied by handle | File sync before publication; no equivalent directory-fsync or power-loss guarantee claimed |
| Other targets | Fail closed | Unsupported | No publication fallback |

Windows rename behavior follows
[`FILE_RENAME_INFORMATION`](https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/ntifs/ns-ntifs-_file_rename_information).
No copy fallback exposes a partially written final filename. On Unix, a filesystem
that cannot provide hard links fails explicitly. Pre-publication write, sync,
close, cancellation and publication failures remove the owned temporary and
preserve preexisting files. A process crash can leave `.partial` entries; automatic
deletion of such files is outside this helper's scope.

Once publication succeeds, the complete destination is authoritative. A later
close, directory-sync or cleanup failure returns `*netstore.Error` with
`Published=true` and the destination path; it does not roll back a published
instance or overwrite an older file. The CLIs return a C-STORE failure status on
that error, so an explicit peer retry can produce a duplicate complete instance.
Cancellation observed after commit does not remove the committed object.
Consumers must use `.dcm` as the publication boundary and ignore `.partial`.

`CreateInstanceFile` remains a low-level exclusive reservation helper: it exposes
an open, unfinished file and does not offer the complete-instance publication
contract. Neither receiving CLI uses it for persistence.

## Containment and diagnostics

The shared `internal/nofollow` primitives were extracted from the existing
transcode path implementation. Unix traverses from an absolute anchor using
`openat` and `O_NOFOLLOW`; Linux preserves the narrowly scoped `O_PATH` fallback
for an unreadable traversal root. Windows uses a verified volume handle and
relative `NtCreateFile` with `OBJ_DONT_REPARSE`. This does not change transcode
replacement policy or relax #892's sandbox checks. Creation, publication and
cleanup operate relative to the opened directory, never via a string-prefix
containment approximation.

The output root and its ancestor namespace must be configured by a trusted
operator. Untrusted peers control command/dataset values, not local directory
ownership, mount configuration or permissions. The tests also exercise a
directory replacement before publication: replacement is detected and the new
directory remains untouched. Moving the actual open directory after the final
identity check, modifying temporary entries as the same OS principal, malicious
filesystems and privileged local attackers are outside the guarantee. This is
not a sandbox against local administrators. Directory ACLs must protect filenames
as well as file contents; existing directory permissions are not rewritten.

Default C-STORE and C-GET store logs contain outcomes and status codes, without
SOP identities, saved paths or remote values. Persistence errors expose stable
operation names and preserve causes through `Unwrap` for `errors.Is/As`; logging
an unwrapped OS error can reveal a path and is not the default. This change does
not provide authentication, transport security or general PHI auditing.

## Qualification

All fixtures are synthetic. Tests cover malformed/overlong UIDs, Windows names,
traversal separators, command/dataset mismatch, multiplicity, maximum-length
UIDs, padding, preexisting files, 24 concurrent publications, symlink targets and
ancestors, directory replacement, default log redaction and injected write,
sync, close, cancellation, publication and post-commit failures. Network tests
exercise the handlers actually used by both CLIs.

Executed locally on 2026-09-07: Windows/amd64 NTFS and Linux/amd64 WSL2 with
`-race`, plus Windows/386. Symlink tests ran on both systems. The extracted
transcode primitives also passed the Linux chroot reproduction: uid 65534,
unreadable root, descriptor traversal fallback, directory replacement and symlink
rejection. Other Unix platforms and remote/SMB filesystems were not executed.
No CI workflow is required or provided.

The reproduced local defects were acceptance through sanitization, premature
final-name visibility, ambiguous command multiplicity and verbose identity/path
logging. Source-inspection provenance is retained in the
[separate validation notes](../../dicom-go-validation/docs/REFERENCE_NOTES.md).
No external implementation or clinical attachment was copied.
