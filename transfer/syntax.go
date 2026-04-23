package transfer

import "encoding/binary"

type Syntax struct {
	UID            string
	Name           string
	ExplicitVR     bool
	ByteOrder      binary.ByteOrder
	Supported      bool
	Encapsulated   bool
	Deflated       bool
	CodecAvailable bool
}

func (s Syntax) IsLittleEndian() bool {
	return s.ByteOrder == nil || s.ByteOrder == binary.LittleEndian
}

func (s Syntax) RequiresCodec() bool {
	return s.Deflated || (s.Encapsulated && !s.Supported)
}

func (s Syntax) HasCodec() bool {
	return s.CodecAvailable
}

var (
	ImplicitVRLittleEndian = Syntax{
		UID:            "1.2.840.10008.1.2",
		Name:           "Implicit VR Little Endian",
		ExplicitVR:     false,
		ByteOrder:      binary.LittleEndian,
		Supported:      true,
		CodecAvailable: true,
	}
	ExplicitVRLittleEndian = Syntax{
		UID:            "1.2.840.10008.1.2.1",
		Name:           "Explicit VR Little Endian",
		ExplicitVR:     true,
		ByteOrder:      binary.LittleEndian,
		Supported:      true,
		CodecAvailable: true,
	}
	DeflatedExplicitVRLittleEndian = Syntax{
		UID:            "1.2.840.10008.1.2.1.99",
		Name:           "Deflated Explicit VR Little Endian",
		ExplicitVR:     true,
		ByteOrder:      binary.LittleEndian,
		Supported:      false,
		Deflated:       true,
		CodecAvailable: false,
	}
	ExplicitVRBigEndian = Syntax{
		UID:            "1.2.840.10008.1.2.2",
		Name:           "Explicit VR Big Endian",
		ExplicitVR:     true,
		ByteOrder:      binary.BigEndian,
		Supported:      true,
		CodecAvailable: true,
	}
	EncapsulatedUncompressedExplicitVRLittleEndian = newFragmentOnlySyntax(
		"1.2.840.10008.1.2.1.98",
		"Encapsulated Uncompressed Explicit VR Little Endian",
	)
	JPEGBaseline = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.50",
		"JPEG Baseline (Process 1)",
	)
	JPEGExtended = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.51",
		"JPEG Extended (Process 2 & 4)",
	)
	JPEGLosslessNonHierarchical = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.57",
		"JPEG Lossless, Non-Hierarchical (Process 14)",
	)
	JPEGLosslessSV1 = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.70",
		"JPEG Lossless, Non-Hierarchical, First-Order Prediction",
	)
	JPEGLSLossless = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.80",
		"JPEG-LS Lossless Image Compression",
	)
	JPEGLSNearLossless = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.81",
		"JPEG-LS Lossy (Near-Lossless) Image Compression",
	)
	JPEG2000LosslessOnly = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.90",
		"JPEG 2000 Image Compression (Lossless Only)",
	)
	JPEG2000 = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.91",
		"JPEG 2000 Image Compression",
	)
	JPEG2000Part2Lossless = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.92",
		"JPEG 2000 Part 2 Multi-component Image Compression (Lossless Only)",
	)
	JPEG2000Part2 = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.93",
		"JPEG 2000 Part 2 Multi-component Image Compression",
	)
	RLELossless = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.5",
		"RLE Lossless",
	)

	JPIPReferenced = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.94",
		"JPIP Referenced",
	)
	JPIPReferencedDeflate = newDeflatedSyntax(
		"1.2.840.10008.1.2.4.95",
		"JPIP Referenced Deflate",
	)
	MPEG2MPML = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.100",
		"MPEG2 Main Profile / Main Level",
	)
	MPEG2MPMLF = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.100.1",
		"Fragmentable MPEG2 Main Profile / Main Level",
	)
	MPEG2MPHL = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.101",
		"MPEG2 Main Profile / High Level",
	)
	MPEG2MPHLF = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.101.1",
		"Fragmentable MPEG2 Main Profile / High Level",
	)
	MPEG4HP41 = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.102",
		"MPEG-4 AVC/H.264 High Profile / Level 4.1",
	)
	MPEG4HP41F = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.102.1",
		"Fragmentable MPEG-4 AVC/H.264 High Profile / Level 4.1",
	)
	MPEG4HP41BD = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.103",
		"MPEG-4 AVC/H.264 BD-compatible High Profile / Level 4.1",
	)
	MPEG4HP41BDF = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.103.1",
		"Fragmentable MPEG-4 AVC/H.264 BD-compatible High Profile / Level 4.1",
	)
	MPEG4HP422D = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.104",
		"MPEG-4 AVC/H.264 High Profile / Level 4.2 For 2D Video",
	)
	MPEG4HP422DF = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.104.1",
		"Fragmentable MPEG-4 AVC/H.264 High Profile / Level 4.2 For 2D Video",
	)
	MPEG4HP423D = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.105",
		"MPEG-4 AVC/H.264 High Profile / Level 4.2 For 3D Video",
	)
	MPEG4HP423DF = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.105.1",
		"Fragmentable MPEG-4 AVC/H.264 High Profile / Level 4.2 For 3D Video",
	)
	MPEG4HP42STEREO = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.106",
		"MPEG-4 AVC/H.264 Stereo High Profile / Level 4.2",
	)
	MPEG4HP42STEREOF = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.106.1",
		"Fragmentable MPEG-4 AVC/H.264 Stereo High Profile / Level 4.2",
	)
	HEVCMP51 = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.107",
		"HEVC/H.265 Main Profile / Level 5.1",
	)
	HEVCM10P51 = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.108",
		"HEVC/H.265 Main 10 Profile / Level 5.1",
	)
	JPEGXLLossless = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.110",
		"JPEG XL Lossless",
	)
	JPEGXLJPEGRecompression = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.111",
		"JPEG XL JPEG Recompression",
	)
	JPEGXL = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.112",
		"JPEG XL",
	)
	HTJ2KLossless = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.201",
		"High-Throughput JPEG 2000 Image Compression (Lossless Only)",
	)
	HTJ2KLosslessRPCL = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.202",
		"High-Throughput JPEG 2000 with RPCL Options Image Compression (Lossless Only)",
	)
	HTJ2K = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.203",
		"High-Throughput JPEG 2000 Image Compression",
	)
	JPIPHTJ2KReferenced = newEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.204",
		"JPIP HTJ2K Referenced",
	)
	JPIPHTJ2KReferencedDeflate = newDeflatedSyntax(
		"1.2.840.10008.1.2.4.205",
		"JPIP HTJ2K Referenced Deflate",
	)
	RFC2557MIMEEncapsulation = newUnsupportedExplicitLESyntax(
		"1.2.840.10008.1.2.6.1",
		"RFC 2557 MIME encapsulation (Retired)",
	)
	XMLEncoding = newUnsupportedExplicitLESyntax(
		"1.2.840.10008.1.2.6.2",
		"XML Encoding (Retired)",
	)
	SMPTEST211020UncompressedProgressiveActiveVideo = newUnsupportedExplicitLESyntax(
		"1.2.840.10008.1.2.7.1",
		"SMPTE ST 2110-20 Uncompressed Progressive Active Video",
	)
	SMPTEST211020UncompressedInterlacedActiveVideo = newUnsupportedExplicitLESyntax(
		"1.2.840.10008.1.2.7.2",
		"SMPTE ST 2110-20 Uncompressed Interlaced Active Video",
	)
	SMPTEST211030PCMDigitalAudio = newUnsupportedExplicitLESyntax(
		"1.2.840.10008.1.2.7.3",
		"SMPTE ST 2110-30 PCM Digital Audio",
	)
	DeflatedImageFrameCompression = newDeflatedSyntax(
		"1.2.840.10008.1.2.8.1",
		"Deflated Image Frame Compression",
	)
)

func newEncapsulatedSyntax(uid, name string) Syntax {
	return Syntax{
		UID:            uid,
		Name:           name,
		ExplicitVR:     true,
		ByteOrder:      binary.LittleEndian,
		Supported:      false,
		Encapsulated:   true,
		CodecAvailable: false,
	}
}

func newFragmentOnlySyntax(uid, name string) Syntax {
	return Syntax{
		UID:            uid,
		Name:           name,
		ExplicitVR:     true,
		ByteOrder:      binary.LittleEndian,
		Supported:      true,
		Encapsulated:   true,
		CodecAvailable: false,
	}
}

func newDeflatedSyntax(uid, name string) Syntax {
	return Syntax{
		UID:            uid,
		Name:           name,
		ExplicitVR:     true,
		ByteOrder:      binary.LittleEndian,
		Supported:      false,
		Deflated:       true,
		CodecAvailable: false,
	}
}

func newUnsupportedExplicitLESyntax(uid, name string) Syntax {
	return Syntax{
		UID:            uid,
		Name:           name,
		ExplicitVR:     true,
		ByteOrder:      binary.LittleEndian,
		Supported:      false,
		CodecAvailable: false,
	}
}
