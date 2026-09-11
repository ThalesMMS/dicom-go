package object

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestUpstreamShortRawDataSetRequiresExplicitSyntax(t *testing.T) {
	for _, syntax := range []transfer.Syntax{transfer.ImplicitVRLittleEndian, transfer.ExplicitVRLittleEndian, transfer.ExplicitVRBigEndian} {
		element := core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x0028, 0x0010), VR: core.VRUS}, Value: core.Uint16Value{7}}
		raw := make([]byte, 2)
		syntax.ByteOrder.PutUint16(raw, 7)
		data := dicomtest.EncodeElements(syntax, core.NewRawElement(element.Tag(), core.VRUS, raw))
		if len(data) >= 100 {
			t.Fatal("regression no longer exercises a short dataset")
		}
		obj, err := ReadDataSetWithOptions(bytes.NewReader(data), syntax, ReadFileOptions{Dictionary: std.Dictionary, MaxElementBytes: 16, MaxTotalBytes: 64})
		if err != nil {
			t.Fatal(err)
		}
		got, err := dicomtest.SemanticDataSet(obj.ToDataSet(), syntax.ByteOrder)
		if err != nil {
			t.Fatal(err)
		}
		want, err := dicomtest.SemanticDataSet(core.DataSet{Elements: []core.Element{element}}, binary.LittleEndian)
		if err != nil {
			t.Fatal(err)
		}
		if diff := dicomtest.DiffSemantic(got, want); diff != "" {
			t.Fatal("short explicit-syntax read changed numeric content:", diff)
		}
		if _, ok := obj.TransferSyntaxResolution(); ok {
			t.Fatal("strict read silently inferred syntax")
		}
		if file, err := ReadFile(bytes.NewReader(data)); err == nil || file != nil {
			t.Fatal("raw dataset accepted as strict Part 10")
		}
	}
}

func TestUpstreamNonDICOMRecoveryRemainsBounded(t *testing.T) {
	data := bytes.Repeat([]byte("NOT_DICOM_DATA\n"), 100)
	if file, err := ReadFile(bytes.NewReader(data)); err == nil || file != nil {
		t.Fatal("strict reader accepted non-DICOM")
	}
	_, resolution, err := ReadFileWithTransferSyntaxRecovery(bytes.NewBuffer(data), ReadFileOptions{MaxElementBytes: 32, MaxTotalBytes: 128}, TransferSyntaxRecoveryOptions{AllowMissingPreamble: true, AllowMissingFileMeta: true, MaxNonSeekableBytes: 128, Probe: parser.TransferSyntaxProbeOptions{MaxProbeBytes: 64}})
	if !errors.Is(err, ErrTransferSyntaxRecoveryBufferExceeded) {
		t.Fatalf("bounded recovery error: %v (%s)", err, resolution.String())
	}
	if strings.Contains(resolution.String(), "NOT_DICOM") || strings.Contains(err.Error(), "NOT_DICOM") {
		t.Fatal("recovery diagnostics exposed input values")
	}
}

