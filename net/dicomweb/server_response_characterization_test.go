package dicomweb

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestEncodeResponseDatasetRetainsVRSpecificFailClosedValidation(t *testing.T) {
	server := &Server{limits: DefaultServerLimits()}
	for _, test := range []struct {
		name    string
		dataset Dataset
	}{
		{name: "UI-number", dataset: Dataset{"00080018": {VR: "UI", Value: []any{123}}}},
		{name: "CS-number", dataset: Dataset{"00080052": {VR: "CS", Value: []any{1}}}},
		{name: "LO-person-name-object", dataset: Dataset{"00100020": {VR: "LO", Value: []any{map[string]any{"Alphabetic": "wrong VR"}}}}},
		{name: "PN-string", dataset: Dataset{"00100010": {VR: "PN", Value: []any{"Person^Name"}}}},
		{name: "DS-invalid-syntax", dataset: Dataset{"00181020": {VR: "DS", Value: []any{"not-a-number"}}}},
		{name: "DS-overlength", dataset: Dataset{"00181020": {VR: "DS", Value: []any{"12345678901234567"}}}},
		{name: "IS-fractional", dataset: Dataset{"00201208": {VR: "IS", Value: []any{"1.5"}}}},
		{name: "IS-outside-int32", dataset: Dataset{"00201208": {VR: "IS", Value: []any{"2147483648"}}}},
		{name: "AT-invalid-tag", dataset: Dataset{"00209165": {VR: "AT", Value: []any{"NOTATAG!"}}}},
		{name: "US-negative", dataset: Dataset{"00280010": {VR: "US", Value: []any{-1}}}},
		{name: "US-overflow", dataset: Dataset{"00280010": {VR: "US", Value: []any{65536}}}},
		{name: "US-fractional", dataset: Dataset{"00280010": {VR: "US", Value: []any{1.5}}}},
		{name: "SS-overflow", dataset: Dataset{"00280106": {VR: "SS", Value: []any{32768}}}},
		{name: "UL-negative", dataset: Dataset{"00280008": {VR: "UL", Value: []any{-1}}}},
		{name: "UL-overflow", dataset: Dataset{"00280008": {VR: "UL", Value: []any{4294967296}}}},
		{name: "UN-Value", dataset: Dataset{"00110010": {VR: "UN", Value: []any{"must use binary"}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := server.validateResponseDataset(context.Background(), test.dataset); err != nil {
				t.Fatalf("validateResponseDataset() error = %v, want structural validation to pass", err)
			}
			if _, err := json.Marshal(test.dataset); err != nil {
				t.Fatalf("json.Marshal() error = %v, want marshaling to pass", err)
			}

			data, err := server.encodeResponseDataset(context.Background(), test.dataset)
			if !errors.Is(err, ErrBackend) {
				t.Fatalf("encodeResponseDataset data=%q error=%v, want ErrBackend", data, err)
			}
		})
	}
}
