package microscopy

import (
	"context"
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/net/dicomweb"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestLocalTileSourceDecodesOnlyRequestedNativeFrame(t *testing.T) {
	file := nativeMicroscopyPixelFixture([]byte{
		0, 10, 20, 30,
		200, 210, 220, 230,
	})
	opened := 0
	source := LocalTileSource{Open: func(ctx context.Context, ref InstanceRef) (*object.File, error) {
		opened++
		return file, nil
	}}
	image, err := source.FetchTile(context.Background(), Tile{
		Source: InstanceRef{SOPInstanceUID: "instance"}, FrameNumber: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if opened != 1 || image.Bounds().Dx() != 2 || image.Bounds().Dy() != 2 {
		t.Fatalf("opened=%d bounds=%v", opened, image.Bounds())
	}
	if pixel := color.GrayModel.Convert(image.At(0, 0)).(color.Gray); pixel.Y < 199 || pixel.Y > 202 {
		t.Fatalf("first pixel of requested frame = %d, want approximately 200", pixel.Y)
	}
}

func TestDICOMwebTileSourceRetrievesExactlyOneRequestedFrame(t *testing.T) {
	client := &recordingFrameClient{}
	source := DICOMwebTileSource{
		Client: client,
		Metadata: map[string]pixeldata.Metadata{
			"instance": {
				Rows: 2, Columns: 2, SamplesPerPixel: 1,
				PhotometricInterpretation: "MONOCHROME2",
				NumberOfFrames:            1, BitsAllocated: 8, BitsStored: 8, HighBit: 7,
			},
		},
		Options: dicomweb.RetrieveOptions{TransferSyntaxUIDs: []string{transfer.ExplicitVRLittleEndian.UID}},
	}
	image, err := source.FetchTile(context.Background(), Tile{
		Source: InstanceRef{
			StudyInstanceUID: "study", SeriesInstanceUID: "series", SOPInstanceUID: "instance",
		},
		FrameNumber: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.frames) != 1 || client.frames[0] != 7 {
		t.Fatalf("retrieved frames = %#v", client.frames)
	}
	if pixel := color.GrayModel.Convert(image.At(0, 0)).(color.Gray); pixel.Y < 39 || pixel.Y > 42 {
		t.Fatalf("first decoded pixel = %d, want approximately 40", pixel.Y)
	}
}

func TestLocalTileSourceStreamsDeferredNativeFrameWithoutMaterializingPixelData(t *testing.T) {
	sourceFile := nativeMicroscopyPixelFixture([]byte{
		0, 10, 20, 30,
		200, 210, 220, 230,
	})
	path := filepath.Join(t.TempDir(), "slide.dcm")
	output, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := object.WriteFile(output, sourceFile); err != nil {
		_ = output.Close()
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}

	source := LocalTileSource{Open: func(ctx context.Context, ref InstanceRef) (*object.File, error) {
		file, err := object.OpenFileWithOptions(path, object.ReadFileOptions{DeferPixelData: true})
		if err != nil {
			return nil, err
		}
		element, ok := file.Dataset.Get(derivedio.TagPixelData)
		if !ok || element.Value != nil {
			_ = file.Close()
			t.Fatalf("Pixel Data was materialized: present=%v value=%T", ok, element.Value)
		}
		return file, nil
	}}
	image, err := source.FetchTile(context.Background(), Tile{FrameNumber: 2})
	if err != nil {
		t.Fatal(err)
	}
	if pixel := color.GrayModel.Convert(image.At(0, 0)).(color.Gray); pixel.Y < 199 || pixel.Y > 202 {
		t.Fatalf("first streamed pixel = %d, want approximately 200", pixel.Y)
	}
}

type recordingFrameClient struct {
	frames []int
}

func (c *recordingFrameClient) RetrieveFrames(
	_ context.Context,
	_ dicomweb.InstanceRef,
	frames []int,
	_ dicomweb.RetrieveOptions,
) ([]dicomweb.FramePart, error) {
	c.frames = append(c.frames, frames...)
	return []dicomweb.FramePart{{
		FrameNumber: frames[0], TransferSyntaxUID: transfer.ExplicitVRLittleEndian.UID,
		Data: []byte{40, 50, 60, 70},
	}}, nil
}

func nativeMicroscopyPixelFixture(pixels []byte) *object.File {
	return &object.File{
		Meta: derivedio.Object(
			derivedio.UI(derivedio.TagMediaStorageSOPClassUID, VLWholeSlideMicroscopyImageStorage),
			derivedio.UI(derivedio.TagMediaStorageSOPInstanceUID, "1.2.3.4"),
			derivedio.UI(derivedio.TagTransferSyntaxUID, transfer.ExplicitVRLittleEndian.UID),
		),
		Dataset: derivedio.Object(
			derivedio.UI(derivedio.TagSOPClassUID, VLWholeSlideMicroscopyImageStorage),
			derivedio.UI(derivedio.TagSOPInstanceUID, "1.2.3.4"),
			derivedio.US(derivedio.TagRows, 2),
			derivedio.US(derivedio.TagColumns, 2),
			derivedio.US(derivedio.TagSamplesPerPixel, 1),
			derivedio.CS(derivedio.TagPhotometricInterpretation, "MONOCHROME2"),
			derivedio.IS(derivedio.TagNumberOfFrames, 2),
			derivedio.US(derivedio.TagBitsAllocated, 8),
			derivedio.US(derivedio.TagBitsStored, 8),
			derivedio.US(derivedio.TagHighBit, 7),
			derivedio.US(derivedio.TagPixelRepresentation, 0),
			derivedio.Raw(derivedio.TagPixelData, core.VROB, pixels),
		),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}
}
