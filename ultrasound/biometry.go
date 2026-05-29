package ultrasound

import (
	"fmt"
	"image"
)

type BiometryKind string

const (
	BiometryBiparietalDiameter     BiometryKind = "biparietal-diameter"
	BiometryHeadCircumference      BiometryKind = "head-circumference"
	BiometryAbdominalCircumference BiometryKind = "abdominal-circumference"
	BiometryFemurLength            BiometryKind = "femur-length"
)

type CodedConcept struct {
	CodeValue              string
	CodingSchemeDesignator string
	CodeMeaning            string
}

type BiometryTemplate struct {
	Kind          BiometryKind
	Concept       CodedConcept
	Circumference bool
}

var biometryTemplates = map[BiometryKind]BiometryTemplate{
	BiometryBiparietalDiameter: {
		Kind:    BiometryBiparietalDiameter,
		Concept: CodedConcept{CodeValue: "11820-8", CodingSchemeDesignator: "LN", CodeMeaning: "Biparietal Diameter"},
	},
	BiometryHeadCircumference: {
		Kind:          BiometryHeadCircumference,
		Concept:       CodedConcept{CodeValue: "11984-2", CodingSchemeDesignator: "LN", CodeMeaning: "Head Circumference"},
		Circumference: true,
	},
	BiometryAbdominalCircumference: {
		Kind:          BiometryAbdominalCircumference,
		Concept:       CodedConcept{CodeValue: "11979-2", CodingSchemeDesignator: "LN", CodeMeaning: "Abdominal Circumference"},
		Circumference: true,
	},
	BiometryFemurLength: {
		Kind:    BiometryFemurLength,
		Concept: CodedConcept{CodeValue: "11963-6", CodingSchemeDesignator: "LN", CodeMeaning: "Femur Length"},
	},
}

func Template(kind BiometryKind) (BiometryTemplate, bool) {
	template, ok := biometryTemplates[kind]
	return template, ok
}

// MeasureBiometry performs only the explicitly selected geometric measurement.
// It intentionally does not infer gestational age, percentiles, or diagnoses.
func MeasureBiometry(frame FrameCalibration, kind BiometryKind, points []image.Point) (Measurement, BiometryTemplate, error) {
	template, ok := Template(kind)
	if !ok {
		return Measurement{}, BiometryTemplate{}, fmt.Errorf("dicom/ultrasound: unsupported biometry template %q", kind)
	}
	var (
		measurement Measurement
		err         error
	)
	if template.Circumference {
		measurement, err = Circumference(frame, points)
	} else if len(points) >= 2 {
		measurement, err = Distance(frame, points[0], points[1])
	} else {
		err = fmt.Errorf("dicom/ultrasound: %s requires two points", kind)
	}
	return measurement, template, err
}
