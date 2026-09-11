# Pure-Go JPEG-LS Near-Lossless decoding

The builtin registry includes `.81` (`1.2.840.10008.1.2.4.81`) through
`jpegls.RegisterNearLossless`. Callers can also use `jpegls.NewNearLossless()`.
The existing `New`, zero `Codec`, `Register` and `.80` binding remain strictly
lossless and reject NEAR>0. Encoding is a separate explicit API documented in
[Near-Lossless encoder policy and evidence](JPEGLS_NEAR_LOSSLESS_ENCODER.md);
no encoder is enabled implicitly.
`codecfull` still deliberately selects its optional CharLS adapter; callers
may explicitly select that backend instead of the base pure-Go decoder.

Supported metadata: unsigned MONOCHROME1/MONOCHROME2 with one component and
ILV=0, or unsigned RGB with three components and ILV=0/1/2; 2-16 stored bits
in 8/16 allocated bits; HighBit=BitsStored-1; matching stream precision;
PlanarConfiguration=0 for RGB. Every scan in a frame uses the same NEAR/ILV.
MAXVAL must be the full precision range. NEAR is validated against
0..min(255, MAXVAL/2). LSE ID=1 defaults are resolved only after SOS supplies
NEAR; thresholds must be ordered and at least NEAR+1. Default and explicit
thresholds are accepted; the independently measured custom profiles use RESET=64.

Signed `.81`, palette color under `.81`, YBR_FULL, restart markers, transforms,
mapping tables and custom MAXVAL are explicitly rejected. Signed lossless `.80`
support is unchanged. JPEG-LS computes source error on unsigned codes; a small
code difference at a signed discontinuity can represent a large signed-value
difference. This initial decoder qualification does not claim signed NEAR
bounds. See [DICOM PS3.5 2026c, 8.2.3](https://dicom.nema.org/medical/dicom/2026c/output/chtml/part05/sect_8.2.3.html)
and [pydicom's signed pixel explanation](https://pydicom.github.io/pydicom/stable/guides/encoding/jpeg_ls.html#pixel-representation).

## Algorithm and evidence

The existing LOCO-I implementation is extended, following
[T.87 Annexes A-C](https://www.itu.int/rec/T-REC-T.87-199806-I/en): NEAR gradient
quantization, quantized RANGE, bias adaptation in reconstructed units,
lossless-only special mapping, dequantization and wrap/clamp reconstruction.
Scalar and interleaved decoding share the same state and run-interruption logic.
The scan, Item/frame assembly, output validation, 512 MiB request/working-set
limits and cancellation paths are reused. No native dependency enters the core.

The #902 corpus adds **116 synthetic streams** and their complete raw
reconstructions and original sources, pinned by SHA256. Profiles include NEAR=1
at every precision from 2 through 16; NEAR=3/7 and the permitted upper endpoints
at 8/12/16 bits; explicit LSE thresholds at 8/16 bits; NEAR=0 under `.81`;
and small one-row fuzz/EOF seeds. Main images are 129x33, with independent and
joint channel runs, changing neighbors, extrema and high variation.
The two existing public pydicom Near-Lossless streams also match their full
CharLS reconstructions exactly. These measured combinations are recorded in
the main corpus manifest; their presence alone does not qualify every Cartesian
combination of coding parameters, metadata and image content.

Two distinct checks are required: every reconstructed sample must equal the
independent CharLS result **exactly**; separately, every reconstructed sample
must differ from the original unsigned sample by at most NEAR. NEAR is never
used as tolerance between decoders. `sourceSamples` records the original raw
path/hash and the normative bound independently of `reconstruction`. The
source-bound check makes no PSNR, spatial-quality or clinical-quality claim.

The corpus includes four RGB/monochrome 8-bit NEAR=127 streams. Their default
thresholds resolve to 128/128/128 as required by T.87, and their reconstructions
match CharLS exactly. Additional comparison evidence is recorded in the
[separate validation workspace](../../dicom-go-validation/docs/REFERENCE_NOTES.md).

Provenance: the in-tree algorithm was extended without copied external code.
The [CharLS 2.4.2 tool](https://github.com/team-charls/charls/tree/36dd3307e070d8fbc765c3ba890b7e681046fa39)
is BSD-3-Clause. The generated data are project-authored, MIT and contain no PHI.
`jpegls-near/generation.json` records the runtime binary hash. Native binaries
and downloaded source remain outside the repository.

## Local reproduction

```sh
python scripts/generate-jpegls-interleave.py --near-lossless --charls /path/to/CharLS-2.4.2-library --output /tmp/near-candidates
go test ./pixeldata/... -count=1
go test -race ./pixeldata/jpegls ./pixeldata/codecfixture ./pixeldata/builtin -count=1
go test ./pixeldata/jpegls -run '^$' -fuzz '^FuzzDecodeNearLossless$' -fuzztime=15s -parallel=2
make check
```

The shared generator keeps its original lossless mode. Generation writes
candidates; hash review remains explicit. Default tests use only committed
fixtures. Negative coverage includes UID/NEAR mismatch, mixed scans, invalid
LSE/bounds, unsupported signed/photometric profiles, precision/geometry conflicts,
truncation, request limits, deadlines, in-frame cancellation, concurrent reuse
and no partial output after a later frame fails.
