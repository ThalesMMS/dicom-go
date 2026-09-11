package dimse

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	dicomencoding "github.com/ThalesMMS/dicom-go/encoding"
	"github.com/ThalesMMS/dicom-go/object"
)

// ErrCFindResponseCharacterSet identifies a missing, malformed, or
// incompatible character-set declaration required by a projected response.
var ErrCFindResponseCharacterSet = errors.New("dicom dimse: invalid C-FIND response Specific Character Set")

// requiredResponseCharacterSet returns the provider declaration only when the
// projected elements cannot be represented by the default DICOM repertoire.
// It validates both the declaration and the projected text without exposing
// any text values in diagnostics.
func requiredResponseCharacterSet(elements []core.Element, candidate *object.Object) (core.Element, bool, error) {
	if !responseNeedsSpecificCharacterSet(elements) {
		return core.Element{}, false, nil
	}
	if candidate == nil {
		return core.Element{}, false, ErrCFindResponseCharacterSet
	}
	declaration, ok := candidate.Get(tagMWLSpecificCharacterSet)
	if !ok || declaration.VR() != core.VRCS {
		return core.Element{}, false, ErrCFindResponseCharacterSet
	}
	values := declaration.StringValues()
	if len(values) == 0 {
		return core.Element{}, false, ErrCFindResponseCharacterSet
	}
	characterSet, err := dicomencoding.ParseCharacterSet(values...)
	if err != nil {
		return core.Element{}, false, fmt.Errorf("%w: unsupported declaration", ErrCFindResponseCharacterSet)
	}
	if err := validateResponseTextEncoding(elements, characterSet); err != nil {
		return core.Element{}, false, ErrCFindResponseCharacterSet
	}
	return declaration, true, nil
}

func responseNeedsSpecificCharacterSet(elements []core.Element) bool {
	for _, element := range elements {
		if element.Tag() == tagMWLSpecificCharacterSet {
			continue
		}
		if sequence, ok := element.Value.(core.SequenceValue); ok {
			for _, item := range sequence.Items {
				if responseNeedsSpecificCharacterSet(item.Elements) {
					return true
				}
			}
			continue
		}
		if !element.VR().UsesSpecificCharacterSet() {
			continue
		}
		switch value := element.Value.(type) {
		case core.StringValue:
			for _, text := range value {
				var err error
				if element.VR() == core.VRPN {
					_, err = dicomencoding.DefaultCharacterSet.EncodePersonName(text)
				} else {
					_, err = dicomencoding.DefaultCharacterSet.Encode(text)
				}
				if err != nil {
					return true
				}
			}
		case core.RawValue:
			for _, octet := range value {
				if octet == 0x1b || octet >= 0x80 {
					return true
				}
			}
		}
	}
	return false
}

func validateResponseTextEncoding(elements []core.Element, characterSet dicomencoding.SpecificCharacterSet) error {
	for _, element := range elements {
		if element.Tag() == tagMWLSpecificCharacterSet || !element.VR().UsesSpecificCharacterSet() && element.VR() != core.VRSQ {
			continue
		}
		switch value := element.Value.(type) {
		case core.StringValue:
			for _, text := range value {
				var err error
				if element.VR() == core.VRPN {
					_, err = characterSet.EncodePersonName(text)
				} else if !element.VR().UsesTextValueDelimiter() {
					_, err = characterSet.EncodeSingleValue(text)
				} else {
					_, err = characterSet.Encode(text)
				}
				if err != nil {
					return err
				}
			}
		case core.RawValue:
			if bytes.Contains([]byte(value), []byte{0x1b}) && !characterSetSupportsCodeExtensions(characterSet) {
				return ErrCFindResponseCharacterSet
			}
			var err error
			if element.VR() == core.VRPN {
				_, err = characterSet.DecodePersonName([]byte(value))
			} else if !element.VR().UsesTextValueDelimiter() {
				_, err = characterSet.DecodeSingleValue([]byte(value))
			} else {
				_, err = characterSet.Decode([]byte(value))
			}
			if err != nil {
				return err
			}
		case core.SequenceValue:
			for _, item := range value.Items {
				if err := validateResponseTextEncoding(item.Elements, characterSet); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func characterSetSupportsCodeExtensions(characterSet dicomencoding.SpecificCharacterSet) bool {
	for _, name := range characterSet.Names() {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(name)), "ISO 2022 ") {
			return true
		}
	}
	return false
}
