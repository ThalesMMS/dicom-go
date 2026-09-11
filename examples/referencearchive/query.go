package main

import (
	"context"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/index"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/qrmatch"
)

var tagLevel = core.NewTag(0x0008, 0x0052)

func textElement(keyword, value string) core.Element {
	e, ok := std.Dictionary.ByKeyword(keyword)
	if !ok {
		panic("reference archive: unknown internal keyword")
	}
	return core.Element{Header: core.ElementHeader{Tag: e.Tag, VR: e.VR}, Value: core.StringValue{value}}
}
func recordObject(r index.Record) *object.Object {
	return object.FromElements([]core.Element{
		textElement("SpecificCharacterSet", "ISO_IR 192"),
		textElement("PatientID", r.Patient.ID), textElement("PatientName", r.Patient.Name),
		textElement("StudyInstanceUID", r.Study.InstanceUID), textElement("StudyDate", r.Study.Date), textElement("AccessionNumber", r.Study.AccessionNumber), textElement("StudyDescription", r.Study.Description),
		textElement("SeriesInstanceUID", r.Series.InstanceUID), textElement("Modality", r.Series.Modality), textElement("SeriesNumber", r.Series.Number), textElement("SeriesDescription", r.Series.Description),
		textElement("SOPInstanceUID", r.Instance.SOPInstanceUID), textElement("SOPClassUID", r.Instance.SOPClassUID), textElement("InstanceNumber", r.Instance.Number),
	}, std.Dictionary)
}

func queryKeys(level string) (map[core.Tag]bool, []core.Tag, error) {
	required, err := dimse.QueryRetrieveRequiredKeys(dimse.QueryRetrieveModelStudyRoot, level)
	if err != nil {
		return nil, nil, errQuery
	}
	optional, err := dimse.QueryRetrieveOptionalKeys(dimse.QueryRetrieveModelStudyRoot, level)
	if err != nil {
		return nil, nil, errQuery
	}
	allowed := map[core.Tag]bool{}
	var ids []core.Tag
	for _, keyword := range required {
		e, ok := std.Dictionary.ByKeyword(keyword)
		if !ok {
			return nil, nil, errQuery
		}
		allowed[e.Tag] = true
		ids = append(ids, e.Tag)
	}
	for _, keyword := range optional {
		e, ok := std.Dictionary.ByKeyword(keyword)
		if !ok {
			return nil, nil, errQuery
		}
		allowed[e.Tag] = true
	}
	return allowed, ids, nil
}

func (a *archive) selectInstances(ctx context.Context, level string, query *object.Object, retrieve bool) ([]storedInstance, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if query == nil {
		return nil, errQuery
	}
	allowed, ids, err := queryKeys(level)
	if err != nil {
		return nil, err
	}
	if retrieve {
		allowed = map[core.Tag]bool{}
		for _, tag := range ids {
			allowed[tag] = true
		}
	}
	var keys []core.Element
	for _, elem := range query.Elements() {
		if elem.Tag() == tagLevel || elem.Tag() == core.NewTag(0x0008, 0x0005) {
			continue
		}
		if !allowed[elem.Tag()] {
			return nil, errQuery
		}
		keys = append(keys, elem)
	}
	for position, tag := range ids {
		if retrieve || position < len(ids)-1 {
			values, ok := query.GetUIDs(tag)
			if !ok || len(values) == 0 || (position < len(ids)-1 && len(values) != 1) {
				return nil, errQuery
			}
			for _, uid := range values {
				if !core.IsValidUID(uid) {
					return nil, errQuery
				}
			}
		}
	}
	charset, err := query.CharacterSet()
	if err != nil {
		return nil, errQuery
	}
	filter := object.FromElementsWithTextOptions(keys, std.Dictionary, object.TextOptions{FallbackCharacterSet: charset})
	items, err := a.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	selected := make([]storedInstance, 0)
	for _, item := range items {
		result, err := qrmatch.Match(filter, recordObject(item.record), qrmatch.Options{Context: ctx, Limits: qrmatch.DefaultLimits()})
		if err != nil {
			return nil, errQuery
		}
		if result.Match {
			selected = append(selected, item)
			if len(selected) > a.limits.results {
				return nil, errQuota
			}
		}
	}
	return selected, nil
}

func (a *archive) Find(ctx context.Context, request dimse.CFindRequestContext) ([]*object.Object, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	items, err := a.selectInstances(ctx, request.QueryRetrieveLevel, request.Identifier, false)
	if err != nil {
		return nil, dimse.NewCFindSCPError(0xC000, "reference archive query failed", err)
	}
	_, ids, err := queryKeys(request.QueryRetrieveLevel)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	responses := make([]*object.Object, 0)
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		candidate := recordObject(item.record)
		identity, _ := candidate.GetUID(ids[len(ids)-1])
		if seen[identity] {
			continue
		}
		seen[identity] = true
		response := object.FromElements([]core.Element{textElement("QueryRetrieveLevel", request.QueryRetrieveLevel), textElement("SpecificCharacterSet", "ISO_IR 192")}, std.Dictionary)
		for _, tag := range ids {
			if elem, ok := candidate.Get(tag); ok {
				response.Put(elem)
			}
		}
		for _, requested := range request.Identifier.Elements() {
			if elem, ok := candidate.Get(requested.Tag()); ok {
				response.Put(elem)
			}
		}
		responses = append(responses, response)
	}
	return responses, nil
}

func (a *archive) Get(ctx context.Context, request dimse.CGetRequestContext) ([]dimse.CGetSubOperation, error) {
	items, err := a.selectInstances(ctx, request.QueryRetrieveLevel, request.Identifier, true)
	if err != nil {
		return nil, dimse.NewCGetSCPError(0xC000, "reference archive retrieve failed", err)
	}
	operations := make([]dimse.CGetSubOperation, 0, len(items))
	for _, item := range items {
		item := item
		operations = append(operations, dimse.CGetSubOperation{AffectedSOPClassUID: item.record.Instance.SOPClassUID, AffectedSOPInstanceUID: item.record.Instance.SOPInstanceUID, TransferSyntaxUIDs: []string{item.record.FileMeta.TransferSyntaxUID}, LoadDataSet: func(ctx context.Context) (*object.Object, error) { return a.load(ctx, item) }})
	}
	return operations, nil
}
