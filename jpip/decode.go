package jpip

import (
	"context"
	"errors"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// DecodedFrame is the native frame produced from one complete JPIP response.
type DecodedFrame struct {
	Metadata    pixeldata.Metadata
	Data        []byte
	ContentType string
	CacheHit    bool
}

// DecodeFrame retrieves and decodes one complete JPEG 2000 or HTJ2K response.
// JPP/JPT data-bin streams are returned as a stable typed limitation because
// they require session-level data-bin assembly rather than a still-image codec.
func (c *Client) DecodeFrame(ctx context.Context, request Request, dataset *object.Object) (DecodedFrame, error) {
	if dataset == nil {
		return DecodedFrame{}, &Error{Kind: ErrorKindInvalidRequest, Operation: "decode", Err: ErrInvalidRequest}
	}
	if request.Range != nil {
		return DecodedFrame{}, &Error{Kind: ErrorKindInvalidRequest, Operation: "decode", Err: ErrInvalidRequest}
	}
	response, err := c.Retrieve(ctx, request)
	if err != nil {
		return DecodedFrame{}, err
	}
	switch response.ContentType {
	case "image/jpp-stream", "image/jpt-stream":
		return DecodedFrame{}, &Error{
			Kind: ErrorKindIncrementalStream, Operation: "decode",
			ContentType: response.ContentType, Err: ErrIncrementalStream,
		}
	}
	if !contentTypeMatchesReference(response.ContentType, request.Reference.HTJ2K) {
		return DecodedFrame{}, &Error{
			Kind: ErrorKindDecode, Operation: "decode",
			ContentType: response.ContentType, Err: errors.Join(ErrDecode, ErrUnsupportedContentType),
		}
	}
	if c.registry == nil {
		return DecodedFrame{}, &Error{Kind: ErrorKindDecode, Operation: "decode", Err: errors.Join(ErrDecode, pixeldata.ErrCodecRegistryNil)}
	}

	// A JPIP stream query selects one one-based code stream. Override only the
	// cloned facade so codecs validate this response as one frame while the
	// caller's DICOM metadata remains unchanged.
	decoderDataset := object.FromElements(dataset.Elements(), std.Dictionary)
	decoderDataset.Put(core.NewRawElement(tags.NumberOfFrames, core.VRIS, []byte("1 ")))
	uid := transfer.JPEG2000.UID
	if request.Reference.HTJ2K {
		uid = transfer.HTJ2K.UID
	}
	frames, err := c.registry.DecodeFrames(uid, pixeldata.PixelData{
		Encapsulated: true,
		Sequence: core.FragmentSequence{
			Fragments: [][]byte{response.Data},
		},
	}, decoderDataset)
	if err != nil {
		return DecodedFrame{}, &Error{Kind: ErrorKindDecode, Operation: "decode", ContentType: response.ContentType, Err: errors.Join(ErrDecode, err)}
	}
	if len(frames.Data) != 1 || len(frames.Data[0]) == 0 {
		return DecodedFrame{}, &Error{Kind: ErrorKindCorruptResponse, Operation: "decode", ContentType: response.ContentType, Err: ErrCorruptResponse}
	}
	metadata, err := pixeldata.ExtractMetadata(decoderDataset)
	if err != nil {
		return DecodedFrame{}, &Error{Kind: ErrorKindDecode, Operation: "decode metadata", Err: errors.Join(ErrDecode, err)}
	}
	metadata.NumberOfFrames = 1
	return DecodedFrame{
		Metadata: metadata, Data: frames.Data[0],
		ContentType: response.ContentType, CacheHit: response.CacheHit,
	}, nil
}

func contentTypeMatchesReference(contentType string, htj2k bool) bool {
	if htj2k {
		return contentType == "image/jph" || contentType == "image/jphc"
	}
	return contentType == "image/jp2"
}
