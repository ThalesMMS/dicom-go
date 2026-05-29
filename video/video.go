// Package video validates and extracts encapsulated DICOM MPEG-2, AVC/H.264,
// and HEVC/H.265 media streams without decoding them.
package video

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	dicomtags "github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	// DefaultMaxBytes bounds one extracted media stream. Callers may choose a
	// smaller policy limit, but zero-valued Limits never disable this guard.
	DefaultMaxBytes int64 = 1 << 30
	// DefaultMaxFragments prevents tiny-fragment resource exhaustion.
	DefaultMaxFragments = 131072
	probeBytes          = 4096
)

var (
	ErrNotVideoTransferSyntax = errors.New("dicom: transfer syntax is not supported DICOM video")
	ErrPixelDataUnavailable   = errors.New("dicom: video Pixel Data is unavailable")
	ErrMalformedFragments     = errors.New("dicom: malformed video fragment sequence")
	ErrMediaTooLarge          = errors.New("dicom: video media exceeds extraction limit")
	ErrTooManyFragments       = errors.New("dicom: video media exceeds fragment limit")
	ErrUnsupportedContainer   = errors.New("dicom: unsupported video container")
)

type Codec string

const (
	CodecMPEG2 Codec = "MPEG-2"
	CodecH264  Codec = "H.264/AVC"
	CodecHEVC  Codec = "H.265/HEVC"
)

type Container string

const (
	ContainerMPEGTransport  Container = "MPEG-TS"
	ContainerMP4            Container = "MP4"
	ContainerMPEGProgram    Container = "MPEG-PS/PES"
	ContainerMPEGElementary Container = "MPEG-ES"
)

// Limits bounds extraction work. Non-positive fields use the safe defaults.
type Limits struct {
	MaxBytes     int64
	MaxFragments int
}

func (l Limits) normalized() Limits {
	if l.MaxBytes <= 0 {
		l.MaxBytes = DefaultMaxBytes
	}
	if l.MaxFragments <= 0 {
		l.MaxFragments = DefaultMaxFragments
	}
	return l
}

// Info describes the original DICOM media stream and its detected container.
type Info struct {
	TransferSyntaxUID  string
	TransferSyntaxName string
	Codec              Codec
	Container          Container
	Extension          string
	MIMEType           string
	FragmentCount      int
	Size               int64
	Rows               int
	Columns            int
	NumberOfFrames     int
	FrameRate          float64
	FrameTimeMillis    float64
}

// Inspect validates a materialized or deferred stream without retaining a
// concatenated copy.
func Inspect(ctx context.Context, file *object.File, limits Limits) (Info, error) {
	return Copy(ctx, file, io.Discard, limits)
}

// Metadata returns the DICOM-side description without reading Pixel Data. It is
// suitable for archive listings whose Pixel Data was intentionally skipped.
func Metadata(file *object.File) (Info, error) {
	info, err := baseInfo(file)
	if err != nil {
		return Info{}, err
	}
	return info, nil
}

// Copy concatenates the ordered fragment payloads into dst while validating
// DICOM video fragmentation, container, cancellation, and resource limits.
// It never changes the source object or transfer syntax.
func Copy(ctx context.Context, file *object.File, dst io.Writer, limits Limits) (Info, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if dst == nil {
		return Info{}, errors.New("dicom: video destination is nil")
	}
	info, err := baseInfo(file)
	if err != nil {
		return Info{}, err
	}
	limits = limits.normalized()
	elem, ok := file.Dataset.Get(core.TagPixelData)
	if !ok {
		return info, ErrPixelDataUnavailable
	}

	probe := &prefixWriter{dst: dst, remaining: probeBytes}
	switch fragments := elem.Value.(type) {
	case core.FragmentSequence:
		err = copyMaterialized(ctx, fragments, probe, limits, &info)
	case nil:
		err = copyDeferred(ctx, file.Dataset, probe, limits, &info)
	default:
		err = fmt.Errorf("%w: Pixel Data value has type %T", ErrMalformedFragments, elem.Value)
	}
	if err != nil {
		return info, err
	}
	info.Container, info.Extension, info.MIMEType, err = detectContainer(info.Codec, probe.prefix.Bytes())
	if err != nil {
		return info, err
	}
	return info, nil
}

