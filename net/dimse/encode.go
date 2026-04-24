package dimse

import (
	"bytes"
	"fmt"
	"math"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func EncodeCommandSet(elements []core.Element) ([]byte, error) {
	withoutGroupLength := make([]core.Element, 0, len(elements))
	for _, elem := range elements {
		if elem.Tag() == CommandGroupLength {
			continue
		}
		withoutGroupLength = append(withoutGroupLength, elem)
	}

	obj := object.FromElements(withoutGroupLength, std.Dictionary)
	var command bytes.Buffer
	if err := object.WriteDataSet(&command, obj, transfer.ImplicitVRLittleEndian); err != nil {
		return nil, fmt.Errorf("dicom dimse: encode command set: %w", err)
	}
	if command.Len() > math.MaxUint32 {
		return nil, fmt.Errorf("dicom dimse: command group length %d exceeds uint32", command.Len())
	}

	withGroupLength := append([]core.Element{
		newULCommandElement(CommandGroupLength, uint32(command.Len())),
	}, withoutGroupLength...)
	obj = object.FromElements(withGroupLength, std.Dictionary)

	var out bytes.Buffer
	if err := object.WriteDataSet(&out, obj, transfer.ImplicitVRLittleEndian); err != nil {
		return nil, fmt.Errorf("dicom dimse: encode command set with group length: %w", err)
	}
	return out.Bytes(), nil
}

func DecodeCommandSet(data []byte) (*object.Object, error) {
	obj, err := object.ReadDataSet(bytes.NewReader(data), transfer.ImplicitVRLittleEndian)
	if err != nil {
		return nil, fmt.Errorf("dicom dimse: decode command set: %w", err)
	}
	return obj, nil
}
