# Patient-space geometry guardrails

`render.Stack.GeometryAssessment` is the single headless decision point before
MPR or VR treats DICOM frames as one volume. It does not decode Pixel Data and
does not affect `RenderFrame`; unsupported inputs therefore retain safe 2D
access.

The result is one of:

- `regular-fast-path`: regular orientation and spacing use the existing volume
  path without a copy or resample;
- `regularizable`: reversed input is sorted deterministically, while gaps,
  irregular spacing, or gantry tilt are resampled in patient LPS space only
  when a uniform-grid consumer calls `Volume.RegularGrid`;
- `unsupported`: duplicate positions without temporal identity, mixed in-plane
  orientation, inconsistent normals or gantry shear, incompatible pixel
  spacing, mixed position sources, and unsplit temporal volumes fail closed
  with a stable `GeometryIssue`.

`GeometryFrameGroups` separates temporal positions and Stack IDs
deterministically. The caller builds one volume per returned group.

## Tolerances and affine contract

Default classification records the measured spacing, orientation, normal, and
affine residual alongside these thresholds:

- inter-slice spacing: 5% relative or 0.01 mm absolute, whichever is larger;
- duplicate patient positions: 0.000001 mm;
- orientation and gantry-tilt boundary: 1 degree;
- per-interval gantry shear deviation: at most `tan(1 degree)`;
- voxel-to-patient-to-voxel absolute error: at most `1e-9`.

Affines are finite row-major 4x4 matrices in DICOM patient LPS millimetres.
Voxel index `(0,0,0)` is the centre of the first voxel. The final row is
`[0,0,0,1]` within `1e-12`, X changes fastest, and handedness is preserved
without implicit flips.

Regularization never rewrites acquired frames. It materializes and caches a
new modality-value grid whose bounds cover all source slice corners. MPR may
continue using exact acquired positions and per-slice origins; VR requests the
regular grid because its texture sampler requires uniform spacing.
