package ups

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
	"github.com/ThalesMMS/dicom-go/validation"
)

const maxUPSStatusRecipients = 1_000_000

var (
	tagCodeValue              = core.NewTag(0x0008, 0x0100)
	tagCodingSchemeDesignator = core.NewTag(0x0008, 0x0102)
	tagCodingSchemeVersion    = core.NewTag(0x0008, 0x0103)
	tagCodeMeaning            = core.NewTag(0x0008, 0x0104)
	tagLongCodeValue          = core.NewTag(0x0008, 0x0119)
	tagURNCodeValue           = core.NewTag(0x0008, 0x0120)
)

type ScheduledStepAttributes struct {
	Priority            string
	ProcedureStepLabel  string
	WorklistLabel       string
	StartDateTime       string
	InputReadinessState string
}

type Code struct {
	Value   string
	Scheme  string
	Meaning string
}

type PerformedProcedureAttributes struct {
	Station       Code
	Workitem      Code
	StartDateTime string
	EndDateTime   string
}

func NewDataSet(elements ...core.Element) *object.Object {
	return object.FromElements(elements, std.Dictionary)
}

func StringElement(tag core.Tag, vr core.VR, values ...string) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.StringValue(append([]string(nil), values...))}
}

func EmptySequence(tag core.Tag) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: tag, VR: core.VRSQ}, Value: core.SequenceValue{}}
}

func BuildScheduledStep(attributes ScheduledStepAttributes) (*object.Object, error) {
	if strings.TrimSpace(attributes.Priority) == "" || strings.TrimSpace(attributes.ProcedureStepLabel) == "" ||
		strings.TrimSpace(attributes.StartDateTime) == "" || strings.TrimSpace(attributes.InputReadinessState) == "" {
		return nil, ErrInvalidDataSet
	}
	return NewDataSet(
		StringElement(TagTransactionUID, core.VRUI, ""),
		StringElement(TagSOPClassUID, core.VRUI, PushSOPClassUID),
		StringElement(TagScheduledProcedureStepPriority, core.VRCS, attributes.Priority),
		StringElement(TagProcedureStepLabel, core.VRLO, attributes.ProcedureStepLabel),
		StringElement(TagWorklistLabel, core.VRLO, attributes.WorklistLabel),
		EmptySequence(TagScheduledProcessingParametersSequence),
		EmptySequence(TagScheduledStationNameCodeSequence),
		EmptySequence(TagScheduledStationClassCodeSequence),
		EmptySequence(TagScheduledStationGeographicLocationCodeSequence),
		EmptySequence(TagScheduledHumanPerformersSequence),
		StringElement(TagScheduledProcedureStepStartDateTime, core.VRDT, attributes.StartDateTime),
		EmptySequence(TagScheduledWorkitemCodeSequence),
		StringElement(TagCommentsOnScheduledProcedureStep, core.VRLT, ""),
		StringElement(TagInputReadinessState, core.VRCS, attributes.InputReadinessState),
		EmptySequence(TagInputInformationSequence),
		StringElement(TagStudyInstanceUID, core.VRUI, ""),
		StringElement(TagPatientName, core.VRPN, ""),
		StringElement(TagPatientID, core.VRLO, ""),
		StringElement(TagIssuerOfPatientID, core.VRLO, ""),
		EmptySequence(TagIssuerOfPatientIDQualifiersSequence),
		EmptySequence(TagOtherPatientIDsSequence),
		StringElement(TagPatientBirthDate, core.VRDA, ""),
		StringElement(TagPatientSex, core.VRCS, ""),
		StringElement(TagAdmissionID, core.VRLO, ""),
		EmptySequence(TagIssuerOfAdmissionIDSequence),
		StringElement(TagAdmittingDiagnosesDescription, core.VRLO, ""),
		EmptySequence(TagAdmittingDiagnosesCodeSequence),
		EmptySequence(TagReferencedRequestSequence),
		StringElement(TagProcedureStepState, core.VRCS, string(StateScheduled)),
		EmptySequence(TagProcedureStepProgressInformationSequence),
		EmptySequence(TagUnifiedProcedureStepPerformedProcedureSequence),
	), nil
}

