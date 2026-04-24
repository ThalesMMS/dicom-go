// Package rle provides an optional pure Go decoder for DICOM RLE Lossless
// pixel data, transfer syntax UID 1.2.840.10008.1.2.5.
//
// The codec supports 8-bit and 16-bit frames with 1 to 3 samples per pixel.
// Import this package only when RLE support is required, then register the
// codec explicitly:
//
//	pixeldata.RegisterCodec(rle.UID, rle.New())
//
// Decoding follows DICOM PS3.5 Annex G: each encapsulated frame contains a
// 64-byte RLE header, PackBits-compressed byte-plane segments, and segment
// interleaving into native little-endian pixel bytes.
package rle