func baseInfo(file *object.File) (Info, error) {
	if file == nil || file.Dataset == nil {
		return Info{}, errors.New("dicom: video source has no dataset")
	}
	uid := transfer.NormalizeUID(file.TransferSyntax.UID)
	if !transfer.IsVideoTransferSyntax(uid) {
		return Info{}, fmt.Errorf("%w: %q", ErrNotVideoTransferSyntax, uid)
	}
	info := Info{
		TransferSyntaxUID:  uid,
		TransferSyntaxName: strings.TrimSpace(file.TransferSyntax.Name),
		Codec:              codecForSyntax(uid),
	}
	if info.TransferSyntaxName == "" {
		if syntax, ok := transfer.DefaultRegistry.Get(uid); ok {
			info.TransferSyntaxName = syntax.Name
		}
	}
	if value, err := file.GetInt(dicomtags.Rows); err == nil && value > 0 && value <= math.MaxInt {
		info.Rows = int(value)
	}
	if value, err := file.GetInt(dicomtags.Columns); err == nil && value > 0 && value <= math.MaxInt {
		info.Columns = int(value)
	}
	if value, err := file.GetInt(dicomtags.NumberOfFrames); err == nil && value > 0 && value <= math.MaxInt {
		info.NumberOfFrames = int(value)
	}
	info.FrameRate, info.FrameTimeMillis = timing(file)
	return info, nil
}

func timing(file *object.File) (rate, frameTimeMillis float64) {
	if value, err := file.GetFloat(dicomtags.FrameTime); err == nil && value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0) {
		frameTimeMillis = value
		rate = 1000 / value
	}
	if value, err := file.GetInt(dicomtags.RecommendedDisplayFrameRate); err == nil && value > 0 {
		rate = float64(value)
		if frameTimeMillis <= 0 {
			frameTimeMillis = 1000 / rate
		}
	} else if value, err := file.GetInt(dicomtags.CineRate); err == nil && value > 0 && rate <= 0 {
		rate = float64(value)
		frameTimeMillis = 1000 / rate
	}
	if frameTimeMillis <= 0 {
		if vector, err := file.GetFloats(dicomtags.FrameTimeVector); err == nil && len(vector) > 1 {
			var total float64
			valid := vector[0] == 0
			for _, increment := range vector[1:] {
				if increment <= 0 || math.IsNaN(increment) || math.IsInf(increment, 0) {
					valid = false
					break
				}
				total += increment
			}
			if valid {
				frameTimeMillis = total / float64(len(vector)-1)
				rate = 1000 / frameTimeMillis
			}
		}
	}
	if frameTimeMillis <= 0 {
		if value, err := file.GetInt(dicomtags.ActualFrameDuration); err == nil && value > 0 {
			frameTimeMillis = float64(value)
			rate = 1000 / frameTimeMillis
		}
	}
	return rate, frameTimeMillis
}

func copyMaterialized(ctx context.Context, sequence core.FragmentSequence, dst io.Writer, limits Limits, info *Info) error {
	if len(sequence.OffsetTable) != 0 {
		return fmt.Errorf("%w: Basic Offset Table must be empty for video", ErrMalformedFragments)
	}
	if err := validateFragmentCount(info.TransferSyntaxUID, len(sequence.Fragments), limits.MaxFragments); err != nil {
		return err
	}
	for _, fragment := range sequence.Fragments {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(fragment) == 0 {
			return fmt.Errorf("%w: empty media fragment", ErrMalformedFragments)
		}
		if int64(len(fragment)) > limits.MaxBytes-info.Size {
			return fmt.Errorf("%w: limit %d bytes", ErrMediaTooLarge, limits.MaxBytes)
		}
		n, err := dst.Write(fragment)
		info.Size += int64(n)
		info.FragmentCount++
		if err != nil {
			return fmt.Errorf("dicom: write video fragment: %w", err)
		}
		if n != len(fragment) {
			return io.ErrShortWrite
		}
	}
	return nil
}

func copyDeferred(ctx context.Context, dataset *object.Object, dst io.Writer, limits Limits, info *Info) error {
	reader, writer := io.Pipe()
	copyResult := make(chan error, 1)
	go func() {
		_, err := dataset.CopyValueTo(core.TagPixelData, writer)
		_ = writer.CloseWithError(err)
		copyResult <- err
	}()

	parseErr := copyEncodedFragments(ctx, reader, dst, limits, info)
	_ = reader.CloseWithError(parseErr)
	providerErr := <-copyResult
	if parseErr != nil {
		return parseErr
	}
	if providerErr != nil {
		return fmt.Errorf("dicom: replay deferred video Pixel Data: %w", providerErr)
	}
	return nil
}

