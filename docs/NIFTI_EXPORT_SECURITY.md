# NIfTI export security

NIfTI export is not a de-identification operation. A NIfTI volume can retain
burned-in annotations or recognizable anatomy even when its companion metadata
contains no direct identifier. Callers that need de-identified output must
assess and, where required, clean pixel data independently. The DICOM
confidentiality profile discusses both [clean pixel data][clean-pixel] and
[recognizable visual features][visual-features].

## Sidecar boundary

`nifti.Sidecar.Marshal` produces a sibling JSON document with a closed field
allowlist and a versioned schema. It intentionally omits patient attributes,
UIDs, dates, descriptions, source paths, filenames, and free-form provenance.
Unit and quantity metadata are limited to code and coding scheme; free-text code
meanings are not included. Warning values are machine-readable codes and must
never contain source metadata or diagnostic text copied from DICOM values.

Always set a positive `maxBytes` and handle `*nifti.SidecarSizeError`. The error
reports sizes only and does not echo sidecar content. Treat output filenames,
directory names, logs, and surrounding workflow metadata as separate possible
identifier channels.

The JSON is a separate sidecar file. It must not be embedded in a NIfTI
extension and does not select or repurpose a NIfTI extension code. In
particular, extension code 2 is reserved for DICOM data and can carry PHI, while
extension code 44 is specific to MRS metadata in the [official NIfTI extension
code registry][ecodes].

Store and transport the NIfTI pixels and sidecar with the same access control,
encryption, retention, and audit policy used for the source clinical data. The
absence of identifiers from the sidecar does not make the NIfTI pixels
anonymous.

[clean-pixel]: https://dicom.nema.org/medical/dicom/current/output/chtml/part15/sect_e.3.2.html
[visual-features]: https://dicom.nema.org/medical/dicom/current/output/chtml/part15/sect_E.3.10.html
[ecodes]: https://raw.githubusercontent.com/NIFTI-Imaging/nifti_clib/master/niftilib/nifti1_io.h
