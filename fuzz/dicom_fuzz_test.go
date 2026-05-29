package fuzz

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"testing"

	dicom "github.com/ThalesMMS/dicom-go"
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dicomjson"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	fuzzMaxElementBytes  = 64 << 10
	fuzzMaxTotalBytes    = 256 << 10
	fuzzMaxSequenceDepth = 32
	fuzzMaxElements      = 4096
	fuzzMaxFragments     = 1024
	fuzzMaxPDULength     = ul.DefaultMaxPDU
)

func FuzzDICOMParse(f *testing.F) {
	for _, seed := range dicomFuzzSeeds() {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = dicom.ReadFileWithOptions(bytes.NewReader(data), fuzzReadFileOptions())
		_, _ = object.ReadFileWithOptions(bytes.NewReader(data), fuzzReadFileOptions())
		_, _, _ = object.ReadFileWithTransferSyntaxRecovery(
			bytes.NewReader(data),
			fuzzReadFileOptions(),
			fuzzTransferSyntaxRecoveryOptions(),
		)
		_, _, _ = object.ReadDataSetWithTransferSyntaxRecovery(
			bytes.NewReader(data),
			fuzzReadFileOptions(),
			fuzzTransferSyntaxRecoveryOptions(),
		)
		for _, syntax := range fuzzTransferSyntaxes() {
			_, _ = object.ReadDataSetWithOptions(bytes.NewReader(data), syntax, fuzzReadFileOptions())
			_, _ = parser.NewReader(bytes.NewReader(data), syntax, fuzzReaderOptions()).ReadDataSet()
		}
	})
}

func FuzzDICOMJSON(f *testing.F) {
	for _, seed := range dicomJSONFuzzSeeds() {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		obj, err := dicomjson.Unmarshal(data, std.Dictionary)
		if err != nil {
			return
		}
		_, _ = dicomjson.MarshalCompact(obj)
		_, _ = dicomjson.MarshalPretty(obj)
	})
}

func FuzzULPDU(f *testing.F) {
	for _, seed := range ulPDUFuzzSeeds() {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ul.ReadPDU(bytes.NewReader(data), fuzzMaxPDULength)
		_, _ = dimse.DecodeCommandSet(data)
	})
}

func dicomFuzzSeeds() [][]byte {
	seeds := [][]byte{
		dicomtest.MinimalFile(),
		mustPart10(transfer.ImplicitVRLittleEndian, dicomtest.MinimalDataset()...),
		mustPart10(transfer.ExplicitVRBigEndian, dicomtest.MinimalDataset()...),
		buildDeflatedPart10Seed(dicomtest.MinimalDataset()...),
		dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...),
		dicomtest.EncodeElements(transfer.ImplicitVRLittleEndian, dicomtest.MinimalDataset()...),
		dicomtest.EncodeElements(transfer.ExplicitVRBigEndian, dicomtest.MinimalDataset()...),
		dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.BenchmarkSequenceDataSet().Elements...),
		{0x09, 0x09, 0x10, 0x10, 'L', 'O', 0x00, 0x00},
		mustPart10(transfer.ExplicitVRLittleEndian, dicomtest.BenchmarkSequenceDataSet().Elements...),
	}
	encapsulated := encapsulatedPixelDataSeed()
	seeds = append(seeds,
		dicomtest.EncodeElements(transfer.EncapsulatedUncompressedExplicitVRLittleEndian, encapsulated...),
		mustPart10(transfer.EncapsulatedUncompressedExplicitVRLittleEndian, encapsulated...),
	)
	seeds = append(seeds, malformedParserSeeds()...)
	return seeds
}

func encapsulatedPixelDataSeed() []core.Element {
	pixel := dicomtest.NewFragmentSequenceElement(
		core.TagPixelData,
		[]byte{0x00, 0x00, 0x00, 0x00},
		[]byte{0x01, 0x02, 0x03},
		[]byte{0x04, 0x05},
	)
	return append(dicomtest.MinimalDataset(), pixel)
}

