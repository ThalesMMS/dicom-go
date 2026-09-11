package object

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestNativeFrameSinkKeepsNestedPixelDataSeparate(t *testing.T) {
	nested := core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x0088, 0x0200), VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{{Elements: nativeFrameStreamingElements([]byte{9, 8, 7, 6}, "1")}}}}
	elements := nativeFrameStreamingElements(sequentialFrameBytes(12), "3")
	elements = append([]core.Element{nested}, elements...)
	sink := &recordingFrameSink{}
	obj, err := ReadDataSetWithOptions(bytes.NewReader(dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, elements...)), transfer.ExplicitVRLittleEndian, ReadFileOptions{FrameSink: sink})
	if err != nil || len(sink.frames) != 3 {
		t.Fatalf("main frames: %v count=%d", err, len(sink.frames))
	}
	el, _ := obj.Get(nested.Tag())
	seq := el.Value.(core.SequenceValue)
	var found bool
	for _, el := range seq.Items[0].Elements {
		if el.Tag() == core.TagPixelData {
			found = bytes.Equal([]byte(el.Value.(core.RawValue)), []byte{9, 8, 7, 6})
		}
	}
	if !found {
		t.Fatal("nested Pixel Data changed")
	}
	for i, f := range sink.frames {
		if f.Index != i || f.Metadata.NumberOfFrames != 3 || !bytes.Equal(f.Data, sequentialFrameBytes(12)[i*4:(i+1)*4]) {
			t.Fatal("nested metadata mixed into main image")
		}
	}
}

type countingCloseFrameSink struct {
	FrameSink
	count    int
	closeErr error
}

func (s *countingCloseFrameSink) Close() error { s.count++; return s.closeErr }

func TestFrameSinkFinalizesBeforeParserFailures(t *testing.T) {
	for _, mode := range []string{"header", "syntax", "open-file", "open-dataset", "recovery-file", "recovery-dataset", "recovery-open-file", "recovery-open-dataset"} {
		t.Run(mode, func(t *testing.T) {
			closeErr := errors.New("synthetic finalization failure")
			sink := &countingCloseFrameSink{FrameSink: FrameSinkFunc(nil), closeErr: closeErr}
			opts := ReadFileOptions{FrameSink: sink}
			var err error
			switch mode {
			case "header":
				_, err = ReadFileWithOptions(bytes.NewReader([]byte("invalid")), opts)
			case "syntax":
				_, err = ReadDataSetWithOptions(bytes.NewReader(nil), transfer.Syntax{UID: "0.0"}, opts)
			case "open-file":
				_, err = OpenFileWithOptions(filepath.Join(t.TempDir(), "absent.dcm"), opts)
			case "open-dataset":
				_, err = OpenDataSetWithOptions(filepath.Join(t.TempDir(), "absent.dcm"), transfer.ExplicitVRLittleEndian, opts)
			case "recovery-file":
				_, _, err = ReadFileWithTransferSyntaxRecovery(bytes.NewReader(nil), opts, TransferSyntaxRecoveryOptions{AllowMissingPreamble: true})
			case "recovery-dataset":
				_, _, err = ReadDataSetWithTransferSyntaxRecovery(bytes.NewReader(nil), opts, TransferSyntaxRecoveryOptions{})
			case "recovery-open-file":
				_, _, err = OpenFileWithTransferSyntaxRecovery(filepath.Join(t.TempDir(), "absent.dcm"), opts, TransferSyntaxRecoveryOptions{})
			case "recovery-open-dataset":
				_, _, err = OpenDataSetWithTransferSyntaxRecovery(filepath.Join(t.TempDir(), "absent.dcm"), opts, TransferSyntaxRecoveryOptions{})
			}
			if sink.count != 1 || !errors.Is(err, closeErr) {
				t.Fatalf("close count=%d error=%v", sink.count, err)
			}
		})
	}
}

type lifecycleChannelSink struct {
	FrameSink
	entered chan struct{}
	count   int
	fail    error
}

func (s *lifecycleChannelSink) HandleFrame(f Frame) error {
	if s.entered != nil {
		close(s.entered)
		s.entered = nil
	}
	if s.fail != nil {
		return s.fail
	}
	return s.FrameSink.HandleFrame(f)
}
func (s *lifecycleChannelSink) Close() error { s.count++; return s.FrameSink.Close() }

func TestFrameChannelContextReadLifecycle(t *testing.T) {
	for _, mode := range []string{"normal", "cancel", "truncated", "sink-error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			capacity := 3
			if mode == "cancel" {
				capacity = 0
			}
			ch := make(chan Frame, capacity)
			entered := make(chan struct{})
			sink := &lifecycleChannelSink{FrameSink: NewFrameChannelSinkContext(ctx, ch), entered: entered}
			failure := errors.New("synthetic consumer failure")
			if mode == "sink-error" {
				sink.fail = failure
			}
			data := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, nativeFrameStreamingElements(sequentialFrameBytes(12), "3")...)
			if mode == "truncated" {
				data = data[:len(data)-1]
			}
			done := make(chan error, 1)
			go func() {
				_, err := ReadDataSetWithOptions(bytes.NewReader(data), transfer.ExplicitVRLittleEndian, ReadFileOptions{FrameSink: sink})
				done <- err
			}()
			if mode == "cancel" {
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("no frame reached sink")
				}
				cancel()
			}
			var err error
			select {
			case err = <-done:
			case <-time.After(time.Second):
				t.Fatal("read failed to finalize")
			}
			switch mode {
			case "normal":
				if err != nil {
					t.Fatal(err)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) || !errors.Is(err, parser.ErrFrameSink) {
					t.Fatalf("cancel chain: %v", err)
				}
			case "sink-error":
				if !errors.Is(err, failure) || !errors.Is(err, parser.ErrFrameSink) {
					t.Fatalf("sink error chain: %v", err)
				}
			case "truncated":
				if !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("parse error chain: %v", err)
				}
			}
			if sink.count != 1 {
				t.Fatalf("Close calls=%d", sink.count)
			}
			count := 0
			for {
				select {
				case _, ok := <-ch:
					if !ok {
						if mode == "normal" && count != 3 {
							t.Fatalf("frames=%d", count)
						}
						return
					}
					count++
				default:
					t.Fatal("channel remained open")
				}
			}
		})
	}
}
