package validation_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/transfer"
	"github.com/ThalesMMS/dicom-go/validation"
)

var (
	tagSOPClassUID             = core.NewTag(0x0008, 0x0016)
	tagSOPInstanceUID          = core.NewTag(0x0008, 0x0018)
	tagSpecificCharacterSet    = core.NewTag(0x0008, 0x0005)
	tagPatientName             = core.NewTag(0x0010, 0x0010)
	tagPatientID               = core.NewTag(0x0010, 0x0020)
	tagReferencedStudySeq      = core.NewTag(0x0008, 0x1110)
	tagReferencedSOPClassUID   = core.NewTag(0x0008, 0x1150)
	tagMediaStorageSOPClass    = core.NewTag(0x0002, 0x0002)
	tagMediaStorageSOPInstance = core.NewTag(0x0002, 0x0003)
	tagTransferSyntaxUID       = core.NewTag(0x0002, 0x0010)
)

func TestValidateDataSetModesPreserveInputAndRedactValues(t *testing.T) {
	invalid := textElement(tagPatientID, core.VRLO, strings.Repeat("sensitive", 9))
	dataset := core.DataSet{Elements: []core.Element{invalid}}

	for _, tc := range []struct {
		name    string
		mode    validation.Mode
		wantErr bool
		wantSev validation.Severity
	}{
		{name: "strict", mode: validation.ModeStrict, wantErr: true, wantSev: validation.SeverityError},
		{name: "warn", mode: validation.ModeWarn, wantSev: validation.SeverityWarning},
		{name: "preserve", mode: validation.ModePreserve, wantSev: validation.SeverityError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := validation.ValidateDataSet(context.Background(), dataset, validation.Options{
				Mode:       tc.mode,
				Dictionary: std.Dictionary,
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateDataSet() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, validation.ErrValidationFailed) {
				t.Fatalf("error = %v, want ErrValidationFailed", err)
			}
			if len(result.Report.Findings) == 0 || result.Report.Findings[0].Severity != tc.wantSev {
				t.Fatalf("findings = %#v, want severity %s", result.Report.Findings, tc.wantSev)
			}
			if strings.Contains(result.Report.Findings[0].Message, "sensitive") || strings.Contains(errString(err), "sensitive") {
				t.Fatal("validation diagnostic leaked an element value")
			}
			if got := result.DataSet.Elements[0].StringValue(); got != invalid.StringValue() {
				t.Fatalf("preserved value = %q, want unchanged input", got)
			}
		})
	}
}

func TestStopFirstAppliesToEveryModeWithoutDroppingPreservedElements(t *testing.T) {
	dataset := core.DataSet{Elements: []core.Element{
		textElement(tagPatientID, core.VRLO, strings.Repeat("x", 65)),
		textElement(tagPatientName, core.VRPN, "A^B^C^D^E^F"),
	}}
	for _, mode := range []validation.Mode{validation.ModePreserve, validation.ModeWarn, validation.ModeStrict} {
		result, err := validation.ValidateDataSet(context.Background(), dataset, validation.Options{Mode: mode, StopFirst: true})
		if mode == validation.ModeStrict {
			if err == nil || !errors.Is(err, validation.ErrValidationFailed) {
				t.Fatalf("mode %v error = %v, want ErrValidationFailed", mode, err)
			}
		} else if err != nil {
			t.Fatalf("mode %v error = %v", mode, err)
		}
		if len(result.Report.Findings) != 1 {
			t.Fatalf("mode %v findings = %#v, want exactly one", mode, result.Report.Findings)
		}
		if len(result.DataSet.Elements) != len(dataset.Elements) || result.DataSet.Elements[1].StringValue() != dataset.Elements[1].StringValue() {
			t.Fatalf("mode %v did not preserve unvalidated suffix: %#v", mode, result.DataSet)
		}
	}
}