func TestUpstreamSpecificCharacterSetUnexpectedValueDoesNotPanic(t *testing.T) {
	for _, value := range []core.Value{core.Uint16Value{192}, core.SequenceValue{}, core.FragmentSequence{Fragments: [][]byte{{1, 2}}}} {
		obj := FromElements([]core.Element{{Header: core.ElementHeader{Tag: tagSpecificCharacterSet, VR: core.VRCS}, Value: value}, core.NewRawElement(core.NewTag(0x0010, 0x0010), core.VRPN, []byte("SYNTHETIC^CHARSET"))}, std.Dictionary)
		if _, err := obj.CharacterSet(); err == nil {
			t.Fatalf("unexpected charset type %T accepted", value)
		}
		if _, err := obj.LookupString(core.NewTag(0x0010, 0x0010)); err == nil {
			t.Fatal("text decoded after charset failure")
		}
	}
	data := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, core.NewRawElement(tagSpecificCharacterSet, core.VROB, []byte("ISO_IR 192")))
	obj, err := ReadDataSet(bytes.NewReader(data), transfer.ExplicitVRLittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := obj.CharacterSet(); err == nil {
		t.Fatal("non-text charset VR accepted")
	}
	// Correct raw CS is supported; a byte representation itself is not invalid.
	obj.Put(core.NewRawElement(tagSpecificCharacterSet, core.VRCS, []byte("ISO_IR 192")))
	if _, err := obj.CharacterSet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamPrivateUNNestedPixelPreservationAndWritePolicy(t *testing.T) {
	data := dicomtest.PrivateUNNestedPixelRegression()
	for _, compatibility := range []bool{false, true} {
		obj, err := ReadDataSetWithOptions(bytes.NewReader(data), transfer.ExplicitVRLittleEndian, ReadFileOptions{AllowMismatchPixelDataLength: compatibility})
		if err != nil {
			t.Fatal(err)
		}
		before, err := dicomtest.SemanticDataSet(obj.ToDataSet(), binary.LittleEndian)
		if err != nil {
			t.Fatal(err)
		}
		seq := obj.ToDataSet().Elements[0].Value.(core.SequenceValue)
		pixel := seq.Items[0].Elements[1]
		raw, ok := pixel.RawBytes()
		if !ok || pixel.Tag() != core.TagPixelData || pixel.VR() != core.VROW || !bytes.Equal(raw, []byte{1, 2, 3, 4}) {
			t.Fatal("opaque nested pixel bytes lost")
		}
		var explicit bytes.Buffer
		err = WriteDataSet(&explicit, obj, transfer.ExplicitVRLittleEndian)
		var writeErr *parser.WriteError
		if !errors.As(err, &writeErr) {
			t.Fatalf("explicit UN sequence must reject unsupported representation: %v", err)
		}
		var implicit bytes.Buffer
		if err := WriteDataSet(&implicit, obj, transfer.ImplicitVRLittleEndian); err != nil {
			t.Fatal(err)
		}
		reread, err := ReadDataSet(bytes.NewReader(implicit.Bytes()), transfer.ImplicitVRLittleEndian)
		if err != nil {
			t.Fatal(err)
		}
		after, err := dicomtest.SemanticDataSet(reread.ToDataSet(), binary.LittleEndian)
		if err != nil {
			t.Fatal(err)
		}
		if diff := dicomtest.DiffSemantic(before, after); diff != "" {
			t.Fatal(diff)
		}
		unchanged, _ := dicomtest.SemanticDataSet(obj.ToDataSet(), binary.LittleEndian)
		if diff := dicomtest.DiffSemantic(before, unchanged); diff != "" {
			t.Fatal("write mutated source:", diff)
		}
	}
}

func TestUpstreamDeferredFileActualConsumptionAndMutation(t *testing.T) {
	payload := bytes.Repeat([]byte{1, 2, 3, 4}, 1024)
	data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, append(dicomtest.MinimalDataset(), dicomtest.NewOBElement(core.TagPixelData, payload))...)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"repeat", "truncate", "same-size-change", "closed", "caller-serialized"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "synthetic.dcm")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			file, err := OpenFileWithOptions(path, ReadFileOptions{DeferPixelData: true, MaxPixelDataBytes: 8192})
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			locs := file.ValueLocations(core.TagPixelData)
			if len(locs) != 1 || locs[0].Length != int64(len(payload)) || locs[0].ValueOffset <= 132 {
				t.Fatalf("locations: %+v", locs)
			}
			want := append([]byte(nil), payload...)
			switch mode {
			case "truncate":
				if err := os.Truncate(path, locs[0].ValueOffset+int64(len(payload))-1); err != nil {
					t.Fatal(err)
				}
			case "same-size-change":
				f, err := os.OpenFile(path, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				want[len(want)-3] ^= 0xff
				_, err = f.WriteAt(want, locs[0].ValueOffset)
				closeErr := f.Close()
				if err != nil || closeErr != nil {
					t.Fatal(err, closeErr)
				}
			case "closed":
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			}
			copyValue := func() error {
				var out bytes.Buffer
				n, err := file.Dataset.CopyValueTo(core.TagPixelData, &out)
				if mode == "truncate" {
					if err == nil || n >= int64(len(payload)) {
						return errors.New("truncated replay reported complete success")
					}
					return nil
				}
				if mode == "closed" {
					if !errors.Is(err, os.ErrClosed) {
						return fmt.Errorf("closed source: %w", err)
					}
					return nil
				}
				if err != nil {
					return err
				}
				if n != int64(len(want)) || !bytes.Equal(out.Bytes(), want) {
					return errors.New("deferred content mismatch")
				}
				return nil
			}
			if mode == "caller-serialized" {
				var mu sync.Mutex // Object/Reader require external synchronization.
				var wg sync.WaitGroup
				errs := make(chan error, 8)
				for i := 0; i < 8; i++ {
					wg.Add(1)
					go func() { defer wg.Done(); mu.Lock(); defer mu.Unlock(); errs <- copyValue() }()
				}
				wg.Wait()
				close(errs)
				for err := range errs {
					if err != nil {
						t.Fatal(err)
					}
				}
			} else {
				for i := 0; i < 2; i++ {
					if err := copyValue(); err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode == "truncate" || mode == "closed" {
				var output bytes.Buffer
				if err := WriteFile(&output, file); err == nil {
					t.Fatal("failed deferred source produced successful file write")
				}
				// io.Writer output can be partial on failure; callers must discard it.
				if recovered, err := ReadFile(bytes.NewReader(output.Bytes())); err == nil && recovered != nil {
					t.Fatal("incomplete write accepted as complete Part 10")
				}
			}
		})
	}
	if _, err := ReadFileWithOptions(bytes.NewReader(data), ReadFileOptions{DeferPixelData: true, MaxPixelDataBytes: 32}); !errors.Is(err, parser.ErrMaxPixelDataBytesExceeded) {
		t.Fatalf("deferred value bypassed configured limit: %v", err)
	}
}
