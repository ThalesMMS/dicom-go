package dimse

import (
	"github.com/ThalesMMS/dicom-go/net/ul"
)

// StudyRootFindSOPClassUID is the Query/Retrieve Information Model - FIND SOP
// Class UID for the Study Root information model.
const StudyRootFindSOPClassUID = "1.2.840.10008.5.1.4.1.2.2.1"

// DefaultFindTransferSyntaxes are the transfer syntaxes proposed for C-FIND
// identifier datasets.
//
// Minimal scope uses Implicit VR Little Endian, but we also propose Explicit VR
// Little Endian for broader interoperability.
var DefaultFindTransferSyntaxes = []string{ul.ImplicitVRLittleEndian, ul.ExplicitVRLittleEndian}

// StudyRootFindPresentationContext returns a presentation context proposal for
// Study Root Query/Retrieve Information Model - FIND.
func StudyRootFindPresentationContext() ul.PresentationContext {
	return ul.PresentationContext{
		AbstractSyntaxUID:  StudyRootFindSOPClassUID,
		TransferSyntaxUIDs: append([]string(nil), DefaultFindTransferSyntaxes...),
	}
}
