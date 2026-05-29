package transfer

import "encoding/binary"

type Syntax struct {
	UID          string
	Name         string
	ExplicitVR   bool
	ByteOrder    binary.ByteOrder
	Supported    bool
	Encapsulated bool
	Deflated     bool
	MediaPayload bool
	// CodecRequired marks metadata/payload-readable still-image syntaxes that
	// still require an optional pixel codec adapter for frame decoding.
	CodecRequired  bool
	CodecAvailable bool
}

func (s Syntax) IsLittleEndian() bool {
	return s.ByteOrder == nil || s.ByteOrder == binary.LittleEndian
}

func (s Syntax) RequiresCodec() bool {
	if s.MediaPayload {
		return false
	}
	return s.Deflated || s.CodecRequired || (s.Encapsulated && (!s.Supported || s.CodecAvailable))
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
		Supported:      true,
		Deflated:       true,
		CodecAvailable: true,
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
	JPEGBaseline = newSupportedEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.50",
		"JPEG Baseline (Process 1)",
		true,
	)
	JPEGExtended = newSupportedEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.51",
		"JPEG Extended (Process 2 & 4)",
		true,
	)
	JPEGLosslessNonHierarchical = newSupportedEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.57",
		"JPEG Lossless, Non-Hierarchical (Process 14)",
		true,
	)
	JPEGLosslessSV1 = newSupportedEncapsulatedSyntax(
		"1.2.840.10008.1.2.4.70",
		"JPEG Lossless, Non-Hierarchical, First-Order Prediction",
		true,
	)
	JPEGLSLossless = newJPEGLSSyntax(
		"1.2.840.10008.1.2.4.80",
		"JPEG-LS Lossless Image Compression",
	)
	JPEGLSNearLossless = newJPEGLSSyntax(
		"1.2.840.10008.1.2.4.81",
		"JPEG-LS Lossy (Near-Lossless) Image Compression",
	)
	JPEG2000LosslessOnly = newJPEG2000Syntax(
		"1.2.840.10008.1.2.4.90",
		"JPEG 2000 Image Compression (Lossless Only)",
	)
	JPEG2000 = newJPEG2000Syntax(
		"1.2.840.10008.1.2.4.91",
		"JPEG 2000 Image Compression",
	)
	JPEG2000Part2Lossless = newJPEG2000Syntax(
		"1.2.840.10008.1.2.4.92",
		"JPEG 2000 Part 2 Multi-component Image Compression (Lossless Only)",
	)
	JPEG2000Part2 = newJPEG2000Syntax(
		"1.2.840.10008.1.2.4.93",
		"JPEG 2000 Part 2 Multi-component Image Compression",
	)
	RLELossless = newSupportedEncapsulatedSyntax(
		"1.2.840.10008.1.2.5",
		"RLE Lossless",
		true,
	)

	JPIPReferenced = newJPIPReferencedSyntax(
		"1.2.840.10008.1.2.4.94",
		"JPIP Referenced",
	)
	JPIPReferencedDeflate = newJPIPReferencedDeflatedSyntax(
		"1.2.840.10008.1.2.4.95",
		"JPIP Referenced Deflate",
	)
	MPEG2MPML = newVideoMediaSyntax(
		"1.2.840.10008.1.2.4.100",
		"MPEG2 Main Profile / Main Level",
	)
	MPEG2MPMLF = newVideoMediaSyntax(
		"1.2.840.10008.1.2.4.100.1",
		"Fragmentable MPEG2 Main Profile / Main Level",
	)
	MPEG2MPHL = newVideoMediaSyntax(
		"1.2.840.10008.1.2.4.101",
		"MPEG2 Main Profile / High Level",
	)
	MPEG2MPHLF = newVideoMediaSyntax(
		"1.2.840.10008.1.2.4.101.1",
		"Fragmentable MPEG2 Main Profile / High Level",
	)
	MPEG4HP41 = newVideoMediaSyntax(
		"1.2.840.10008.1.2.4.102",
		"MPEG-4 AVC/H.264 High Profile / Level 4.1",
	)
	MPEG4HP41F = newVideoMediaSyntax(
		"1.2.840.10008.1.2.4.102.1",
		"Fragmentable MPEG-4 AVC/H.264 High Profile / Level 4.1",
	)
	MPEG4HP41BD = newVideoMediaSyntax(
		"1.2.840.10008.1.2.4.103",
		"MPEG-4 AVC/H.264 BD-compatible High Profile / Level 4.1",
	)
	MPEG4HP41BDF = newVideoMediaSyntax(
		"1.2.840.10008.1.2.4.103.1",
		"Fragmentable MPEG-4 AVC/H.264 BD-compatible High Profile / Level 4.1",
	)
	MPEG4HP422D = newVideoMediaSyntax(
		"1.2.840.10008.1.2.4.104",
		"MPEG-4 AVC/H.264 High Profile / Level 4.2 For 2D Video",
	)
	MPEG4HP422DF = newVideoMediaSyntax(
		"1.2.840.10008.1.2.4.104.1",
		"Fragmentable MPEG-4 AVC/H.264 High Profile / Level 4.2 For 2D Video",
	)
	MPEG4HP423D = newVideoMediaSyntax(
		"1.2.840.10008.1.2.4.105",
		"MPEG-4 AVC/H.264 High Profile / Level 4.2 For 3D Video",
	)
	MPEG4HP423DF = newVideoMediaSyntax(
		"1.2.840.10008.1.2.4.105.1",
		"Fragmentable MPEG-4 AVC/H.264 High Profile / Level 4.2 For 3D Video",
	)
	MPEG4HP42STEREO = newVideoMediaSyntax(
		"1.2.840.10008.1.2.4.106",
		"MPEG-4 AVC/H.264 Stereo High Profile / Level 4.2",
	)
	MPEG4HP42STEREOF = newVideoMediaSyntax(
		"1.2.840.10008.1.2.4.106.1",
		"Fragmentable MPEG-4 AVC/H.264 Stereo High Profile / Level 4.2",
	)
	HEVCMP51 = newVideoMediaSyntax(
		"1.2.840.10008.1.2.4.107",
		"HEVC/H.265 Main Profile / Level 5.1",
	)
	HEVCM10P51 = newVideoMediaSyntax(
		"1.2.840.10008.1.2.4.108",
		"HEVC/H.265 Main 10 Profile / Level 5.1",
	)
	JPEGXLLossless = newJPEGXLSyntax(
		"1.2.840.10008.1.2.4.110",
		"JPEG XL Lossless",
	)
	JPEGXLJPEGRecompression = newJPEGXLSyntax(
		"1.2.840.10008.1.2.4.111",
		"JPEG XL JPEG Recompression",
	)
	JPEGXL = newJPEGXLSyntax(
		"1.2.840.10008.1.2.4.112",
		"JPEG XL",
	)
	HTJ2KLossless = newJPEG2000Syntax(
		"1.2.840.10008.1.2.4.201",
		"High-Throughput JPEG 2000 Image Compression (Lossless Only)",
	)
	HTJ2KLosslessRPCL = newJPEG2000Syntax(
		"1.2.840.10008.1.2.4.202",
		"High-Throughput JPEG 2000 with RPCL Options Image Compression (Lossless Only)",
	)
	HTJ2K = newJPEG2000Syntax(
		"1.2.840.10008.1.2.4.203",
		"High-Throughput JPEG 2000 Image Compression",
	)
	JPIPHTJ2KReferenced = newJPIPReferencedSyntax(
		"1.2.840.10008.1.2.4.204",
		"JPIP HTJ2K Referenced",
	)
	JPIPHTJ2KReferencedDeflate = newJPIPReferencedDeflatedSyntax(
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

func newSupportedEncapsulatedSyntax(uid, name string, codecAvailable bool) Syntax {
	return Syntax{
		UID:            uid,
		Name:           name,
		ExplicitVR:     true,
		ByteOrder:      binary.LittleEndian,
		Supported:      true,
		Encapsulated:   true,
		CodecAvailable: codecAvailable,
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

func newVideoMediaSyntax(uid, name string) Syntax {
	return Syntax{
		UID:            uid,
		Name:           name,
		ExplicitVR:     true,
		ByteOrder:      binary.LittleEndian,
		Supported:      true,
		Encapsulated:   true,
		MediaPayload:   true,
		CodecAvailable: false,
	}
}

func newJPEGXLSyntax(uid, name string) Syntax {
	return newOptionalStillImageCodecSyntax(uid, name)
}

func newJPEGLSSyntax(uid, name string) Syntax {
	return newOptionalStillImageCodecSyntax(uid, name)
}

func newJPEG2000Syntax(uid, name string) Syntax {
	return newOptionalStillImageCodecSyntax(uid, name)
}

func newOptionalStillImageCodecSyntax(uid, name string) Syntax {
	return Syntax{
		UID:            uid,
		Name:           name,
		ExplicitVR:     true,
		ByteOrder:      binary.LittleEndian,
		Supported:      true,
		Encapsulated:   true,
		CodecRequired:  true,
		CodecAvailable: false,
	}
}

func newJPIPReferencedSyntax(uid, name string) Syntax {
	return Syntax{
		UID:            uid,
		Name:           name,
		ExplicitVR:     true,
		ByteOrder:      binary.LittleEndian,
		Supported:      true,
		CodecAvailable: false,
	}
}

// IsVideoTransferSyntax reports whether uid is one of the MPEG, MPEG-4
// AVC/H.264, or HEVC/H.265 transfer syntaxes that carry encapsulated video media
// payloads rather than locally renderable still-image frames.
func IsVideoTransferSyntax(uid string) bool {
	switch NormalizeUID(uid) {
	case MPEG2MPML.UID,
		MPEG2MPMLF.UID,
		MPEG2MPHL.UID,
		MPEG2MPHLF.UID,
		MPEG4HP41.UID,
		MPEG4HP41F.UID,
		MPEG4HP41BD.UID,
		MPEG4HP41BDF.UID,
		MPEG4HP422D.UID,
		MPEG4HP422DF.UID,
		MPEG4HP423D.UID,
		MPEG4HP423DF.UID,
		MPEG4HP42STEREO.UID,
		MPEG4HP42STEREOF.UID,
		HEVCMP51.UID,
		HEVCM10P51.UID:
		return true
	default:
		return false
	}
}

// IsJPEGLSTransferSyntax reports whether uid is one of the DICOM JPEG-LS
// still-image transfer syntaxes. These syntaxes are metadata/payload-readable
// but require an optional decoder adapter for pixel frame rendering.
func IsJPEGLSTransferSyntax(uid string) bool {
	switch NormalizeUID(uid) {
	case JPEGLSLossless.UID,
		JPEGLSNearLossless.UID:
		return true
	default:
		return false
	}
}

// IsJPEG2000TransferSyntax reports whether uid is one of the DICOM JPEG 2000
// or HTJ2K still-image transfer syntaxes. JPIP referenced syntaxes are excluded
// because they need external pixel-stream retrieval rather than local frame
// decode.
func IsJPEG2000TransferSyntax(uid string) bool {
	switch NormalizeUID(uid) {
	case JPEG2000LosslessOnly.UID,
		JPEG2000.UID,
		JPEG2000Part2Lossless.UID,
		JPEG2000Part2.UID,
		HTJ2KLossless.UID,
		HTJ2KLosslessRPCL.UID,
		HTJ2K.UID:
		return true
	default:
		return false
	}
}

// IsJPEGXLTransferSyntax reports whether uid is one of the DICOM JPEG XL
// still-image transfer syntaxes. These syntaxes are metadata/payload-readable
// but require an optional decoder adapter for pixel frame rendering.
func IsJPEGXLTransferSyntax(uid string) bool {
	switch NormalizeUID(uid) {
	case JPEGXLLossless.UID,
		JPEGXLJPEGRecompression.UID,
		JPEGXL.UID:
		return true
	default:
		return false
	}
}

func newJPIPReferencedDeflatedSyntax(uid, name string) Syntax {
	return Syntax{
		UID:            uid,
		Name:           name,
		ExplicitVR:     true,
		ByteOrder:      binary.LittleEndian,
		Supported:      true,
		Deflated:       true,
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
