package sr

var (
	KOSTitleOfInterest = CodedEntry{CodeValue: "113000", CodingSchemeDesignator: "DCM", CodeMeaning: "Of Interest"}
	KOSFindingConcept  = CodedEntry{CodeValue: "121071", CodingSchemeDesignator: "DCM", CodeMeaning: "Finding"}
)

// NewKeyObjectSelection builds a KOS document that references the provided
// images and attaches optional textual findings by SOP Instance UID.
func NewKeyObjectSelection(sopInstanceUID string, refs []ImageReference, findings map[string]string) *Document {
	contentDate, contentTime := currentContentDateTime()
	doc := &Document{
		SOPClassUID:    KeyObjectSelectionDocumentStorage,
		SOPInstanceUID: sopInstanceUID,
		Modality:       "KO",
		Title:          KOSTitleOfInterest,
		ContentDate:    contentDate,
		ContentTime:    contentTime,
	}
	for _, ref := range refs {
		item := ContentItem{
			ValueType:        ValueImage,
			RelationshipType: RelationshipContains,
			Image: ImageReference{
				StudyInstanceUID:  ref.StudyInstanceUID,
				SeriesInstanceUID: ref.SeriesInstanceUID,
				SOPClassUID:       ref.SOPClassUID,
				SOPInstanceUID:    ref.SOPInstanceUID,
				Frames:            append([]int(nil), ref.Frames...),
			},
		}
		if findings != nil {
			if finding := findings[ref.SOPInstanceUID]; finding != "" {
				item.Children = []ContentItem{{
					ValueType:        ValueText,
					RelationshipType: RelationshipContains,
					ConceptName:      KOSFindingConcept,
					Text:             finding,
				}}
			}
		}
		doc.Content = append(doc.Content, item)
	}
	return doc
}

// KeyObjectSelectionImages extracts image references from a KOS document.
func KeyObjectSelectionImages(doc *Document) []ImageReference {
	if doc == nil {
		return nil
	}
	refs := make([]ImageReference, 0, len(doc.Content))
	for _, item := range doc.Content {
		if item.ValueType != ValueImage {
			continue
		}
		ref := item.Image
		ref.Frames = append([]int(nil), ref.Frames...)
		refs = append(refs, ref)
	}
	return refs
}

// KeyObjectSelectionFindings extracts textual findings by SOP Instance UID.
func KeyObjectSelectionFindings(doc *Document) map[string]string {
	out := map[string]string{}
	if doc == nil {
		return out
	}
	for _, item := range doc.Content {
		if item.ValueType != ValueImage || item.Image.SOPInstanceUID == "" {
			continue
		}
		for _, child := range item.Children {
			if child.ValueType == ValueText && child.Text != "" {
				out[item.Image.SOPInstanceUID] = child.Text
				break
			}
		}
	}
	return out
}
