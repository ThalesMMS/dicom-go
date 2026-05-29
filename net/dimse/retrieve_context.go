package dimse

import "github.com/ThalesMMS/dicom-go/net/ul"

// StudyRootMoveSOPClassUID is the Query/Retrieve Information Model - MOVE SOP
// Class UID for the Study Root information model.
const StudyRootMoveSOPClassUID = "1.2.840.10008.5.1.4.1.2.2.2"

// PatientRootMoveSOPClassUID is the Query/Retrieve Information Model - MOVE SOP
// Class UID for the Patient Root information model.
const PatientRootMoveSOPClassUID = "1.2.840.10008.5.1.4.1.2.1.2"

// StudyRootGetSOPClassUID is the Query/Retrieve Information Model - GET SOP
// Class UID for the Study Root information model.
const StudyRootGetSOPClassUID = "1.2.840.10008.5.1.4.1.2.2.3"

// PatientRootGetSOPClassUID is the Query/Retrieve Information Model - GET SOP
// Class UID for the Patient Root information model.
const PatientRootGetSOPClassUID = "1.2.840.10008.5.1.4.1.2.1.3"

// DefaultMoveTransferSyntaxes are the transfer syntaxes proposed for C-MOVE
// identifier datasets.
var DefaultMoveTransferSyntaxes = []string{ul.ImplicitVRLittleEndian, ul.ExplicitVRLittleEndian}

// DefaultGetTransferSyntaxes are the transfer syntaxes proposed for C-GET
// identifier datasets.
var DefaultGetTransferSyntaxes = []string{ul.ImplicitVRLittleEndian, ul.ExplicitVRLittleEndian}

// StudyRootMovePresentationContext returns a presentation context proposal for
// Study Root Query/Retrieve Information Model - MOVE.
func StudyRootMovePresentationContext() ul.PresentationContext {
	return presentationContextFor(StudyRootMoveSOPClassUID, DefaultMoveTransferSyntaxes)
}

// PatientRootMovePresentationContext returns a presentation context proposal
// for Patient Root Query/Retrieve Information Model - MOVE.
func PatientRootMovePresentationContext() ul.PresentationContext {
	return presentationContextFor(PatientRootMoveSOPClassUID, DefaultMoveTransferSyntaxes)
}

// StudyRootGetPresentationContext returns a presentation context proposal for
// Study Root Query/Retrieve Information Model - GET.
func StudyRootGetPresentationContext() ul.PresentationContext {
	return presentationContextFor(StudyRootGetSOPClassUID, DefaultGetTransferSyntaxes)
}

// PatientRootGetPresentationContext returns a presentation context proposal
// for Patient Root Query/Retrieve Information Model - GET.
func PatientRootGetPresentationContext() ul.PresentationContext {
	return presentationContextFor(PatientRootGetSOPClassUID, DefaultGetTransferSyntaxes)
}
