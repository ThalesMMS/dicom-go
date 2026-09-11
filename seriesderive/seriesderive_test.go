package seriesderive

import (
	"bytes"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
)

func TestCloneToSeriesPreservesSourceAndBuildsCoherentReferences(t *testing.T) {
	src := testFile(t)
	clone, err := CloneToSeries(src, Options{
		SeriesInstanceUID:     "1.2.826.0.1.3680043.10.999.20",
		SOPInstanceUID:        "1.2.826.0.1.3680043.10.999.21",
		SeriesDescription:     "Original - split 1/2",
		DerivationDescription: "Split Series: temporal position 1",
		SeriesNumber:          "701",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer clone.Close()

	assertString(t, src.Dataset, derivedio.TagSeriesInstanceUID, "1.2.3.4")
	assertString(t, src.Dataset, derivedio.TagSOPInstanceUID, "1.2.3.4.5")
	assertString(t, clone.Dataset, derivedio.TagStudyInstanceUID, "1.2.3")
	assertString(t, clone.Dataset, derivedio.TagSeriesInstanceUID, "1.2.826.0.1.3680043.10.999.20")
	assertString(t, clone.Dataset, derivedio.TagSOPInstanceUID, "1.2.826.0.1.3680043.10.999.21")
	assertString(t, clone.Meta, derivedio.TagMediaStorageSOPInstanceUID, "1.2.826.0.1.3680043.10.999.21")
	imageType, _ := clone.Dataset.GetStrings(tagImageType)
	if len(imageType) != 3 || imageType[0] != "DERIVED" || imageType[1] != "SECONDARY" || imageType[2] != "AXIAL" {
		t.Fatalf("ImageType = %#v", imageType)
	}
	sequence, ok := clone.Dataset.GetSequence(tagSourceImageSequence)
	if !ok || len(sequence) != 1 {
		t.Fatalf("SourceImageSequence = len %d, ok %v", len(sequence), ok)
	}
	item := sequence[0]
	assertString(t, item, derivedio.TagRefSOPClassUID, "1.2.840.10008.5.1.4.1.1.2")
	assertString(t, item, derivedio.TagRefSOPInstanceUID, "1.2.3.4.5")
}

func TestCloneToSeriesReloadsWithDerivedIdentity(t *testing.T) {
	clone, err := CloneToSeries(testFile(t), Options{
		SeriesInstanceUID: "1.2.826.0.1.3680043.10.999.30",
		SOPInstanceUID:    "1.2.826.0.1.3680043.10.999.31",
	})
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := object.WriteFile(&encoded, clone); err != nil {
		t.Fatal(err)
	}
	reloaded, err := object.ReadFile(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	assertString(t, reloaded.Dataset, derivedio.TagSeriesInstanceUID, "1.2.826.0.1.3680043.10.999.30")
	assertString(t, reloaded.Dataset, derivedio.TagSOPInstanceUID, "1.2.826.0.1.3680043.10.999.31")
}

func testFile(t *testing.T) *object.File {
	t.Helper()
	dataset := derivedio.Object(
		derivedio.UI(derivedio.TagSOPClassUID, "1.2.840.10008.5.1.4.1.1.2"),
		derivedio.UI(derivedio.TagSOPInstanceUID, "1.2.3.4.5"),
		derivedio.UI(derivedio.TagStudyInstanceUID, "1.2.3"),
		derivedio.UI(derivedio.TagSeriesInstanceUID, "1.2.3.4"),
		derivedio.Strings(tagImageType, core.VRCS, []string{"ORIGINAL", "PRIMARY", "AXIAL"}),
	)
	file, err := derivedio.File("1.2.840.10008.5.1.4.1.1.2", "1.2.3.4.5", dataset)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func assertString(t *testing.T, obj *object.Object, tag core.Tag, want string) {
	t.Helper()
	got, ok := obj.GetString(tag)
	if !ok || got != want {
		t.Fatalf("%v = %q, ok %v; want %q", tag, got, ok, want)
	}
}
