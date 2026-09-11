package object

import (
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var benchmarkClonedFile *File

func BenchmarkCloneFilePixelData64MiB(b *testing.B) {
	elements := dicomtest.MinimalDataset()
	elements = append(elements, core.NewRawElement(core.TagPixelData, core.VROB, make([]byte, 64<<20)))
	src := &File{
		Dataset:        FromDataSet(core.DataSet{Elements: elements}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}
	if err := src.RebuildFileMeta(); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var err error
		benchmarkClonedFile, err = CloneFile(src)
		if err != nil {
			b.Fatal(err)
		}
	}
}
