package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const archiveTestClass = "1.2.840.10008.5.1.4.1.1.7"

func syntheticInstance(study, series, uid string) *object.Object {
	return object.FromElements([]core.Element{
		textElement("SOPClassUID", archiveTestClass), textElement("SOPInstanceUID", uid), textElement("StudyInstanceUID", study), textElement("SeriesInstanceUID", series),
		textElement("PatientID", "SYNTHETIC"), textElement("PatientName", "SYNTHETIC^ONLY"), textElement("StudyDate", "20260907"), textElement("Modality", "OT"),
		{Header: core.ElementHeader{Tag: core.NewTag(0x0028, 0x0010), VR: core.VRUS}, Value: core.Uint16Value{2}},
		{Header: core.ElementHeader{Tag: core.NewTag(0x0028, 0x0011), VR: core.VRUS}, Value: core.Uint16Value{3}},
		{Header: core.ElementHeader{Tag: core.NewTag(0x0028, 0x0002), VR: core.VRUS}, Value: core.Uint16Value{1}},
		textElement("PhotometricInterpretation", "MONOCHROME2"),
		{Header: core.ElementHeader{Tag: core.NewTag(0x0028, 0x0100), VR: core.VRUS}, Value: core.Uint16Value{8}},
		{Header: core.ElementHeader{Tag: core.NewTag(0x0028, 0x0101), VR: core.VRUS}, Value: core.Uint16Value{8}},
		{Header: core.ElementHeader{Tag: core.NewTag(0x0028, 0x0102), VR: core.VRUS}, Value: core.Uint16Value{7}},
		{Header: core.ElementHeader{Tag: core.NewTag(0x0028, 0x0103), VR: core.VRUS}, Value: core.Uint16Value{0}},
		core.NewRawElement(core.NewTag(0x7fe0, 0x0010), core.VROB, []byte{0, 1, 2, 127, 128, 255}),
	}, std.Dictionary)
}
func storeRequest(ds *object.Object) dimse.CStoreRequestContext {
	uid, _ := ds.GetUID(core.NewTag(0x0008, 0x0018))
	return dimse.CStoreRequestContext{Request: dimse.CStoreRequest{AffectedSOPClassUID: archiveTestClass, AffectedSOPInstanceUID: uid}, PresentationContext: ul.AcceptedContext{AbstractSyntaxUID: archiveTestClass, TransferSyntaxUID: transfer.ExplicitVRLittleEndian.UID}, DataSet: ds, DataSetSyntax: transfer.ExplicitVRLittleEndian}
}
func TestArchiveStoreQueryRetrieveRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	a, err := openArchive(ctx, dir, defaultArchiveLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	for _, item := range []struct{ study, series, uid string }{{"1.2.1", "1.2.1.1", "1.2.1.1.1"}, {"1.2.1", "1.2.1.1", "1.2.1.1.2"}, {"1.2.2", "1.2.2.1", "1.2.2.1.1"}} {
		status, err := a.Store(ctx, storeRequest(syntheticInstance(item.study, item.series, item.uid)))
		if err != nil || status != 0 {
			t.Fatalf("store: %04X %v", status, err)
		}
	}
	for pass := 0; pass < 2; pass++ {
		query := object.FromElements([]core.Element{textElement("QueryRetrieveLevel", "STUDY"), textElement("StudyInstanceUID", ""), textElement("PatientName", "SYNTH*")}, std.Dictionary)
		matches, err := a.Find(ctx, dimse.CFindRequestContext{Identifier: query, QueryRetrieveLevel: "STUDY"})
		if err != nil || len(matches) != 2 {
			t.Fatalf("find: %d %v", len(matches), err)
		}
		query.Put(textElement("StudyInstanceUID", "1.2.1"))
		query.Remove(core.NewTag(0x0010, 0x0010))
		operations, err := a.Get(ctx, dimse.CGetRequestContext{Identifier: query, QueryRetrieveLevel: "STUDY"})
		if err != nil || len(operations) != 2 {
			t.Fatalf("get: %d %v", len(operations), err)
		}
		for _, op := range operations {
			ds, err := op.LoadDataSet(ctx)
			if err != nil {
				t.Fatal(err)
			}
			elem, ok := ds.Get(core.NewTag(0x7fe0, 0x0010))
			if !ok || elem.Value == nil {
				t.Fatal("retrieval lost pixel payload")
			}
		}
		if pass == 0 {
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			a, err = openArchive(ctx, dir, defaultArchiveLimits())
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}
func TestArchiveExclusiveOwnerAndIncompleteRestart(t *testing.T) {
	dir := t.TempDir()
	a, err := openArchive(context.Background(), dir, defaultArchiveLimits())
	if err != nil {
		t.Fatal(err)
	}
	if second, err := openArchive(context.Background(), dir, defaultArchiveLimits()); err == nil {
		_ = second.Close()
		t.Fatal("second owner acquired archive")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".netstore-incomplete.partial"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if reopened, err := openArchive(context.Background(), dir, defaultArchiveLimits()); err == nil {
		_ = reopened.Close()
		t.Fatal("incomplete file accepted at restart")
	}
}
