package core

import "fmt"

// VR is the DICOM value representation code associated with an element.
type VR string

const (
	VRUnknown VR = "UN"

	VRAE VR = "AE"
	VRAS VR = "AS"
	VRAT VR = "AT"
	VRCS VR = "CS"
	VRDA VR = "DA"
	VRDS VR = "DS"
	VRDT VR = "DT"
	VRFL VR = "FL"
	VRFD VR = "FD"
	VRIS VR = "IS"
	VRLO VR = "LO"
	VRLT VR = "LT"
	VROB VR = "OB"
	VROD VR = "OD"
	VROF VR = "OF"
	VROL VR = "OL"
	VROV VR = "OV"
	VROW VR = "OW"
	VRPN VR = "PN"
	VRSH VR = "SH"
	VRSL VR = "SL"
	VRSQ VR = "SQ"
	VRSS VR = "SS"
	VRSV VR = "SV"
	VRST VR = "ST"
	VRTM VR = "TM"
	VRUC VR = "UC"
	VRUI VR = "UI"
	VRUL VR = "UL"
	VRUN VR = "UN"
	VRUR VR = "UR"
	VRUS VR = "US"
	VRUT VR = "UT"
	VRUV VR = "UV"
)

var knownVRs = map[VR]struct{}{
	VRAE: {}, VRAS: {}, VRAT: {}, VRCS: {}, VRDA: {}, VRDS: {}, VRDT: {}, VRFL: {}, VRFD: {}, VRIS: {}, VRLO: {}, VRLT: {}, VROB: {}, VROD: {}, VROF: {}, VROL: {}, VROV: {}, VROW: {}, VRPN: {}, VRSH: {}, VRSL: {}, VRSQ: {}, VRSS: {}, VRSV: {}, VRST: {}, VRTM: {}, VRUC: {}, VRUI: {}, VRUL: {}, VRUN: {}, VRUR: {}, VRUS: {}, VRUT: {}, VRUV: {},
}

var spacePaddedVRs = map[VR]struct{}{
	VRAE: {},
	VRAS: {},
	VRCS: {},
	VRDA: {},
	VRDS: {},
	VRDT: {},
	VRIS: {},
	VRLO: {},
	VRLT: {},
	VRPN: {},
	VRSH: {},
	VRST: {},
	VRTM: {},
	VRUC: {},
	VRUR: {},
	VRUT: {},
}

func ParseVR(s string) (VR, error) {
	vr := VR(s)
	if _, ok := knownVRs[vr]; !ok {
		return "", fmt.Errorf("dicom: invalid VR %q", s)
	}
	return vr, nil
}

func (vr VR) String() string {
	if vr == "" {
		return string(VRUnknown)
	}
	return string(vr)
}

func (vr VR) UsesLongExplicitLength() bool {
	switch vr {
	case VROB, VROD, VROF, VROL, VROV, VROW, VRSQ, VRSV, VRUC, VRUR, VRUT, VRUN, VRUV:
		return true
	default:
		return false
	}
}

func (vr VR) IsStringLike() bool {
	switch vr {
	case VRAE, VRAS, VRCS, VRDA, VRDS, VRDT, VRIS, VRLO, VRLT, VRPN, VRSH, VRST, VRTM, VRUC, VRUI, VRUR, VRUT:
		return true
	default:
		return false
	}
}

// UsesSpecificCharacterSet reports whether this VR is decoded using the data
// set's Specific Character Set (0008,0005). Other textual VRs stay in the
// default DICOM repertoire even when a charset is declared.
func (vr VR) UsesSpecificCharacterSet() bool {
	switch vr {
	case VRLO, VRLT, VRPN, VRSH, VRST, VRUC, VRUT:
		return true
	default:
		return false
	}
}

// PaddingByte reports the byte used to pad an odd-length value for this VR
// when serialized according to the DICOM standard. String VRs use space
// padding except UI, while UI and binary VRs use NUL padding.
func (vr VR) PaddingByte() byte {
	if _, ok := spacePaddedVRs[vr]; ok {
		return ' '
	}
	return 0x00
}

// PadToEvenLength returns a copy of data padded to an even number of bytes
// using the VR's standard padding byte. If data already has even length,
// the returned slice is an unmodified copy.
func (vr VR) PadToEvenLength(data []byte) []byte {
	padded := CloneBytes(data)
	if len(padded)%2 == 0 {
		return padded
	}
	return append(padded, vr.PaddingByte())
}
