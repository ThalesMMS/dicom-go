// Package rle provides an optional pure Go codec for DICOM RLE Lossless pixel
// data, transfer syntax UID 1.2.840.10008.1.2.5.
//
// The decoder and frame encoder support 8-bit and 16-bit monochrome, palette,
// and RGB frames. Import this package only when RLE support is required, then
// register the required direction explicitly:
//
//	pixeldata.RegisterCodec(rle.UID, rle.New())
//	rle.RegisterEncoder(encoderRegistry)
//
// Coding follows DICOM PS3.5 Annex G: each encapsulated frame contains a
// 64-byte RLE header and PackBits-compressed byte-plane segments. NewEncoder
// returns one complete encoded frame for use as one encapsulated fragment.
package rle
