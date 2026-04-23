package dicomtest

import (
	"bytes"
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
)

func DiffDataSet(got, want core.DataSet) string {
	if err := diffDataSet("dataset", got, want); err != nil {
		return err.Error()
	}
	return ""
}

func pathTag(path string, tag core.Tag) string {
	return fmt.Sprintf("%s/%s", path, tag)
}

func pathIndex(path string, index int) string {
	return fmt.Sprintf("%s[%d]", path, index)
}

func pathLabel(path, label string) string {
	return fmt.Sprintf("%s[%s]", path, label)
}

func diffBytes(path string, got, want []byte) error {
	if bytes.Equal(got, want) {
		return nil
	}
	if len(got) != len(want) {
		return fmt.Errorf("%s: got %d bytes, want %d bytes", path, len(got), len(want))
	}
	return fmt.Errorf("%s: bytes = % X, want % X", path, got, want)
}

func diffDataSet(path string, got, want core.DataSet) error {
	if len(got.Elements) != len(want.Elements) {
		return fmt.Errorf("%s: element count = %d, want %d", path, len(got.Elements), len(want.Elements))
	}
	for i := range want.Elements {
		if err := diffElement(path, i, got.Elements[i], want.Elements[i]); err != nil {
			return err
		}
	}
	return nil
}

func diffElement(path string, index int, got, want core.Element) error {
	if got.Tag() != want.Tag() {
		return fmt.Errorf("%s[%d]: tag = %s, want %s", path, index, got.Tag(), want.Tag())
	}

	currentPath := pathTag(path, want.Tag())
	if got.VR() != want.VR() {
		return fmt.Errorf("%s: VR = %s, want %s", currentPath, got.VR(), want.VR())
	}
	if want.Header.HasLength() && !got.Header.HasLength() {
		return fmt.Errorf("%s: missing encoded length %s", currentPath, want.Header.Length)
	}
	if want.Header.HasLength() && got.Header.Length != want.Header.Length {
		return fmt.Errorf("%s: encoded length = %s, want %s", currentPath, got.Header.Length, want.Header.Length)
	}
	return diffValue(currentPath, got.Value, want.Value)
}

func diffValue(path string, got, want core.Value) error {
	switch wantValue := want.(type) {
	case nil:
		if got != nil {
			return fmt.Errorf("%s: value = %T, want nil", path, got)
		}
		return nil
	case core.RawValue:
		gotValue, ok := got.(core.RawValue)
		if !ok {
			return fmt.Errorf("%s: value type = %T, want %T", path, got, want)
		}
		if err := diffBytes(path, gotValue.Bytes(), wantValue.Bytes()); err != nil {
			return err
		}
		return nil
	case core.StringValue:
		var gotStrings []string
		switch gotValue := got.(type) {
		case core.StringValue:
			gotStrings = append([]string(nil), gotValue...)
		case core.RawValue:
			gotStrings = gotValue.Strings()
		default:
			return fmt.Errorf("%s: value type = %T, want %T", path, got, want)
		}
		if len(gotStrings) != len(wantValue) {
			return fmt.Errorf("%s: string multiplicity = %d, want %d", path, len(gotStrings), len(wantValue))
		}
		for i := range wantValue {
			if gotStrings[i] != wantValue[i] {
				return fmt.Errorf("%s: string value[%d] = %q, want %q", path, i, gotStrings[i], wantValue[i])
			}
		}
		return nil
	case core.SequenceValue:
		gotValue, ok := got.(core.SequenceValue)
		if !ok {
			return fmt.Errorf("%s: value type = %T, want %T", path, got, want)
		}
		if len(gotValue.Items) != len(wantValue.Items) {
			return fmt.Errorf("%s: item count = %d, want %d", path, len(gotValue.Items), len(wantValue.Items))
		}
		for i := range wantValue.Items {
			itemPath := pathIndex(path, i)
			if err := diffDataSet(itemPath, gotValue.Items[i], wantValue.Items[i]); err != nil {
				return err
			}
		}
		return nil
	case core.FragmentSequence:
		gotValue, ok := got.(core.FragmentSequence)
		if !ok {
			return fmt.Errorf("%s: value type = %T, want %T", path, got, want)
		}
		if err := diffBytes(pathLabel(path, "offset_table"), gotValue.OffsetTable, wantValue.OffsetTable); err != nil {
			return err
		}
		if len(gotValue.Fragments) != len(wantValue.Fragments) {
			return fmt.Errorf("%s: fragment count = %d, want %d", path, len(gotValue.Fragments), len(wantValue.Fragments))
		}
		for i := range wantValue.Fragments {
			if err := diffBytes(pathIndex(path, i), gotValue.Fragments[i], wantValue.Fragments[i]); err != nil {
				return err
			}
		}
		return nil
	case core.BulkDataValue:
		gotValue, ok := got.(core.BulkDataValue)
		if !ok {
			return fmt.Errorf("%s: value type = %T, want %T", path, got, want)
		}
		if gotValue.URI != wantValue.URI {
			return fmt.Errorf("%s: BulkDataURI = %q, want %q", path, gotValue.URI, wantValue.URI)
		}
		return nil
	default:
		return fmt.Errorf("%s: unsupported expected value type %T", path, want)
	}
}
