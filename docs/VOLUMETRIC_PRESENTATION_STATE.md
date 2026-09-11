# Volumetric Presentation State support

The `vps` package reads, validates, applies, and writes a deliberately bounded
subset of the standard DICOM Volumetric Presentation State modules. Standard
objects are never interpreted as the historical `THALESMMS_VPS` private
payload. Unsupported transforms return `vps.ErrUnsupportedPayload`; malformed
or internally inconsistent modules return `vps.ErrInvalidObject`.

## SOP Class matrix

| SOP Class | Standard subset | Current boundary |
| --- | --- | --- |
| Grayscale Planar MPR VPS (`…11.6`) | One `VOLUME` input; PLANAR THIN or SLAB geometry; WC/WW VOI; IDENTITY/INVERSE presentation LUT; bounding-box crop; AVERAGE/MAXIMUM/MINIMUM IP for slabs | VOI LUT Sequence, non-planar MPR, registration, oblique/segmentation crop, animation |
| Compositing Planar MPR VPS (`…11.7`) | One-input `EQUAL_RGB` classification with embedded sRGB ICC profile, plus the MPR geometry/VOI/crop subset above | `TABLE` LUTs, two-input classification, compositor components, multiple classifications |
| Volume Rendering VPS (`…11.9`) | One volume stream; PERSPECTIVE or ORTHOGRAPHIC viewpoint; WC/WW VOI; `VOLUME_RENDERED`, `MAXIMUM_IP`, or `MINIMUM_IP`; `EQUAL_RGB` with `NONE`/`IDENTITY` alpha and embedded sRGB ICC profile; bounding-box crop; Phong shading fields retained | LUT tables, multiple streams/components, spatial registration, animation, non-bounding crop |
| Segmented Volume Rendering VPS (`…11.10`) | The same single-stream scalar subset when the referenced input itself is directly renderable | segmentation-driven crop/classification and segmentation composition |
| Multiple Volume Rendering VPS (`…11.11`) | The same single-stream scalar subset | multiple volume streams and compositor chains |

The private creator payload written by older versions remains readable and
round-trippable for all five SOP Classes. A state without standard geometry is
written through that legacy path for source compatibility. A state with
standard geometry is written only with standard modules and does not acquire
private camera or preset tags.

## Application constraints

`vps.Apply` preserves standard geometry, input references, crop selection,
VOI, display method, classification, and shading. Twin-Viewer's shared
`study.ApplyVPS` converts VPS-RCS (LPS) into the renderer's volume-centred
coordinates and is used by both UI styles.

The current square CPU VR surface can exactly apply centered square Render
Field of View values. Asymmetric or non-square fields fail closed. Near and far
depth bounds become clipping planes. A VPS-RCS bounding box becomes normalized
texture crop only for RCS-aligned source volumes; applying it to an oblique
source fails instead of using a larger approximate axis-aligned box.

References are retained as SOP Class UID/SOP Instance UID pairs and input
numbers must be contiguous from one. `Crop=YES` and `GlobalCrop=YES` must point
to existing cropping specifications. The supported crop path currently accepts
one effective bounding-box specification; composing several specifications is
rejected by the viewer application layer.

Authored standard states carry a non-empty Frame of Reference UID (0020,0052).
Applications must verify that every referenced input instance belongs to that
single frame before calling `vps.Write`; the writer rejects an authored
standard state that omits the frame UID. `vps.Validate` performs the same
non-mutating validation used by the writer, and `vps.SRGBICCProfile` returns an
independent profile header suitable for the supported one-input TRUE_COLOR
display contract.
