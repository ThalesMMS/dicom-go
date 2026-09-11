package dicomweb

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/ThalesMMS/dicom-go/dicomjson"
)

var benchmarkQIDOResponseBytes []byte

func BenchmarkQIDOResponseDatasetEncoding(b *testing.B) {
	server := &Server{limits: DefaultServerLimits()}
	for _, resultCount := range []int{10, 1_000} {
		datasets := benchmarkQIDODatasets(resultCount)
		b.Run(fmt.Sprintf("results-%d/full-revalidation", resultCount), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			b.ReportMetric(float64(resultCount), "results/op")
			for iteration := 0; iteration < b.N; iteration++ {
				for _, dataset := range datasets {
					data, err := server.encodeResponseDataset(context.Background(), dataset)
					if err != nil {
						b.Fatal(err)
					}
					benchmarkQIDOResponseBytes = data
				}
			}
		})
		// This diagnostic deliberately omits VR-aware validation. It quantifies
		// the upper bound of the removable cost, but is not a safe candidate for
		// production because malformed DICOM JSON values would pass.
		b.Run(fmt.Sprintf("results-%d/unsafe-without-vr-revalidation", resultCount), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			b.ReportMetric(float64(resultCount), "results/op")
			for iteration := 0; iteration < b.N; iteration++ {
				for _, dataset := range datasets {
					if err := server.validateResponseDataset(context.Background(), dataset); err != nil {
						b.Fatal(err)
					}
					data, err := json.Marshal(dataset)
					if err != nil {
						b.Fatal(err)
					}
					if int64(len(data)) > server.limits.MaxResponseBytes {
						b.Fatal("fixture exceeded response limit")
					}
					benchmarkQIDOResponseBytes = data
				}
			}
		})
	}
}

func benchmarkQIDODatasets(count int) []Dataset {
	datasets := make([]Dataset, count)
	for index := range datasets {
		datasets[index] = Dataset{
			"00080005": {VR: "CS", Value: []any{"ISO_IR 192"}},
			"00080020": {VR: "DA", Value: []any{"20260830"}},
			"00080030": {VR: "TM", Value: []any{"134500"}},
			"00080050": {VR: "SH", Value: []any{fmt.Sprintf("ACC-%06d", index)}},
			"00080052": {VR: "CS", Value: []any{"STUDY"}},
			"00080054": {VR: "AE", Value: []any{"TWIN_PACS"}},
			"00080056": {VR: "CS", Value: []any{"ONLINE"}},
			"00080061": {VR: "CS", Value: []any{"CT", "MR"}},
			"00081190": {VR: "UR", Value: []any{fmt.Sprintf("/dicom-web/studies/1.2.826.0.1.3680043.9.7433.861.%d", index+1)}},
			"00100010": {VR: "PN", Value: []any{dicomjson.PersonNameComponents{Alphabetic: "Example^Patient"}}},
			"00100020": {VR: "LO", Value: []any{fmt.Sprintf("PAT-%06d", index)}},
			"00100030": {VR: "DA", Value: []any{"19700101"}},
			"00100040": {VR: "CS", Value: []any{"O"}},
			"0020000D": {VR: "UI", Value: []any{fmt.Sprintf("1.2.826.0.1.3680043.9.7433.861.%d", index+1)}},
			"00200010": {VR: "SH", Value: []any{fmt.Sprintf("STUDY-%06d", index)}},
			"00201206": {VR: "IS", Value: []any{"2"}},
			"00201208": {VR: "IS", Value: []any{"240"}},
		}
	}
	return datasets
}
