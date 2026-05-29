package parser

import (
	"bytes"
	"context"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/transfer"
	"github.com/ThalesMMS/dicom-go/validation"
)

func BenchmarkReaderValidationLifecycle(b *testing.B) {
	encoded := encodeValidationBenchmarkDataSet(b)
	b.Run("default-no-validation", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			reader := NewReader(bytes.NewReader(encoded), transfer.ExplicitVRLittleEndian, ReaderOptions{})
			if _, err := reader.ReadDataSet(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("opt-in-no-hooks", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			reader, err := NewReaderWithValidation(context.Background(), bytes.NewReader(encoded), transfer.ExplicitVRLittleEndian, ReaderOptions{}, validation.Options{})
			if err != nil {
				b.Fatal(err)
			}
			if _, err := reader.ReadDataSet(); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func encodeValidationBenchmarkDataSet(b *testing.B) []byte {
	b.Helper()
	var output bytes.Buffer
	writer := NewWriter(&output, transfer.ExplicitVRLittleEndian)
	for index := 0; index < 32; index++ {
		tag := core.NewTag(0x0011, uint16(0x1000+index))
		if err := writer.WriteElement(core.NewRawElement(tag, core.VRLO, []byte("benchmark"))); err != nil {
			b.Fatal(err)
		}
	}
	return output.Bytes()
}
