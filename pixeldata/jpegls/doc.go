// Package jpegls provides qualified pure-Go DICOM JPEG-LS decoders
// and explicitly registered lossless and near-lossless encoders.
//
// New and Register support 1.2.840.10008.1.2.4.80 strictly with NEAR=0 and
// ILV=0, plus unsigned RGB ILV=1/2 with 2-16 stored bits in 8/16-bit containers,
// HighBit=BitsStored-1, PlanarConfiguration=0 and matching codestream precision.
// The decoder accepts default coding parameters and LSE preset coding
// parameters with ID=1. NewNearLossless and RegisterNearLossless additionally
// support .81 with unsigned MONOCHROME1/2 ILV=0 or RGB ILV=0/1/2, matching
// precision and full-range MAXVAL. Signed .81, palette color under .81,
// restart markers, HP color transforms, and other JPEG-LS extensions are outside
// this package's subset. Interleaved scans must contain all three components in
// frame-header order, with MAXVAL=2^precision-1. YBR_FULL and signed RGB are not
// qualified. Encoders produce ILV=0 only. NewEncoderWithOptions and
// RegisterNearLosslessEncoder require explicit NEAR and lossy authorization;
// NewEncoder, zero Encoder and RegisterEncoder remain lossless. See
// docs/JPEGLS_INTERLEAVE.md and docs/JPEGLS_NEAR_LOSSLESS.md for the independent
// corpora and parameter limits.
package jpegls

// UID is the DICOM JPEG-LS Lossless transfer syntax UID.
const UID = "1.2.840.10008.1.2.4.80"

// NearLosslessUID is the JPEG-LS Near-Lossless transfer syntax, enabled for
// encoding only through explicit options and a caller-owned registry.
const NearLosslessUID = "1.2.840.10008.1.2.4.81"
