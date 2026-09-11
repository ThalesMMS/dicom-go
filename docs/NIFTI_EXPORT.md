# NIfTI export

Package `nifti` converts one DICOM Series/Frame of Reference into a scalar
NIfTI-1 volume. It accepts replayable sources, plans from metadata first, and
writes `.nii` or streaming `.nii.gz` in a second pass.

```go
options := nifti.DefaultOptions()
options.Compression = nifti.CompressionGZIP

report, err := nifti.Write(
    ctx,
    destination,
    nifti.NewPathSource(paths),
    options,
)
```

`NewFilesSource` borrows already-open `*object.File` values. `NewPathSource`
reopens each Part 10 file and owns those handles. A custom `Source` must be
replayable because metadata and pixels are deliberately separate passes.

## Geometry policy

The default is fail-closed. Every frame must have finite Image Position
Patient, Image Orientation Patient, and Pixel Spacing. The exporter requires a
single Series Instance UID and Frame of Reference UID, rejects QUADRUPED
coordinates, separates explicit temporal positions, and uses tighter geometry
tolerances than the viewer. A volume is ordered by projection onto the slice
normal, never by path or input order.

The voxel-to-patient affine uses DICOM LPS coordinates and is premultiplied by
`diag(-1,-1,1,1)` for NIfTI RAS. Both qform and sform use scanner-anatomical
code 1. Non-orthogonal source geometry cannot be represented by qform and is
rejected unless `GeometryResampleLinear` is explicitly selected. Resampling
uses the validated `render` volume path, writes float32 modality values, and is
reported in `Report` and the sidecar.

## Pixels and scaling

Scalar MONOCHROME1/2 native pixels support signed and unsigned 8, 16, and
32-bit storage, including shifted Bits Stored/High Bit and either byte order.
MONOCHROME1 is not inverted: VOI, windowing, GSPS, and other presentation
operations never enter the quantitative export path.

`ScalingPreserveUniform` keeps normalized stored integers and places one
uniform slope/intercept in the NIfTI header. `ScalingApplyFloat32` and
`ScalingApplyFloat64` apply each frame's transform exactly once. A Parametric
Map is accepted only through its validated Real World Value Mapping; coded
units and quantitative concept must remain consistent across every source, and
the calibrated payload is emitted as float64.

Native path sources stream through `FrameSink` with O(one frame) working
memory. A codec that only supports whole-object decode uses the explicitly
bounded fallback `Limits.MaxInMemorySourceBytes`; an oversized source returns
`ErrLimitExceeded` instead of allocating without a ceiling. The final volume
is staged in a temporary spool so input reordering does not require holding all
voxels in RAM. Applications writing a path should use an atomic destination;
the Twin integration does so.

## Temporal volumes

4D export requires an explicit temporal position, offset, trigger, or phase for
every frame. Acquisition order alone is not a temporal dimension. Uniform
offsets or durations determine `pixdim[4]`; `TemporalSpacingSeconds` is allowed
only when metadata identifies temporal order but omits timing, and cannot
override contradictory irregular timing. Every time point must resolve to the
same spatial grid and affine.

## Provenance and PHI

NIfTI extensions are not emitted. `Report.Sidecar` is a closed, size-capped,
non-PHI JSON schema containing only dimensions, datatype, scaling policy,
reordering/resampling, coded units and quantity, timing, and stable warnings. See
`NIFTI_EXPORT_SECURITY.md`. Pixel data may itself contain burned-in identifying
information; exporting a volume is not de-identification.

## Independent-reader smoke check

The Go suite covers header goldens, qform/sform reconstruction, voxel order,
enhanced equivalence, 4D timing, gzip, limits, cancellation, and streaming. To
also check a generated file with nibabel:

```sh
export DICOM_GO_NIFTI_INTEROP_OUTPUT=/tmp/dicom-go-nifti-smoke.nii.gz
go test ./nifti -run '^TestWriteIndependentReaderFixture$' -count=1
python3 - <<'PY'
import nibabel as nib
import numpy as np
import os

base = os.environ["DICOM_GO_NIFTI_INTEROP_OUTPUT"]
suffix = ".nii.gz" if base.lower().endswith(".nii.gz") else ".nii"
stem = base[:-len(suffix)]
c, s = np.cos(np.pi / 6), np.sin(np.pi / 6)
expected = {
    "axial": np.diag([-1.0, -2.0, 2.0, 1.0]),
    "coronal": np.array([[-1,0,0,0], [0,0,-2,0], [0,-2,0,0], [0,0,0,1.]]),
    "sagittal": np.array([[0,0,2,0], [-1,0,0,0], [0,-2,0,0], [0,0,0,1.]]),
    "oblique": np.array([[-c,2*s,0,0], [-s,-2*c,0,0], [0,0,2,0], [0,0,0,1.]]),
}
for orientation, affine in expected.items():
    path = base if orientation == "axial" else f"{stem}-{orientation}{suffix}"
    img = nib.load(path)
    assert img.shape == (2, 1, 2)
    assert int(img.header["qform_code"]) == int(img.header["sform_code"]) == 1
    np.testing.assert_allclose(img.get_qform(), affine, atol=2e-5)
    np.testing.assert_allclose(img.get_sform(), affine, atol=2e-5)
    np.testing.assert_array_equal(np.asarray(img.dataobj).reshape(-1, order="F"), [1, 2, 3, 4])
PY
```

Nibabel is an optional external verification dependency and is not required by
the normal Go build.