func TestStopFirstSkipsLaterDataSetRules(t *testing.T) {
	firstCalls := 0
	secondCalls := 0
	first := validation.DataSetRuleFunc(func(context.Context, validation.DataSetContext) []validation.Finding {
		firstCalls++
		return []validation.Finding{{Code: validation.CodeDataSetRule}}
	})
	second := validation.DataSetRuleFunc(func(context.Context, validation.DataSetContext) []validation.Finding {
		secondCalls++
		return nil
	})
	_, err := validation.ValidateDataSet(context.Background(), core.DataSet{}, validation.Options{
		StopFirst: true,
		DataSetRules: []validation.DataSetRuleRegistration{
			{Name: "first", Rule: first},
			{Name: "second", Rule: second},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if firstCalls != 1 || secondCalls != 0 {
		t.Fatalf("rule calls = first:%d second:%d, want 1/0", firstCalls, secondCalls)
	}
}

func TestValidateDataSetStopFirstStopsHookChainAndKeepsAtomicProvenance(t *testing.T) {
	original := textElement(tagPatientID, core.VRLO, "ORIGINAL")
	replacement := textElement(tagPatientID, core.VRLO, "REPLACED")
	laterCalls := 0
	chain, err := validation.NewHookChain(
		validation.HookRegistration{
			Name: "finding-and-transform", Points: []validation.HookPoint{validation.HookAfterElement},
			Hook: validation.HookFunc(func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
				return validation.HookDecision{Element: &replacement, Findings: []validation.Finding{{Code: validation.CodeHookDiagnostic}}}, nil
			}),
		},
		validation.HookRegistration{
			Name: "later", Points: []validation.HookPoint{validation.HookAfterElement},
			Hook: validation.HookFunc(func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
				laterCalls++
				return validation.HookDecision{Filter: true}, nil
			}),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := validation.ValidateDataSet(context.Background(), core.DataSet{Elements: []core.Element{original}}, validation.Options{StopFirst: true, Hooks: chain})
	if err != nil {
		t.Fatal(err)
	}
	if laterCalls != 0 || len(result.DataSet.Elements) != 1 || result.DataSet.Elements[0].StringValue() != "REPLACED" {
		t.Fatalf("result=%#v laterCalls=%d", result, laterCalls)
	}
	if len(result.Report.Findings) != 1 || len(result.Report.Changes) != 1 || result.Report.Changes[0].Kind != validation.ChangeTransformed {
		t.Fatalf("report = %#v", result.Report)
	}
}

func TestValidateDataSetReportsNestedPathDuplicatesOrderingAndBound(t *testing.T) {
	nestedInvalid := textElement(tagReferencedSOPClassUID, core.VRUI, "1..2")
	sequence := core.Element{
		Header: core.ElementHeader{Tag: tagReferencedStudySeq, VR: core.VRSQ},
		Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			nestedInvalid,
		}}}},
	}
	dataset := core.DataSet{Elements: []core.Element{
		textElement(tagPatientID, core.VRLO, "one"),
		textElement(tagPatientName, core.VRPN, "DOE^JANE"),
		textElement(tagPatientID, core.VRLO, "two"),
		sequence,
	}}

	result, err := validation.ValidateDataSet(context.Background(), dataset, validation.Options{
		Mode:         validation.ModePreserve,
		Dictionary:   std.Dictionary,
		MaxFindings:  4,
		RequiredUIDs: []core.Tag{tagSOPClassUID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Report.Findings) != 4 || !result.Report.Truncated || result.Report.Dropped == 0 {
		t.Fatalf("bounded report = %#v", result.Report)
	}
	assertFindingCode(t, result.Report, validation.CodeDuplicateElement)
	assertFindingCode(t, result.Report, validation.CodeElementOrder)
	var nested validation.Finding
	for _, finding := range result.Report.Findings {
		if finding.Tag == tagReferencedSOPClassUID {
			nested = finding
			break
		}
	}
	if got, want := nested.Path.String(), tagReferencedStudySeq.String()+"[0]/"+tagReferencedSOPClassUID.String(); got != want {
		t.Fatalf("nested path = %q, want %q (findings %#v)", got, want, result.Report.Findings)
	}
}

func TestValidationLimitsCountSequenceItemsBeforeCloneAllocation(t *testing.T) {
	items := make([]core.DataSet, 32)
	dataset := core.DataSet{Elements: []core.Element{{
		Header: core.ElementHeader{Tag: tagReferencedStudySeq, VR: core.VRSQ},
		Value:  core.SequenceValue{Items: items},
	}}}
	_, err := validation.ValidateDataSet(context.Background(), dataset, validation.Options{MaxElements: 8})
	if err == nil || !errors.Is(err, validation.ErrValidationLimit) {
		t.Fatalf("ValidateDataSet() error = %v, want ErrValidationLimit", err)
	}
}

func TestValidateDataSetDictionaryVMPrivateOverlayAndAmbiguousResolver(t *testing.T) {
	privateTag := core.NewTag(0x0011, 0x1010)
	overlay := fixedDictionary{entries: map[core.Tag]dictionary.Entry{
		privateTag: {Tag: privateTag, VR: core.VRCS, Keyword: "PrivateCode", VM: "2-2n"},
	}}
	ambiguousTag := core.NewTag(0x0028, 0x3006)
	dataset := core.DataSet{Elements: []core.Element{
		textElement(privateTag, core.VRLO, "ONE\\TWO\\THREE"),
		{Header: core.ElementHeader{Tag: ambiguousTag, VR: core.VRSS}, Value: core.Int16Value{1}},
	}}

	result, err := validation.ValidateDataSet(context.Background(), dataset, validation.Options{
		Mode:       validation.ModePreserve,
		Dictionary: dictionary.Chain{overlay, std.Dictionary},
		ResolveVR: func(ctx validation.ElementContext, entry dictionary.Entry) []core.VR {
			if ctx.Element.Tag() == ambiguousTag {
				return []core.VR{core.VRUS, core.VRSS}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFindingCode(t, result.Report, validation.CodeDictionaryVR)
	assertFindingCode(t, result.Report, validation.CodeValueMultiplicity)
	for _, finding := range result.Report.Findings {
		if finding.Tag == ambiguousTag && finding.Code == validation.CodeDictionaryVR {
			t.Fatalf("ambiguous resolver produced false mismatch: %#v", finding)
		}
	}
}

func TestContextualVRResolverUsesPixelRepresentation(t *testing.T) {
	pixelRepresentation := core.NewTag(0x0028, 0x0103)
	smallestPixelValue := core.NewTag(0x0028, 0x0106)
	resolver := func(ctx validation.ElementContext, _ dictionary.Entry) []core.VR {
		if ctx.Element.Tag() != smallestPixelValue {
			return nil
		}
		for _, element := range ctx.DataSet.Elements {
			if element.Tag() != pixelRepresentation {
				continue
			}
			if values, ok := element.Value.(core.Uint16Value); ok && len(values) == 1 && values[0] == 1 {
				return []core.VR{core.VRSS}
			}
		}
		return []core.VR{core.VRUS}
	}
	for _, test := range []struct {
		name         string
		value        core.Element
		wantMismatch bool
	}{
		{name: "signed SS", value: core.Element{Header: core.ElementHeader{Tag: smallestPixelValue, VR: core.VRSS}, Value: core.Int16Value{-1}}},
		{name: "signed wrong US", value: core.Element{Header: core.ElementHeader{Tag: smallestPixelValue, VR: core.VRUS}, Value: core.Uint16Value{1}}, wantMismatch: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataset := core.DataSet{Elements: []core.Element{
				{Header: core.ElementHeader{Tag: pixelRepresentation, VR: core.VRUS}, Value: core.Uint16Value{1}},
				test.value,
			}}
			result, err := validation.ValidateDataSet(context.Background(), dataset, validation.Options{Dictionary: std.Dictionary, ResolveVR: resolver})
			if err != nil {
				t.Fatal(err)
			}
			gotMismatch := result.Report.Count(validation.CodeDictionaryVR) > 0
			if gotMismatch != test.wantMismatch {
				t.Fatalf("dictionary mismatch = %v, want %v: %#v", gotMismatch, test.wantMismatch, result.Report)
			}
		})
	}
}

func TestValidateFileChecksUIDAndTransferSyntaxConsistency(t *testing.T) {
	meta := core.DataSet{Elements: []core.Element{
		textElement(tagMediaStorageSOPClass, core.VRUI, "1.2.3"),
		textElement(tagMediaStorageSOPInstance, core.VRUI, "1.2.3.4"),
		textElement(tagTransferSyntaxUID, core.VRUI, transfer.ImplicitVRLittleEndian.UID),
	}}
	dataset := core.DataSet{Elements: []core.Element{
		textElement(tagSOPClassUID, core.VRUI, "1.2.840.10008.5.1.4.1.1.2"),
		textElement(tagSOPInstanceUID, core.VRUI, "1.2.3.999"),
	}}

	report, err := validation.ValidateFile(context.Background(), meta, dataset, transfer.ExplicitVRLittleEndian, validation.Options{
		Mode:       validation.ModePreserve,
		Dictionary: std.Dictionary,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Count(validation.CodeFileMetaMismatch); got != 3 {
		t.Fatalf("file meta mismatch count = %d, want 3: %#v", got, report.Findings)
	}
}

func TestValidateFileWarnModeDoesNotApplyDatasetRequiredUIDsToFileMeta(t *testing.T) {
	meta := core.DataSet{Elements: []core.Element{
		textElement(tagMediaStorageSOPClass, core.VRUI, "1.2.3"),
		textElement(tagMediaStorageSOPInstance, core.VRUI, "1.2.3.4"),
		textElement(tagTransferSyntaxUID, core.VRUI, transfer.ExplicitVRLittleEndian.UID),
	}}
	dataset := core.DataSet{Elements: []core.Element{
		textElement(tagSOPClassUID, core.VRUI, "1.2.3"),
		textElement(tagSOPInstanceUID, core.VRUI, "1.2.3.4"),
	}}
	report, err := validation.ValidateFile(context.Background(), meta, dataset, transfer.ExplicitVRLittleEndian, validation.Options{
		Mode: validation.ModeWarn, RequiredUIDs: []core.Tag{tagSOPClassUID, tagSOPInstanceUID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Count(validation.CodeRequiredUID) != 0 {
		t.Fatalf("file meta received dataset UID policy: %#v", report.Findings)
	}
	for _, finding := range report.Findings {
		if finding.Severity == validation.SeverityError {
			t.Fatalf("warn mode retained error finding: %#v", finding)
		}
	}
}

func TestValidateDataSetRequiredUIDsAndBinaryWidths(t *testing.T) {
	dataset := core.DataSet{Elements: []core.Element{
		{Header: core.ElementHeader{Tag: core.NewTag(0x0028, 0x0010), VR: core.VRUS}, Value: core.RawValue{1, 2, 3}},
	}}
	result, err := validation.ValidateDataSet(context.Background(), dataset, validation.Options{
		Mode:         validation.ModePreserve,
		Dictionary:   std.Dictionary,
		RequiredUIDs: []core.Tag{tagSOPClassUID, tagSOPInstanceUID},
		ByteOrder:    binary.LittleEndian,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Count(validation.CodeRequiredUID) != 2 {
		t.Fatalf("required UID findings = %#v", result.Report.Findings)
	}
	assertFindingCode(t, result.Report, validation.CodeValueLength)
}

func TestValidateEverySupportedVRHasValidAndInvalidFixture(t *testing.T) {
	fixtures := []struct {
		vr      core.VR
		valid   core.Value
		invalid core.Value
	}{
		{core.VRAE, core.StringValue{"REMOTE_AE"}, core.StringValue{"BAD\x01AE"}},
		{core.VRAS, core.StringValue{"018Y"}, core.StringValue{"18Y"}},
		{core.VRAT, core.TagValue{tagPatientID}, core.StringValue{"00100020"}},
		{core.VRCS, core.StringValue{"ORIGINAL"}, core.StringValue{"lowercase"}},
		{core.VRDA, core.StringValue{"20260228"}, core.StringValue{"20260230"}},
		{core.VRDS, core.StringValue{"1.25E+2"}, core.StringValue{"NaN"}},
		{core.VRDT, core.StringValue{"20260228123045.123456-0300"}, core.StringValue{"20261301"}},
		{core.VRFL, core.Float32Value{1.25}, core.StringValue{"1.25"}},
		{core.VRFD, core.Float64Value{1.25}, core.StringValue{"1.25"}},
		{core.VRIS, core.StringValue{"2147483647"}, core.StringValue{"2147483648"}},
		{core.VRLO, core.StringValue{"description"}, core.StringValue{strings.Repeat("x", 65)}},
		{core.VRLT, core.StringValue{"long text"}, core.StringValue{"bad\x01text"}},
		{core.VROB, core.RawValue{1, 2}, core.StringValue{"bytes"}},
		{core.VROD, core.Float64Value{1.25}, core.StringValue{"1.25"}},
		{core.VROF, core.Float32Value{1.25}, core.StringValue{"1.25"}},
		{core.VROL, core.Uint32Value{1}, core.StringValue{"1"}},
		{core.VROV, core.Uint64Value{1}, core.StringValue{"1"}},
		{core.VROW, core.RawValue{1, 2}, core.StringValue{"words"}},
		{core.VRPN, core.StringValue{"DOE^JANE=山田^花子"}, core.StringValue{"A^B^C^D^E^F"}},
		{core.VRSH, core.StringValue{"SHORT"}, core.StringValue{strings.Repeat("x", 17)}},
		{core.VRSL, core.Int32Value{-1}, core.StringValue{"-1"}},
		{core.VRSQ, core.SequenceValue{Items: []core.DataSet{{}}}, core.RawValue{1, 2}},
		{core.VRSS, core.Int16Value{-1}, core.StringValue{"-1"}},
		{core.VRSV, core.Int64Value{-1}, core.StringValue{"-1"}},
		{core.VRST, core.StringValue{"short text"}, core.StringValue{"bad\x01text"}},
		{core.VRTM, core.StringValue{"123045.123456"}, core.StringValue{"246000"}},
		{core.VRUC, core.StringValue{"Unicode 山田"}, core.StringValue{"bad\x01text"}},
		{core.VRUI, core.StringValue{"1.2.840.10008.1"}, core.StringValue{"1..2"}},
		{core.VRUL, core.Uint32Value{1}, core.StringValue{"1"}},
		{core.VRUN, core.RawValue{1, 2}, core.StringValue{"opaque"}},
		{core.VRUR, core.StringValue{"https://example.test/dicom"}, core.StringValue{"bad\x01uri"}},
		{core.VRUS, core.Uint16Value{1}, core.StringValue{"1"}},
		{core.VRUT, core.StringValue{"Unlimited 山田"}, core.StringValue{"bad\x01text"}},
		{core.VRUV, core.Uint64Value{1}, core.StringValue{"1"}},
	}
	if len(fixtures) != 34 {
		t.Fatalf("fixture count = %d, want all 34 VRs", len(fixtures))
	}
	for index, fixture := range fixtures {
		t.Run(fixture.vr.String(), func(t *testing.T) {
			tag := core.NewTag(0x0011, uint16(0x1000+index))
			valid, err := validation.ValidateElement(context.Background(), core.Element{
				Header: core.ElementHeader{Tag: tag, VR: fixture.vr}, Value: fixture.valid,
			}, validation.Options{Mode: validation.ModePreserve})
			if err != nil || len(valid.Findings) != 0 {
				t.Fatalf("valid %s fixture: report=%#v err=%v", fixture.vr, valid, err)
			}
			invalid, err := validation.ValidateElement(context.Background(), core.Element{
				Header: core.ElementHeader{Tag: tag, VR: fixture.vr}, Value: fixture.invalid,
			}, validation.Options{Mode: validation.ModePreserve})
			if err != nil {
				t.Fatal(err)
			}
			if len(invalid.Findings) == 0 {
				t.Fatalf("invalid %s fixture produced no finding", fixture.vr)
			}
		})
	}
}

func TestValidateDictionarySupportsEveryGeneratedVMForm(t *testing.T) {
	tests := []struct {
		spec           string
		valid, invalid int
	}{
		{"1", 1, 2}, {"1-2", 2, 3}, {"1-3", 3, 4}, {"1-8", 8, 9},
		{"1-32", 32, 33}, {"1-99", 99, 100}, {"1-n", 7, -1}, {"16", 16, 15},
		{"2", 2, 1}, {"2-2n", 4, 3}, {"2-4", 4, 5}, {"2-n", 5, 1},
		{"3", 3, 2}, {"3-3n", 6, 4}, {"3-n", 4, 2}, {"4", 4, 3},
		{"4-5", 5, 6}, {"6", 6, 5}, {"6-n", 7, 5}, {"9", 9, 8},
	}
	for index, tc := range tests {
		t.Run(tc.spec, func(t *testing.T) {
			tag := core.NewTag(0x0011, uint16(0x2000+index))
			dict := fixedDictionary{entries: map[core.Tag]dictionary.Entry{
				tag: {Tag: tag, VR: core.VRLO, Keyword: "VMFixture", VM: tc.spec},
			}}
			values := func(count int) core.StringValue {
				result := make(core.StringValue, count)
				for i := range result {
					result[i] = "x"
				}
				return result
			}
			valid, err := validation.ValidateElement(context.Background(), core.Element{
				Header: core.ElementHeader{Tag: tag, VR: core.VRLO}, Value: values(tc.valid),
			}, validation.Options{Mode: validation.ModePreserve, Dictionary: dict})
			if err != nil || valid.Count(validation.CodeValueMultiplicity) != 0 {
				t.Fatalf("valid VM %s count %d: %#v, %v", tc.spec, tc.valid, valid, err)
			}
			if tc.invalid < 0 {
				return
			}
			invalid, err := validation.ValidateElement(context.Background(), core.Element{
				Header: core.ElementHeader{Tag: tag, VR: core.VRLO}, Value: values(tc.invalid),
			}, validation.Options{Mode: validation.ModePreserve, Dictionary: dict})
			if err != nil {
				t.Fatal(err)
			}
			if invalid.Count(validation.CodeValueMultiplicity) != 1 {
				t.Fatalf("invalid VM %s count %d: %#v", tc.spec, tc.invalid, invalid)
			}
		})
	}
}

func TestValidateDictionaryIgnoresMalformedVMWithoutPanicking(t *testing.T) {
	for index, spec := range []string{"", "0", "0-0n", "1-0", "1-", "1-2-3", "bad"} {
		t.Run(spec, func(t *testing.T) {
			tag := core.NewTag(0x0011, uint16(0x2F00+index))
			dict := fixedDictionary{entries: map[core.Tag]dictionary.Entry{
				tag: {Tag: tag, VR: core.VRLO, Keyword: "MalformedVMFixture", VM: spec},
			}}
			report, err := validation.ValidateElement(context.Background(), core.Element{
				Header: core.ElementHeader{Tag: tag, VR: core.VRLO}, Value: core.StringValue{"x"},
			}, validation.Options{Mode: validation.ModePreserve, Dictionary: dict})
			if err != nil {
				t.Fatal(err)
			}
			if report.Count(validation.CodeValueMultiplicity) != 0 {
				t.Fatalf("malformed dictionary VM %q produced a value finding: %#v", spec, report)
			}
		})
	}
}

func TestValueMultiplicityTreatsSequenceAndOtherVRPayloadAsOneValue(t *testing.T) {
	sequenceTag := core.NewTag(0x0011, 0x3100)
	otherTag := core.NewTag(0x0011, 0x3101)
	dict := fixedDictionary{entries: map[core.Tag]dictionary.Entry{
		sequenceTag: {Tag: sequenceTag, VR: core.VRSQ, VM: "1"},
		otherTag:    {Tag: otherTag, VR: core.VROW, VM: "1"},
	}}
	dataset := core.DataSet{Elements: []core.Element{
		{Header: core.ElementHeader{Tag: sequenceTag, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{{}, {}, {}}}},
		{Header: core.ElementHeader{Tag: otherTag, VR: core.VROW}, Value: core.RawValue{0, 1, 2, 3, 4, 5}},
	}}
	result, err := validation.ValidateDataSet(context.Background(), dataset, validation.Options{Dictionary: dict})
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Count(validation.CodeValueMultiplicity) != 0 {
		t.Fatalf("sequence/Other VR produced false VM finding: %#v", result.Report.Findings)
	}
}

func TestValidateDataSetAppliesSequenceCompleteReplacement(t *testing.T) {
	chain, err := validation.NewHookChain(validation.HookRegistration{
		Name: "replace-sequence", Points: []validation.HookPoint{validation.HookSequenceComplete},
		Hook: validation.HookFunc(func(_ context.Context, event validation.HookEvent) (validation.HookDecision, error) {
			replacement := *event.Element
			replacement.Value = core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
				textElement(tagPatientID, core.VRLO, "replacement"),
			}}}}
			return validation.HookDecision{Element: &replacement}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	sequence := core.Element{Header: core.ElementHeader{Tag: tagReferencedStudySeq, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{{}}}}
	result, err := validation.ValidateDataSet(context.Background(), core.DataSet{Elements: []core.Element{sequence}}, validation.Options{Hooks: chain})
	if err != nil {
		t.Fatal(err)
	}
	items := result.DataSet.Elements[0].Value.(core.SequenceValue).Items
	if got := items[0].Elements[0].StringValue(); got != "replacement" {
		t.Fatalf("sequence replacement value = %q", got)
	}
}

func TestDataSetRulesRunAtNestedPathsAndRedactMessages(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1110)
	nestedTag := core.NewTag(0x0008, 0x1155)
	rule := validation.DataSetRuleFunc(func(context.Context, validation.DataSetContext) []validation.Finding {
		return []validation.Finding{{Tag: nestedTag, Code: validation.CodePixelMetadata, Message: "secret patient value"}}
	})
	dataset := core.DataSet{Elements: []core.Element{{
		Header: core.ElementHeader{Tag: sequenceTag, VR: core.VRSQ},
		Value:  core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{textElement(nestedTag, core.VRUI, "1.2.3")}}}},
	}}}
	result, err := validation.ValidateDataSet(context.Background(), dataset, validation.Options{
		DataSetRules: []validation.DataSetRuleRegistration{{Name: "pixel-metadata", Rule: rule}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Report.Findings) != 2 {
		// The rule runs for the nested item and the root dataset.
		t.Fatalf("data set rule findings = %#v", result.Report.Findings)
	}
	for _, finding := range result.Report.Findings {
		if finding.Rule != "pixel-metadata" || strings.Contains(finding.Message, "secret") {
			t.Fatalf("unsafe rule finding = %#v", finding)
		}
	}
	if got := result.Report.Findings[0].Path.String(); got != sequenceTag.String()+"[0]/"+nestedTag.String() {
		t.Fatalf("nested rule path = %q", got)
	}
}

func TestDataSetRuleCannotMutateValidatedDataSet(t *testing.T) {
	dataset := core.DataSet{Elements: []core.Element{
		textElement(tagPatientID, core.VRLO, "ORIGINAL"),
	}}
	rule := validation.DataSetRuleFunc(func(_ context.Context, view validation.DataSetContext) []validation.Finding {
		view.DataSet.Elements[0].Value = core.StringValue{"MUTATED"}
		return nil
	})
	result, err := validation.ValidateDataSet(context.Background(), dataset, validation.Options{
		DataSetRules: []validation.DataSetRuleRegistration{{Name: "read-only", Rule: rule}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.DataSet.Elements[0].StringValue(); got != "ORIGINAL" {
		t.Fatalf("rule mutated validation result to %q", got)
	}
	if got := dataset.Elements[0].StringValue(); got != "ORIGINAL" {
		t.Fatalf("rule mutated caller-owned dataset to %q", got)
	}
}

func TestPathStepJSONPreservesZeroItemAndOmitsNoItem(t *testing.T) {
	encoded, err := json.Marshal(validation.Path{
		{Tag: tagReferencedStudySeq, ItemIndex: 0},
		{Tag: tagPatientID, ItemIndex: validation.NoItem},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if !strings.Contains(text, `"item_index":0`) || strings.Contains(text, `"item_index":-1`) {
		t.Fatalf("path JSON = %s", text)
	}
}

func TestValidateTextAllowsDICOMPaddingSpacesAndSingleValueBackslash(t *testing.T) {
	for _, element := range []core.Element{
		textElement(core.NewTag(0x0011, 0x3001), core.VRDS, " 1.25 "),
		textElement(core.NewTag(0x0011, 0x3002), core.VRIS, " -42 "),
		{Header: core.ElementHeader{Tag: core.NewTag(0x0011, 0x3003), VR: core.VRLT}, Value: core.RawValue(`line\separator`)},
	} {
		report, err := validation.ValidateElement(context.Background(), element, validation.Options{})
		if err != nil || len(report.Findings) != 0 {
			t.Fatalf("valid padded/single-value text %s: report=%#v err=%v", element.VR(), report, err)
		}
	}
}

func TestValidateSpecificCharacterRepertoireControls(t *testing.T) {
	tests := []struct {
		name    string
		element core.Element
		valid   bool
	}{
		{name: "raw LO NUL", element: core.NewRawElement(tagPatientID, core.VRLO, []byte{'A', 0, 'B'}), valid: false},
		{name: "typed LO TAB", element: textElement(tagPatientID, core.VRLO, "A\tB"), valid: false},
		{name: "typed LO escape control", element: textElement(tagPatientID, core.VRLO, "A\x1bB"), valid: false},
		{name: "raw LO undeclared escape", element: core.NewRawElement(tagPatientID, core.VRLO, []byte{'A', 0x1b, 'B', ' '}), valid: false},
		{name: "raw LO incomplete escape", element: core.NewRawElement(tagPatientID, core.VRLO, []byte{'A', 0x1b}), valid: false},
		{name: "typed LT line controls", element: textElement(core.NewTag(0x0011, 0x3004), core.VRLT, "A\tB\nC\fD\rE"), valid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report, err := validation.ValidateElement(context.Background(), test.element, validation.Options{})
			if err != nil {
				t.Fatal(err)
			}
			gotValid := report.Count(validation.CodeValueRepertoire) == 0
			if gotValid != test.valid {
				t.Fatalf("repertoire report = %#v, want valid=%v", report, test.valid)
			}
		})
	}
}

func TestValidateRawSpecificCharacterSetEncoding(t *testing.T) {
	tests := []struct {
		name        string
		charset     string
		value       []byte
		wantFinding bool
	}{
		{name: "default rejects high byte", value: []byte{0xff, ' '}, wantFinding: true},
		{name: "UTF-8 rejects malformed sequence", charset: "ISO_IR 192", value: []byte{0xc3, 0x28}, wantFinding: true},
		{name: "UTF-8 accepts valid sequence", charset: "ISO_IR 192", value: []byte{0xc3, 0xa9}, wantFinding: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			elements := []core.Element(nil)
			if test.charset != "" {
				elements = append(elements, textElement(tagSpecificCharacterSet, core.VRCS, test.charset))
			}
			elements = append(elements, core.NewRawElement(tagPatientID, core.VRLO, test.value))
			result, err := validation.ValidateDataSet(context.Background(), core.DataSet{Elements: elements}, validation.Options{})
			if err != nil {
				t.Fatal(err)
			}
			gotFinding := result.Report.Count(validation.CodeValueRepertoire) > 0
			if gotFinding != test.wantFinding {
				t.Fatalf("repertoire report = %#v, want finding=%v", result.Report, test.wantFinding)
			}
		})
	}
}

func TestValidateNestedDataSetInheritsSpecificCharacterSet(t *testing.T) {
	dataset := core.DataSet{Elements: []core.Element{
		textElement(tagSpecificCharacterSet, core.VRCS, "ISO_IR 100"),
		{
			Header: core.ElementHeader{Tag: tagReferencedStudySeq, VR: core.VRSQ},
			Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
				core.NewRawElement(tagPatientID, core.VRLO, []byte{0xe9, ' '}),
			}}}},
		},
	}}
	result, err := validation.ValidateDataSet(context.Background(), dataset, validation.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Count(validation.CodeValueRepertoire) != 0 {
		t.Fatalf("inherited ISO_IR 100 report = %#v", result.Report)
	}
}

func textElement(tag core.Tag, vr core.VR, values ...string) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.StringValue(values)}
}

type fixedDictionary struct {
	entries map[core.Tag]dictionary.Entry
}

func (d fixedDictionary) ByTag(tag core.Tag) (dictionary.Entry, bool) {
	entry, ok := d.entries[tag]
	return entry, ok
}

func (d fixedDictionary) ByKeyword(keyword string) (dictionary.Entry, bool) {
	for _, entry := range d.entries {
		if strings.EqualFold(entry.Keyword, keyword) {
			return entry, true
		}
	}
	return dictionary.Entry{}, false
}

func assertFindingCode(t *testing.T, report validation.Report, code validation.Code) {
	t.Helper()
	if report.Count(code) == 0 {
		t.Fatalf("missing finding code %q in %#v", code, report.Findings)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
