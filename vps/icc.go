package vps

import (
	"encoding/binary"
	"math"
)

type iccTag struct {
	signature string
	value     []byte
}

// SRGBICCProfile returns a fresh deterministic ICC v4 matrix/TRC monitor
// profile for the sRGB colour space. Callers may safely mutate the result.
func SRGBICCProfile() []byte {
	tags := []iccTag{
		{"desc", iccMLUC("Twin Viewer sRGB")},
		{"cprt", iccMLUC("Public domain")},
		{"wtpt", iccXYZ(.9642, 1, .8249)},
		{"rXYZ", iccXYZ(.4360747, .2225045, .0139322)},
		{"gXYZ", iccXYZ(.3850649, .7168786, .0971045)},
		{"bXYZ", iccXYZ(.1430804, .0606169, .7141733)},
		{"rTRC", iccSRGBCurve()},
		{"gTRC", iccSRGBCurve()},
		{"bTRC", iccSRGBCurve()},
	}
	tagTableEnd := 128 + 4 + len(tags)*12
	offset := tagTableEnd
	for i := range tags {
		tags[i].value = iccPadded(tags[i].value)
		offset += len(tags[i].value)
	}
	profile := make([]byte, offset)
	binary.BigEndian.PutUint32(profile[0:4], uint32(len(profile)))
	copy(profile[4:8], "vps ")
	binary.BigEndian.PutUint32(profile[8:12], 0x04300000)
	copy(profile[12:16], "mntr")
	copy(profile[16:20], "RGB ")
	copy(profile[20:24], "XYZ ")
	binary.BigEndian.PutUint16(profile[24:26], 2026)
	binary.BigEndian.PutUint16(profile[26:28], 1)
	binary.BigEndian.PutUint16(profile[28:30], 1)
	copy(profile[36:40], "acsp")
	copy(profile[40:44], "APPL")
	binary.BigEndian.PutUint32(profile[68:72], uint32(iccFixed(.9642)))
	binary.BigEndian.PutUint32(profile[72:76], uint32(iccFixed(1)))
	binary.BigEndian.PutUint32(profile[76:80], uint32(iccFixed(.8249)))
	copy(profile[80:84], "vps ")
	binary.BigEndian.PutUint32(profile[128:132], uint32(len(tags)))
	dataOffset := tagTableEnd
	for index, tag := range tags {
		record := 132 + index*12
		copy(profile[record:record+4], tag.signature)
		binary.BigEndian.PutUint32(profile[record+4:record+8], uint32(dataOffset))
		binary.BigEndian.PutUint32(profile[record+8:record+12], uint32(len(tag.value)))
		copy(profile[dataOffset:], tag.value)
		dataOffset += len(tag.value)
	}
	return profile
}

func iccMLUC(value string) []byte {
	encoded := make([]byte, len(value)*2)
	for index := range len(value) {
		encoded[index*2+1] = value[index]
	}
	out := make([]byte, 28+len(encoded))
	copy(out[0:4], "mluc")
	binary.BigEndian.PutUint32(out[8:12], 1)
	binary.BigEndian.PutUint32(out[12:16], 12)
	copy(out[16:20], "enUS")
	binary.BigEndian.PutUint32(out[20:24], uint32(len(encoded)))
	binary.BigEndian.PutUint32(out[24:28], 28)
	copy(out[28:], encoded)
	return out
}

func iccXYZ(x, y, z float64) []byte {
	out := make([]byte, 20)
	copy(out[0:4], "XYZ ")
	for index, value := range []float64{x, y, z} {
		binary.BigEndian.PutUint32(out[8+index*4:12+index*4], uint32(iccFixed(value)))
	}
	return out
}

func iccSRGBCurve() []byte {
	// ICC parametric curve type 4 is the exact piecewise sRGB transfer curve.
	values := []float64{2.4, 1 / 1.055, .055 / 1.055, 1 / 12.92, .04045, 0, 0}
	out := make([]byte, 12+len(values)*4)
	copy(out[0:4], "para")
	binary.BigEndian.PutUint16(out[8:10], 4)
	for index, value := range values {
		binary.BigEndian.PutUint32(out[12+index*4:16+index*4], uint32(iccFixed(value)))
	}
	return out
}

func iccFixed(value float64) int32 { return int32(math.Round(value * 65536)) }

func iccPadded(value []byte) []byte {
	if len(value)%4 == 0 {
		return value
	}
	return append(value, make([]byte, 4-len(value)%4)...)
}
