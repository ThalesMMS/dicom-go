package ultrasound

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ThalesMMS/dicom-go/sr"
)

// Annotation is a supported ultrasound quantitative SR item. It retains the
// original coded concept, units, source frame, spatial evidence, and optional
// ultrasound-region reference without adding clinical interpretation.
type Annotation struct {
	Template    BiometryTemplate
	Value       float64
	Units       sr.CodedEntry
	SourceImage sr.ImageReference
	Spatial     sr.SpatialReference
	Tracking    sr.TrackingIdentifier
	Region      *RegionReference
}

// AnnotationsFromMeasurementReport extracts supported fetal-biometry items
// from an already decoded DICOM measurement report.
func AnnotationsFromMeasurementReport(report *sr.MeasurementReport) []Annotation {
	if report == nil {
		return nil
	}
	templates := make(map[string]BiometryTemplate, len(biometryTemplates))
	for _, template := range biometryTemplates {
		key := template.Concept.CodingSchemeDesignator + "\x00" + template.Concept.CodeValue
		templates[key] = template
	}
	var out []Annotation
	for _, group := range report.Groups {
		for _, measurement := range group.Measurements {
			key := measurement.ConceptName.CodingSchemeDesignator + "\x00" + measurement.ConceptName.CodeValue
			template, ok := templates[key]
			if !ok {
				continue
			}
			annotation := Annotation{
				Template: template, Value: measurement.Value, Units: measurement.Units,
				SourceImage: cloneImageReference(measurement.Image),
				Spatial:     cloneSpatialReference(measurement.Spatial),
				Tracking:    group.Tracking,
			}
			if reference, ok := regionReferenceFromTracking(group.Tracking.Identifier); ok {
				annotation.Region = &reference
			}
			out = append(out, annotation)
		}
	}
	return out
}

func regionReferenceFromTracking(identifier string) (RegionReference, bool) {
	const marker = "[US frame "
	start := strings.LastIndex(identifier, marker)
	if start < 0 || !strings.HasSuffix(identifier, "]") {
		return RegionReference{}, false
	}
	fields := strings.Fields(strings.TrimSuffix(identifier[start+len(marker):], "]"))
	if len(fields) != 3 || fields[1] != "region" {
		return RegionReference{}, false
	}
	frame, frameErr := strconv.Atoi(fields[0])
	region, regionErr := strconv.Atoi(fields[2])
	if frameErr != nil || regionErr != nil || frame <= 0 || region <= 0 {
		return RegionReference{}, false
	}
	return RegionReference{FrameNumber: frame, RegionIndex: region - 1}, true
}

func cloneImageReference(in sr.ImageReference) sr.ImageReference {
	out := in
	out.Frames = append([]int(nil), in.Frames...)
	return out
}

func cloneSpatialReference(in sr.SpatialReference) sr.SpatialReference {
	out := in
	out.Coordinates = append([]sr.Point3D(nil), in.Coordinates...)
	return out
}

func (a Annotation) ValidateEvidence() error {
	if a.SourceImage.SOPClassUID == "" || a.SourceImage.SOPInstanceUID == "" {
		return fmt.Errorf("dicom/ultrasound: annotation has no source image evidence")
	}
	if a.Region != nil && len(a.SourceImage.Frames) > 0 {
		matches := false
		for _, frame := range a.SourceImage.Frames {
			matches = matches || frame == a.Region.FrameNumber
		}
		if !matches {
			return fmt.Errorf("dicom/ultrasound: annotation region frame is not referenced by its source image")
		}
	}
	return nil
}