func mustPart10(syntax transfer.Syntax, elements ...core.Element) []byte {
	data, err := dicomtest.Part10File(syntax, elements...)
	if err != nil {
		panic(err)
	}
	return data
}

func buildDeflatedPart10Seed(elements ...core.Element) []byte {
	var buf bytes.Buffer
	buf.Write(make([]byte, 128))
	buf.WriteString("DICM")
	buf.Write(dicomtest.NewFileMetaBuilder().WithTransferSyntax(transfer.DeflatedExplicitVRLittleEndian.UID).Encode())
	buf.Write(deflateSeed(dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, elements...)))
	return buf.Bytes()
}

func malformedParserSeeds() [][]byte {
	explicitLongLength := []byte{0x10, 0x00, 0x10, 0x00, 'P', 'N', 0xff, 0xff}
	implicitHugeLength := []byte{0x10, 0x00, 0x20, 0x00, 0xff, 0xff, 0xff, 0x7f}
	undefinedSequence := append(
		dicomtest.SequenceHeaderBytes(transfer.ExplicitVRLittleEndian, core.NewTag(0x0008, 0x1111), 0xffffffff),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0xffffffff)...,
	)
	fragmentMissingBasicOffsetTable := append(
		dicomtest.ExplicitLongHeaderBytes(binary.LittleEndian, core.TagPixelData, core.VROB, 0xffffffff),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0)...,
	)
	return [][]byte{
		explicitLongLength,
		implicitHugeLength,
		undefinedSequence,
		fragmentMissingBasicOffsetTable,
	}
}

func dicomJSONFuzzSeeds() [][]byte {
	valid, err := dicomjson.MarshalCompact(object.FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary))
	if err != nil {
		panic(err)
	}
	sequence, err := dicomjson.MarshalCompact(object.FromDataSet(dicomtest.BenchmarkSequenceDataSet(), std.Dictionary))
	if err != nil {
		panic(err)
	}
	return [][]byte{
		valid,
		sequence,
		[]byte(`{}`),
		[]byte(`{"00100010":{"vr":"PN","Value":[{"Alphabetic":"Doe^John"}]}}`),
		[]byte(`{"00081111":{"vr":"SQ","Value":[{"00100010":{"vr":"PN","Value":[{"Alphabetic":"Nested"}]}}]}}`),
		[]byte(`{"7FE00010":{"vr":"OB","InlineBinary":"AQIDBA=="}}`),
		[]byte(`{"7FE00010":{"vr":"OB","InlineBinary":"not-base64"}}`),
		[]byte(`{"00100010":{"vr":"PN","Value":[{"Alphabetic":1}]}}`),
		[]byte(`{"00100010":{"vr":"PN","Value":{}}}`),
		[]byte(`{"FFFFFFFF":{"vr":"LO","Value":["bad tag"]}}`),
	}
}

func ulPDUFuzzSeeds() [][]byte {
	command := mustCommandSetSeed()
	return [][]byte{
		nil,
		{byte(ul.PDUDataTF)},
		rawPDUSeed(ul.PDUReleaseRQ, nil),
		rawPDUSeed(ul.PDUReleaseRP, nil),
		rawPDUSeed(ul.PDUAbort, []byte{0x00, 0x00, ul.AbortSourceServiceUser, ul.AbortReasonNotSpecified}),
		rawPDUSeed(ul.PDUAbort, []byte{0x00, 0x00, ul.AbortSourceReserved, 0x00}),
		rawPDUSeed(ul.PDUDataTF, []byte{0x00, 0x00, 0x00, 0x10, 0x01, 0x03}),
		pduTooLargeSeed(),
		mustPDUSeed(ul.PDataTF{Values: []ul.PDataValue{{PresentationContextID: 1, IsCommand: true, IsLast: true, Data: command}}}),
		mustPDUSeed(ul.UnknownPDU{Type: 0x88, Data: []byte{0xde, 0xad, 0xbe, 0xef}}),
		command,
		[]byte{0x00, 0x00, 0x00, 0x00, 0xff, 0xff},
	}
}

