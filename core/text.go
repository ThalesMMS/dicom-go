package core

import (
	"bytes"
	"strings"
)

const uidPaddingCutset = " \x00"

// NormalizeUID trims trailing DICOM UID padding spaces and NUL bytes. It leaves
// leading and interior bytes unchanged.
func NormalizeUID(uid string) string {
	return strings.TrimRight(uid, uidPaddingCutset)
}

// TrimTextValueBytes removes trailing DICOM padding according to the element VR.
func TrimTextValueBytes(vr VR, data []byte) []byte {
	switch vr {
	case VRUI:
		return bytes.TrimRight(data, uidPaddingCutset)
	case VRAE, VRAS, VRCS, VRDA, VRDS, VRDT, VRIS, VRLO, VRLT, VRPN, VRSH, VRST, VRTM, VRUC, VRUR, VRUT:
		return bytes.TrimRight(data, " ")
	default:
		return bytes.TrimRight(data, uidPaddingCutset)
	}
}

// TrimTextValue removes trailing DICOM padding according to the element VR.
func TrimTextValue(vr VR, s string) string {
	switch vr {
	case VRUI:
		return NormalizeUID(s)
	case VRAE, VRAS, VRCS, VRDA, VRDS, VRDT, VRIS, VRLO, VRLT, VRPN, VRSH, VRST, VRTM, VRUC, VRUR, VRUT:
		return strings.TrimRight(s, " ")
	default:
		return strings.TrimRight(s, uidPaddingCutset)
	}
}

// CleanTextValue removes embedded NUL bytes, applies VR-specific trailing
// padding trim, and trims surrounding whitespace for display-oriented text.
func CleanTextValue(vr VR, s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	return strings.TrimSpace(TrimTextValue(vr, s))
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
