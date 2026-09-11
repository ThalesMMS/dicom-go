package builtin_test

import (
	"bytes"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/builtin"
	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestNewRegistryBaselineMatrix(t *testing.T) {
	registry, err := builtin.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		transfer.JPEGBaseline.UID,
		transfer.JPEGExtended.UID,
		transfer.RLELossless.UID,
		transfer.JPEGLosslessNonHierarchical.UID,
		transfer.JPEGLosslessSV1.UID,
		transfer.JPEGLSLossless.UID,
		transfer.JPEGLSNearLossless.UID,
	}
	if got := builtin.TransferSyntaxUIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("TransferSyntaxUIDs() = %#v, want %#v", got, want)
	}
	sort.Strings(want)
	if got := registry.RegisteredCodecUIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("registered codecs = %#v, want %#v", got, want)
	}
}

func TestNewRegistryDecodesJPEGLosslessSV1RGBIndependentFixture(t *testing.T) {
	registry, err := builtin.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	fixture := codecfixture.JPEGLosslessSV1RGB8Interleaved()
	result := codecfixture.RunCase(registry, fixture)
	if result.Err != nil {
		t.Fatalf("RunCase() error = %v", result.Err)
	}
	if len(result.Frames.Data) != 1 || !bytes.Equal(result.Frames.Data[0], fixture.ExpectedFrames[0]) {
		t.Fatalf("decoded frames = %#v, want independent exact RGB reference", result.Frames.Data)
	}
}

func TestTransferSyntaxUIDsReturnsCopy(t *testing.T) {
	uids := builtin.TransferSyntaxUIDs()
	uids[0] = "changed"
	if builtin.TransferSyntaxUIDs()[0] == "changed" {
		t.Fatal("TransferSyntaxUIDs exposed mutable package state")
	}
}

func TestNewRegistryReturnsIndependentRegistries(t *testing.T) {
	first, err := builtin.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	second, err := builtin.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	const optionalUID = "1.2.826.0.1.3680043.10.543.765.1"
	if err := first.RegisterCodec(optionalUID, noOpCodec{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := second.GetCodec(optionalUID); ok {
		t.Fatal("caller registration leaked into another registry")
	}
}

func TestRegisterPreservesFamilyErrorContext(t *testing.T) {
	sentinel := errors.New("registration failed")
	for _, test := range []struct {
		name        string
		failureCall int
		want        string
	}{
		{name: "JPEG Baseline / Extended", failureCall: 1, want: "register JPEG Baseline / Extended codec"},
		{name: "RLE", failureCall: 3, want: "register RLE codec"},
		{name: "JPEG Lossless", failureCall: 4, want: "register JPEG Lossless codec"},
		{name: "JPEG-LS Lossless", failureCall: 6, want: "register JPEG-LS Lossless codec"},
		{name: "JPEG-LS Near-Lossless", failureCall: 7, want: "register JPEG-LS Near-Lossless codec"},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := &failingRegistry{failureCall: test.failureCall, err: sentinel}
			err := builtin.Register(registry)
			if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Register() error = %v, want %q wrapping sentinel", err, test.want)
			}
		})
	}
}

func TestRegisterNilPreservesFirstFamilyErrorContext(t *testing.T) {
	err := builtin.Register(nil)
	if !errors.Is(err, pixeldata.ErrCodecRegistryNil) || !strings.Contains(err.Error(), "register JPEG Baseline / Extended codec") {
		t.Fatalf("Register(nil) error = %v", err)
	}
}

type noOpCodec struct{}

func (noOpCodec) Decode(pixeldata.PixelData, *object.Object) (pixeldata.Frames, error) {
	return pixeldata.Frames{}, nil
}

type failingRegistry struct {
	calls       int
	failureCall int
	err         error
}

func (registry *failingRegistry) RegisterCodec(string, pixeldata.Codec) error {
	registry.calls++
	if registry.calls == registry.failureCall {
		return registry.err
	}
	return nil
}

func (*failingRegistry) GetCodec(string) (pixeldata.Codec, bool) {
	return nil, false
}

func (*failingRegistry) DecodeFrames(string, pixeldata.PixelData, *object.Object) (pixeldata.Frames, error) {
	return pixeldata.Frames{}, nil
}
