package core

import (
	"bytes"
	"strings"
)

// TrimTextValueBytes removes trailing DICOM padding according to the element VR.
func TrimTextValueBytes(vr VR, data []byte) []byte {
	switch vr {
	case VRUI:
		return bytes.TrimRight(data, " \x00")
	case VRAE, VRAS, VRCS, VRDA, VRDS, VRDT, VRIS, VRLO, VRLT, VRPN, VRSH, VRST, VRTM, VRUC, VRUR, VRUT:
		return bytes.TrimRight(data, " ")
	default:
		return bytes.TrimRight(data, " \x00")
	}
}

// TrimTextValue removes trailing DICOM padding according to the element VR.
func TrimTextValue(vr VR, s string) string {
	switch vr {
	case VRUI:
		return strings.TrimRight(s, " \x00")
	case VRAE, VRAS, VRCS, VRDA, VRDS, VRDT, VRIS, VRLO, VRLT, VRPN, VRSH, VRST, VRTM, VRUC, VRUR, VRUT:
		return strings.TrimRight(s, " ")
	default:
		return strings.TrimRight(s, " \x00")
	}
}

// SplitTextMultiplicity splits an already decoded DICOM textual value by the
// value multiplicity delimiter and trims each component's trailing padding.
func SplitTextMultiplicity(vr VR, s string) []string {
	s = TrimTextValue(vr, s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\\")
	for i := range parts {
		parts[i] = TrimTextValue(vr, parts[i])
	}
	return parts
}