func BuildPerformedProcedure(attributes PerformedProcedureAttributes) (*object.Object, error) {
	if err := validateCode(attributes.Station); err != nil {
		return nil, err
	}
	if err := validateCode(attributes.Workitem); err != nil {
		return nil, err
	}
	if attributes.StartDateTime == "" || attributes.EndDateTime == "" {
		return nil, ErrInvalidDataSet
	}
	item := core.DataSet{Elements: []core.Element{
		codeSequence(TagPerformedStationNameCodeSequence, attributes.Station),
		StringElement(TagPerformedProcedureStepStartDateTime, core.VRDT, attributes.StartDateTime),
		codeSequence(TagPerformedWorkitemCodeSequence, attributes.Workitem),
		StringElement(TagPerformedProcedureStepEndDateTime, core.VRDT, attributes.EndDateTime),
		EmptySequence(TagOutputInformationSequence),
	}}
	return NewDataSet(core.Element{
		Header: core.ElementHeader{Tag: TagUnifiedProcedureStepPerformedProcedureSequence, VR: core.VRSQ},
		Value:  core.SequenceValue{Items: []core.DataSet{item}},
	}), nil
}

func validateCode(code Code) error {
	if strings.TrimSpace(code.Value) == "" || strings.TrimSpace(code.Scheme) == "" || strings.TrimSpace(code.Meaning) == "" {
		return ErrInvalidDataSet
	}
	return nil
}

func codeSequence(tag core.Tag, code Code) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: core.VRSQ},
		Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			StringElement(tagCodeValue, core.VRSH, code.Value),
			StringElement(tagCodingSchemeDesignator, core.VRSH, code.Scheme),
			StringElement(tagCodeMeaning, core.VRLO, code.Meaning),
		}}}},
	}
}

func normalizeLimits(limits Limits) (Limits, error) {
	if limits.MaxDataSetBytes < 0 || limits.MaxDataSetElements < 0 || limits.MaxDataSetDepth < 0 || limits.MaxCASAttempts < 0 || limits.MaxStatusRecipients < 0 || limits.MaxStatusRecipients > maxUPSStatusRecipients {
		return Limits{}, ErrResourceLimit
	}
	if limits.MaxDataSetBytes == 0 {
		limits.MaxDataSetBytes = 4 << 20
	}
	if limits.MaxDataSetElements == 0 {
		limits.MaxDataSetElements = 16_384
	}
	if limits.MaxDataSetDepth == 0 {
		limits.MaxDataSetDepth = 32
	}
	if limits.MaxCASAttempts == 0 {
		limits.MaxCASAttempts = 64
	}
	if limits.MaxStatusRecipients == 0 {
		limits.MaxStatusRecipients = 10_000
	}
	return limits, nil
}

type cloneBudget struct {
	ctx      context.Context
	bytes    int64
	elements int
	maxBytes int64
	maxElems int
	maxDepth int
}

func cloneDataSet(ctx context.Context, dataSet core.DataSet, limits Limits) (core.DataSet, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	budget := cloneBudget{ctx: ctx, maxBytes: limits.MaxDataSetBytes, maxElems: limits.MaxDataSetElements, maxDepth: limits.MaxDataSetDepth}
	return budget.cloneDataSet(dataSet, 0)
}

func (budget *cloneBudget) cloneDataSet(dataSet core.DataSet, depth int) (core.DataSet, error) {
	if err := budget.ctx.Err(); err != nil {
		return core.DataSet{}, err
	}
	if depth > budget.maxDepth || len(dataSet.Elements) > budget.maxElems-budget.elements {
		return core.DataSet{}, ErrResourceLimit
	}
	result := core.DataSet{ItemOffset: dataSet.ItemOffset, ItemOffsetSet: dataSet.ItemOffsetSet, Elements: make([]core.Element, len(dataSet.Elements))}
	for index, element := range dataSet.Elements {
		budget.elements++
		value, err := budget.cloneValue(element.Value, depth)
		if err != nil {
			return core.DataSet{}, err
		}
		result.Elements[index] = core.Element{Header: element.Header, Value: value}
	}
	return result, nil
}

func (budget *cloneBudget) reserveBytes(count int64) error {
	if count < 0 || count > budget.maxBytes-budget.bytes {
		return ErrResourceLimit
	}
	budget.bytes += count
	return nil
}

