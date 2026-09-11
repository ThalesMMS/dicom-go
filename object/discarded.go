package object

import "github.com/ThalesMMS/dicom-go/core"

// HasDiscardedValues reports payloads consumed without retention, including in
// sequence items. This state follows the element when datasets are copied.
// Writing is forbidden until the caller explicitly replaces/removes that value.
func (o *Object) HasDiscardedValues() bool {
	if o == nil {
		return false
	}
	return hasDiscardedElements(o.Elements())
}
func hasDiscardedElements(elements []core.Element) bool {
	for _, e := range elements {
		switch v := e.Value.(type) {
		case core.DiscardedValue:
			return true
		case core.SequenceValue:
			for _, item := range v.Items {
				if hasDiscardedElements(item.Elements) {
					return true
				}
			}
		}
	}
	return false
}