func copyEncodedFragments(ctx context.Context, src io.Reader, dst io.Writer, limits Limits, info *Info) error {
	var header [8]byte
	itemIndex := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := io.ReadFull(src, header[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return fmt.Errorf("%w: truncated fragment header", ErrMalformedFragments)
			}
			return err
		}
		group := binary.LittleEndian.Uint16(header[0:2])
		element := binary.LittleEndian.Uint16(header[2:4])
		length := int64(binary.LittleEndian.Uint32(header[4:8]))
		switch {
		case group == 0xFFFE && element == 0xE0DD:
			if length != 0 {
				return fmt.Errorf("%w: non-zero sequence delimiter length", ErrMalformedFragments)
			}
			return validateFragmentCount(info.TransferSyntaxUID, info.FragmentCount, limits.MaxFragments)
		case group != 0xFFFE || element != 0xE000:
			return fmt.Errorf("%w: unexpected item tag (%04X,%04X)", ErrMalformedFragments, group, element)
		case itemIndex == 0:
			if length != 0 {
				return fmt.Errorf("%w: Basic Offset Table must be empty for video", ErrMalformedFragments)
			}
			itemIndex++
		default:
			if length == 0 {
				return fmt.Errorf("%w: empty media fragment", ErrMalformedFragments)
			}
			if info.FragmentCount >= limits.MaxFragments {
				return fmt.Errorf("%w: limit %d", ErrTooManyFragments, limits.MaxFragments)
			}
			if length > limits.MaxBytes-info.Size {
				return fmt.Errorf("%w: limit %d bytes", ErrMediaTooLarge, limits.MaxBytes)
			}
			n, err := io.CopyN(&contextWriter{ctx: ctx, dst: dst}, src, length)
			info.Size += n
			info.FragmentCount++
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
					return fmt.Errorf("%w: truncated media fragment", ErrMalformedFragments)
				}
				return err
			}
			itemIndex++
		}
	}
}

func validateFragmentCount(uid string, count, max int) error {
	if count <= 0 {
		return fmt.Errorf("%w: no media fragments", ErrMalformedFragments)
	}
	if count > max {
		return fmt.Errorf("%w: got %d, limit %d", ErrTooManyFragments, count, max)
	}
	if !isFragmentable(uid) && count != 1 {
		return fmt.Errorf("%w: non-fragmentable transfer syntax %q has %d fragments", ErrMalformedFragments, uid, count)
	}
	return nil
}

func isFragmentable(uid string) bool {
	switch transfer.NormalizeUID(uid) {
	case transfer.MPEG2MPMLF.UID,
		transfer.MPEG2MPHLF.UID,
		transfer.MPEG4HP41F.UID,
		transfer.MPEG4HP41BDF.UID,
		transfer.MPEG4HP422DF.UID,
		transfer.MPEG4HP423DF.UID,
		transfer.MPEG4HP42STEREOF.UID:
		return true
	default:
		return false
	}
}

func codecForSyntax(uid string) Codec {
	switch transfer.NormalizeUID(uid) {
	case transfer.MPEG2MPML.UID, transfer.MPEG2MPMLF.UID,
		transfer.MPEG2MPHL.UID, transfer.MPEG2MPHLF.UID:
		return CodecMPEG2
	case transfer.HEVCMP51.UID, transfer.HEVCM10P51.UID:
		return CodecHEVC
	default:
		return CodecH264
	}
}

func detectContainer(codec Codec, data []byte) (Container, string, string, error) {
	var container Container
	switch {
	case len(data) >= 12 && string(data[4:8]) == "ftyp":
		container = ContainerMP4
	case isMPEGTransportStream(data):
		container = ContainerMPEGTransport
	case len(data) >= 4 && bytes.Equal(data[:4], []byte{0x00, 0x00, 0x01, 0xBA}):
		container = ContainerMPEGProgram
	case len(data) >= 4 && data[0] == 0 && data[1] == 0 && data[2] == 1 && data[3] >= 0xBD && data[3] <= 0xEF:
		container = ContainerMPEGProgram
	case len(data) >= 4 && bytes.Equal(data[:4], []byte{0x00, 0x00, 0x01, 0xB3}):
		container = ContainerMPEGElementary
	default:
		return "", "", "", fmt.Errorf("%w: unrecognized %s stream signature", ErrUnsupportedContainer, codec)
	}
	if codec != CodecMPEG2 && container != ContainerMP4 && container != ContainerMPEGTransport {
		return "", "", "", fmt.Errorf("%w: %s requires MP4 or MPEG-TS, got %s", ErrUnsupportedContainer, codec, container)
	}
	switch container {
	case ContainerMP4:
		return container, ".mp4", "video/mp4", nil
	case ContainerMPEGTransport:
		return container, ".ts", "video/mp2t", nil
	case ContainerMPEGElementary:
		return container, ".m2v", "video/mpeg", nil
	default:
		return container, ".mpg", "video/mpeg", nil
	}
}

func isMPEGTransportStream(data []byte) bool {
	if len(data) < 188 || data[0] != 0x47 {
		return false
	}
	for offset, checked := 188, 1; offset < len(data) && checked < 4; offset, checked = offset+188, checked+1 {
		if data[offset] != 0x47 {
			return false
		}
	}
	return true
}

type prefixWriter struct {
	dst       io.Writer
	prefix    bytes.Buffer
	remaining int
}

func (w *prefixWriter) Write(p []byte) (int, error) {
	if w.remaining > 0 {
		n := len(p)
		if n > w.remaining {
			n = w.remaining
		}
		_, _ = w.prefix.Write(p[:n])
		w.remaining -= n
	}
	return w.dst.Write(p)
}

type contextWriter struct {
	ctx context.Context
	dst io.Writer
}

func (w *contextWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.dst.Write(p)
}