func (budget *cloneBudget) cloneValue(value core.Value, depth int) (core.Value, error) {
	if value == nil {
		return nil, ErrInvalidDataSet
	}
	switch typed := value.(type) {
	case core.RawValue:
		if err := budget.reserveBytes(int64(len(typed))); err != nil {
			return nil, err
		}
		return core.RawValue(core.CloneBytes(typed)), nil
	case core.StringValue:
		if len(typed) > budget.maxElems-budget.elements {
			return nil, ErrResourceLimit
		}
		clone := make(core.StringValue, len(typed))
		for index, item := range typed {
			if err := budget.reserveBytes(int64(len(item))); err != nil {
				return nil, err
			}
			clone[index] = strings.Clone(item)
		}
		return clone, nil
	case core.SequenceValue:
		if depth+2 > budget.maxDepth || len(typed.Items) > budget.maxElems-budget.elements {
			return nil, ErrResourceLimit
		}
		items := make([]core.DataSet, len(typed.Items))
		for index, item := range typed.Items {
			budget.elements++
			clone, err := budget.cloneDataSet(item, depth+2)
			if err != nil {
				return nil, err
			}
			items[index] = clone
		}
		return core.SequenceValue{Items: items}, nil
	case core.FragmentSequence:
		return nil, ErrInvalidDataSet
	case core.BulkDataValue:
		return nil, ErrInvalidDataSet
	case core.Uint16Value:
		if err := budget.reserveBytes(int64(len(typed)) * 2); err != nil {
			return nil, err
		}
		return append(core.Uint16Value(nil), typed...), nil
	case core.Int16Value:
		if err := budget.reserveBytes(int64(len(typed)) * 2); err != nil {
			return nil, err
		}
		return append(core.Int16Value(nil), typed...), nil
	case core.Uint32Value:
		if err := budget.reserveBytes(int64(len(typed)) * 4); err != nil {
			return nil, err
		}
		return append(core.Uint32Value(nil), typed...), nil
	case core.Int32Value:
		if err := budget.reserveBytes(int64(len(typed)) * 4); err != nil {
			return nil, err
		}
		return append(core.Int32Value(nil), typed...), nil
	case core.Uint64Value:
		if len(typed) > math.MaxInt/8 {
			return nil, ErrResourceLimit
		}
		if err := budget.reserveBytes(int64(len(typed)) * 8); err != nil {
			return nil, err
		}
		return append(core.Uint64Value(nil), typed...), nil
	case core.Int64Value:
		if len(typed) > math.MaxInt/8 {
			return nil, ErrResourceLimit
		}
		if err := budget.reserveBytes(int64(len(typed)) * 8); err != nil {
			return nil, err
		}
		return append(core.Int64Value(nil), typed...), nil
	case core.Float32Value:
		if err := budget.reserveBytes(int64(len(typed)) * 4); err != nil {
			return nil, err
		}
		return append(core.Float32Value(nil), typed...), nil
	case core.Float64Value:
		if err := budget.reserveBytes(int64(len(typed)) * 8); err != nil {
			return nil, err
		}
		return append(core.Float64Value(nil), typed...), nil
	case core.TagValue:
		if err := budget.reserveBytes(int64(len(typed)) * 4); err != nil {
			return nil, err
		}
		return append(core.TagValue(nil), typed...), nil
	default:
		return nil, ErrInvalidDataSet
	}
}

func validateDataSet(ctx context.Context, dataSet core.DataSet, limits Limits) error {
	objectValue := object.FromDataSet(dataSet, std.Dictionary)
	_, err := objectValue.ValidateDataSet(ctx, validation.Options{
		Mode: validation.ModeStrict, Dictionary: std.Dictionary, MaxFindings: 1, StopFirst: true,
		MaxDepth: limits.MaxDataSetDepth, MaxElements: limits.MaxDataSetElements,
	})
	if err != nil {
		if errors.Is(err, validation.ErrValidationLimit) {
			return ErrResourceLimit
		}
		return fmt.Errorf("%w", ErrInvalidDataSet)
	}
	return nil
}

func validateDataSetEncoding(ctx context.Context, dataSet core.DataSet) error {
	if err := normalizeContext(ctx).Err(); err != nil {
		return err
	}
	if err := object.WriteDataSet(io.Discard, object.FromDataSet(dataSet, std.Dictionary), transfer.ExplicitVRLittleEndian); err != nil {
		return ErrInvalidDataSet
	}
	return normalizeContext(ctx).Err()
}

func dataSetElement(dataSet core.DataSet, tag core.Tag) (core.Element, bool) {
	for index := len(dataSet.Elements) - 1; index >= 0; index-- {
		if dataSet.Elements[index].Tag() == tag {
			return dataSet.Elements[index], true
		}
	}
	return core.Element{}, false
}

func dataSetString(dataSet core.DataSet, tag core.Tag) (string, bool) {
	element, ok := dataSetElement(dataSet, tag)
	if !ok {
		return "", false
	}
	return element.StringValue(), true
}

func putElement(dataSet *core.DataSet, element core.Element) {
	for index := range dataSet.Elements {
		if dataSet.Elements[index].Tag() == element.Tag() {
			dataSet.Elements[index] = element
			return
		}
	}
	dataSet.Elements = append(dataSet.Elements, element)
}

func removeElement(dataSet *core.DataSet, tag core.Tag) {
	for index := range dataSet.Elements {
		if dataSet.Elements[index].Tag() == tag {
			copy(dataSet.Elements[index:], dataSet.Elements[index+1:])
			dataSet.Elements = dataSet.Elements[:len(dataSet.Elements)-1]
			return
		}
	}
}
