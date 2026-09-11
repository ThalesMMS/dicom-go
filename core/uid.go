package core

// IsValidUID reports whether uid uses canonical DICOM UID syntax. The value
// must contain at least two numeric OID components, use root arc 0, 1, or 2,
// obey the OID second-arc limit for roots 0 and 1, contain no leading zeroes,
// and fit the 64-byte UI VR limit. Encoded DICOM padding is not syntax;
// callers that accept it must call NormalizeUID separately first.
func IsValidUID(uid string) bool {
	if len(uid) < 3 || len(uid) > 64 {
		return false
	}
	componentIndex := 0
	componentStart := 0
	root := byte(0)
	for index := 0; index <= len(uid); index++ {
		if index < len(uid) && uid[index] != '.' {
			if uid[index] < '0' || uid[index] > '9' {
				return false
			}
			continue
		}
		componentLength := index - componentStart
		if componentLength == 0 || componentLength > 1 && uid[componentStart] == '0' {
			return false
		}
		switch componentIndex {
		case 0:
			if componentLength != 1 || uid[componentStart] > '2' {
				return false
			}
			root = uid[componentStart]
		case 1:
			if root != '2' {
				secondArc := int(uid[componentStart] - '0')
				if componentLength == 2 {
					secondArc = secondArc*10 + int(uid[componentStart+1]-'0')
				}
				if componentLength > 2 || secondArc > 39 {
					return false
				}
			}
		}
		componentIndex++
		componentStart = index + 1
	}
	return componentIndex >= 2
}
