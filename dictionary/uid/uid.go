package uid

import (
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
)

// Type identifies the standard DICOM UID category from PS3.6 table A-1.
type Type uint8

const (
	SOPClass Type = iota
	MetaSOPClass
	TransferSyntax
	WellKnownSOPInstance
	DICOMUIDAsCodingScheme
	CodingScheme
	ApplicationContextName
	ServiceClass
	ApplicationHostingModel
	MappingResource
	LDAPOID
	SynchronizationFrameOfReference
)

var typeLabels = map[Type]string{
	SOPClass:                        "SOP Class",
	MetaSOPClass:                    "Meta SOP Class",
	TransferSyntax:                  "Transfer Syntax",
	WellKnownSOPInstance:            "Well-known SOP Instance",
	DICOMUIDAsCodingScheme:          "DICOM UIDs as a Coding Scheme",
	CodingScheme:                    "Coding Scheme",
	ApplicationContextName:          "Application Context Name",
	ServiceClass:                    "Service Class",
	ApplicationHostingModel:         "Application Hosting Model",
	MappingResource:                 "Mapping Resource",
	LDAPOID:                         "LDAP OID",
	SynchronizationFrameOfReference: "Synchronization Frame of Reference",
}

var reverseTypeLabels = func() map[string]Type {
	out := make(map[string]Type, len(typeLabels))
	for typ, label := range typeLabels {
		out[label] = typ
	}
	return out
}()

// Entry describes one standard DICOM UID record.
type Entry struct {
	UID     string
	Name    string
	Keyword string
	Type    Type
	Retired bool
}

// Registry resolves DICOM UIDs by UID or keyword.
//
// Keyword lookup is case-insensitive by contract so callers do not need to
// preserve standard DICOM casing.
type Registry interface {
	ByUID(uid string) (Entry, bool)
	ByKeyword(keyword string) (Entry, bool)
}

// NormalizeUID trims trailing spaces and NUL bytes from a DICOM UID.
func NormalizeUID(uid string) string {
	return core.NormalizeUID(uid)
}

// ParseType converts the standard PS3.6 type label into a Type.
func ParseType(value string) (Type, error) {
	if typ, ok := reverseTypeLabels[strings.TrimSpace(value)]; ok {
		return typ, nil
	}
	return 0, fmt.Errorf("unknown UID type %q", value)
}

func (t Type) String() string {
	if label, ok := typeLabels[t]; ok {
		return label
	}
	return fmt.Sprintf("Type(%d)", t)
}