func mustCommandSetSeed() []byte {
	data, err := dimse.EncodeCommandSet([]core.Element{
		dicomtest.NewUIElement(dimse.AffectedSOPClassUID, dimse.VerificationSOPClassUID),
		dicomtest.Uint16Element(dimse.CommandField, core.VRUS, binary.LittleEndian, dimse.CEchoRQ),
		dicomtest.Uint16Element(dimse.MessageID, core.VRUS, binary.LittleEndian, 1),
		dicomtest.Uint16Element(dimse.CommandDataSetType, core.VRUS, binary.LittleEndian, dimse.NoDataSet),
	})
	if err != nil {
		panic(err)
	}
	return data
}

func mustPDUSeed(pdu ul.PDU) []byte {
	var buf bytes.Buffer
	if err := ul.WritePDU(&buf, pdu); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func rawPDUSeed(pduType ul.PDUType, body []byte) []byte {
	var header [ul.PDUHeaderSize]byte
	header[0] = byte(pduType)
	binary.BigEndian.PutUint32(header[2:], uint32(len(body)))
	return append(header[:], body...)
}

func pduTooLargeSeed() []byte {
	var header [ul.PDUHeaderSize]byte
	header[0] = byte(ul.PDUDataTF)
	binary.BigEndian.PutUint32(header[2:], fuzzMaxPDULength+1)
	return header[:]
}

func deflateSeed(data []byte) []byte {
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		panic(err)
	}
	if _, err := w.Write(data); err != nil {
		panic(err)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func fuzzReadFileOptions() object.ReadFileOptions {
	return object.ReadFileOptions{
		Dictionary:                         std.Dictionary,
		MaxElementBytes:                    fuzzMaxElementBytes,
		MaxTotalBytes:                      fuzzMaxTotalBytes,
		MaxSequenceDepth:                   fuzzMaxSequenceDepth,
		MaxElements:                        fuzzMaxElements,
		MaxFragments:                       fuzzMaxFragments,
		OddLengthPolicy:                    parser.AcceptOddLength,
		StrictReservedBytes:                true,
		AllowMissingMetaElementGroupLength: true,
	}
}

func fuzzReaderOptions() parser.ReaderOptions {
	return parser.ReaderOptions{
		Dictionary:          std.Dictionary,
		MaxElementBytes:     fuzzMaxElementBytes,
		MaxTotalBytes:       fuzzMaxTotalBytes,
		MaxSequenceDepth:    fuzzMaxSequenceDepth,
		MaxElements:         fuzzMaxElements,
		MaxFragments:        fuzzMaxFragments,
		OddLengthPolicy:     parser.AcceptOddLength,
		StrictReservedBytes: true,
	}
}

func fuzzTransferSyntaxRecoveryOptions() object.TransferSyntaxRecoveryOptions {
	return object.TransferSyntaxRecoveryOptions{
		AllowMissingPreamble:          true,
		AllowMissingFileMeta:          true,
		AllowMissingTransferSyntaxUID: true,
		AllowUnknownTransferSyntaxUID: true,
		AllowDeclaredMismatch:         true,
		MaxNonSeekableBytes:           fuzzMaxTotalBytes,
		Probe: parser.TransferSyntaxProbeOptions{
			MaxProbeBytes:     fuzzMaxElementBytes,
			MaxCandidateBytes: fuzzMaxElementBytes,
			MaxElements:       256,
			MaxSequenceDepth:  16,
			MaxFragments:      64,
		},
	}
}

func fuzzTransferSyntaxes() []transfer.Syntax {
	return []transfer.Syntax{
		transfer.ImplicitVRLittleEndian,
		transfer.ExplicitVRLittleEndian,
		transfer.ExplicitVRBigEndian,
		transfer.DeflatedExplicitVRLittleEndian,
		transfer.EncapsulatedUncompressedExplicitVRLittleEndian,
	}
}
